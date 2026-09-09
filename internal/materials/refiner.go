package materials

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

const refineTimeout = 30 * time.Second

// Completer is the B16-shaped LLM seam (same as drill/review).
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// BlockWriter writes refined rows into phrase_blocks.
type BlockWriter interface {
	SaveAcceptedBlocks(ctx context.Context, blocks []corpus.PhraseBlock) ([]corpus.PhraseBlock, error)
}

// Refine runs queued → processing → ready|failed. A second call on a
// non-queued row is a no-op.
func (s *Service) Refine(ctx context.Context, materialID string) error {
	materialID = strings.TrimSpace(materialID)
	if materialID == "" {
		return nil
	}
	m, err := s.store.GetMaterial(ctx, "", materialID)
	if err != nil {
		return err
	}
	if m.DeletedAt != nil {
		return nil
	}
	if m.RefineStatus != StatusQueued {
		return nil
	}
	now := s.now().UTC()
	if err := s.store.MarkProcessing(ctx, materialID, now); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil
		}
		return err
	}
	incTransition(StatusQueued, StatusProcessing)

	if s.llm == nil {
		return s.fail(ctx, materialID, ErrorLLMTimeout)
	}
	callCtx, cancel := context.WithTimeout(ctx, refineTimeout)
	defer cancel()
	raw, err := s.llm.Complete(callCtx, RefinePrompt(m.Kind, m.Content))
	if err != nil {
		incTimeout()
		return s.fail(ctx, materialID, ErrorLLMTimeout)
	}
	result, ok := parseRefineJSON(raw)
	if !ok {
		incParseError()
		return s.fail(ctx, materialID, ErrorParse)
	}
	if len(result.Blocks) == 0 {
		if err := s.store.MarkRefined(ctx, materialID, 0, ErrorNoChunks, s.now().UTC()); err != nil {
			return err
		}
		incTransition(StatusProcessing, StatusReady)
		return nil
	}
	if s.blocks == nil {
		return s.fail(ctx, materialID, ErrorDB)
	}
	rows := toPhraseBlocks(m, result.Blocks, s.now().UTC(), s.newID)
	if _, err := s.blocks.SaveAcceptedBlocks(ctx, rows); err != nil {
		return s.fail(ctx, materialID, ErrorDB)
	}
	if err := s.store.MarkRefined(ctx, materialID, len(rows), "", s.now().UTC()); err != nil {
		return err
	}
	incTransition(StatusProcessing, StatusReady)
	return nil
}

func (s *Service) fail(ctx context.Context, materialID, code string) error {
	if err := s.store.MarkRefineFailed(ctx, materialID, code, s.now().UTC()); err != nil {
		return err
	}
	incTransition(StatusProcessing, StatusFailed)
	return nil
}

func parseRefineJSON(raw string) (RefineResult, bool) {
	trimmed := strings.TrimSpace(raw)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var out RefineResult
	if json.Unmarshal([]byte(trimmed), &out) != nil {
		return RefineResult{}, false
	}
	if out.Blocks == nil {
		out.Blocks = []RefinedBlock{}
	}
	return out, true
}

func toPhraseBlocks(m Material, blocks []RefinedBlock, now time.Time, newID func() string) []corpus.PhraseBlock {
	out := make([]corpus.PhraseBlock, 0, len(blocks))
	for _, b := range blocks {
		expr := strings.TrimSpace(b.ExpressionEN)
		intent := strings.TrimSpace(b.IntentZH)
		if expr == "" {
			continue
		}
		if newID == nil {
			newID = uuid.NewString
		}
		anchor := expr
		if utf8.RuneCountInString(m.Content) > 0 {
			anchor = clipRunes(m.Content, 80)
		}
		out = append(out, corpus.PhraseBlock{
			ID:             newID(),
			UserID:         m.UserID,
			IntentZH:       intent,
			ExpressionEN:   expr,
			AnchorUserSaid: anchor,
			SceneTag:       normalizeScene(b.SceneTag),
			FunctionTag:    normalizeFunction(b.FunctionTag),
			State:          corpus.StateNew,
			NextDueAt:      now,
			EaseFactor:     2.5,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	return out
}

func normalizeScene(tag string) string {
	switch strings.TrimSpace(tag) {
	case "standup", "review", "1on1", "interview", "casual":
		return tag
	default:
		return "casual"
	}
}

func normalizeFunction(tag string) string {
	switch strings.TrimSpace(tag) {
	case "object", "clarify", "report", "propose", "agree", "disagree", "ask", "summarize", "defer", "commit":
		return tag
	default:
		return "report"
	}
}

func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit])
}
