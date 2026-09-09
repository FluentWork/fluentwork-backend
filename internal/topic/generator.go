package topic

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Completer is the B16-shaped LLM seam (same as drill/review/materials).
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// SignalSource loads practice stats for the generator prompt.
type SignalSource interface {
	Snapshot(ctx context.Context, userID string, now time.Time) (Signals, error)
}

// Generator writes three topic cards for one user/day.
type Generator struct {
	store Store
	llm   Completer
	sig   SignalSource
	now   func() time.Time
	newID func() string
}

// NewGenerator constructs the daily card writer.
func NewGenerator(store Store, llm Completer, sig SignalSource) *Generator {
	return &Generator{store: store, llm: llm, sig: sig, now: time.Now, newID: uuid.NewString}
}

// GenerateForUser inserts today's cards unless they already exist.
func (g *Generator) GenerateForUser(ctx context.Context, userID string, targetDate time.Time) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	day := utcDate(targetDate)
	existing, err := g.store.ListTodayCards(ctx, userID, day)
	if err != nil {
		incGenerated(false)
		return err
	}
	if len(existing) > 0 {
		incSkip(SkipAlreadyGenerated)
		return nil
	}
	if has, err := g.store.HasCardsForDate(ctx, userID, day); err != nil {
		incGenerated(false)
		return err
	} else if has {
		incSkip(SkipAlreadyGenerated)
		return nil
	}
	if g.llm == nil {
		incSkip(SkipNoLLM)
		incGenerated(false)
		return nil
	}
	sig := Signals{Level: "intermediate", Empty: true, SceneCounts: map[string]int{}, FunctionCounts: map[string]int{}}
	if g.sig != nil {
		got, snapErr := g.sig.Snapshot(ctx, userID, g.now())
		if snapErr == nil {
			sig = got
		}
	}
	var raw string
	var lastErr error
	for attempt := 1; attempt <= GenerateAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, GenerateTimeout)
		raw, lastErr = g.llm.Complete(callCtx, GeneratePrompt(sig, day.Format("2006-01-02")))
		cancel()
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		incSkip(SkipLLMTimeout)
		incGenerated(false)
		return nil
	}
	result, ok := parseGenerateJSON(raw)
	if !ok {
		incParseError()
		incSkip(SkipParseError)
		incGenerated(false)
		return nil
	}
	now := g.now().UTC()
	validUntil := day.Add(24 * time.Hour)
	cards := make([]Card, 0, CardsPerDay)
	for _, item := range result.Cards {
		if len(cards) == CardsPerDay {
			break
		}
		title := strings.TrimSpace(item.Title)
		en := strings.TrimSpace(item.PromptEN)
		if title == "" || en == "" {
			continue
		}
		cards = append(cards, Card{
			ID:         g.newID(),
			UserID:     userID,
			ForDate:    day,
			Title:      title,
			PromptEN:   en,
			PromptZH:   strings.TrimSpace(item.PromptZH),
			CardType:   normalizeCardType(item.CardType, len(cards)),
			SeedTags:   item.SeedTags,
			ValidUntil: validUntil,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	if len(cards) != CardsPerDay {
		incParseError()
		incSkip(SkipParseError)
		incGenerated(false)
		return nil
	}
	if err := g.store.InsertCards(ctx, cards); err != nil {
		incGenerated(false)
		return err
	}
	incGenerated(true)
	return nil
}

func parseGenerateJSON(raw string) (GenerateResult, bool) {
	trimmed := strings.TrimSpace(raw)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var out GenerateResult
	if json.Unmarshal([]byte(trimmed), &out) != nil {
		return GenerateResult{}, false
	}
	return out, true
}

func normalizeCardType(tag string, index int) string {
	switch strings.TrimSpace(tag) {
	case CardTypeWarmup, CardTypePractice, CardTypeStretch:
		return tag
	}
	fallback := []string{CardTypeWarmup, CardTypePractice, CardTypeStretch}
	if index >= 0 && index < len(fallback) {
		return fallback[index]
	}
	return CardTypePractice
}
