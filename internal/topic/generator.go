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
	// minBlocks is H1's threshold (PRD §7.8): below it there is nothing to
	// ground a topic on, so the user gets no cards rather than generic ones.
	minBlocks int
}

// NewGenerator constructs the daily card writer.
func NewGenerator(store Store, llm Completer, sig SignalSource) *Generator {
	return &Generator{
		store:     store,
		llm:       llm,
		sig:       sig,
		now:       time.Now,
		newID:     uuid.NewString,
		minBlocks: DefaultMinBlocks,
	}
}

// SetMinBlocks overrides the H1 threshold (服务端可配; 0 disables the gate).
func (g *Generator) SetMinBlocks(n int) {
	if g == nil || n < 0 {
		return
	}
	g.minBlocks = n
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
	recentTitles := g.recentTitles(ctx, userID, day)

	sig := Signals{Level: "intermediate", Empty: true, SceneCounts: map[string]int{}, FunctionCounts: map[string]int{}}
	if g.sig != nil {
		got, snapErr := g.sig.Snapshot(ctx, userID, g.now())
		if snapErr == nil {
			sig = got
		}
	}
	// H1's gate: no corpus, no topics. Checked before the model call, so a user
	// below the threshold costs nothing.
	if len(sig.Blocks) < g.minBlocks {
		incSkip(SkipBelowThreshold)
		incGenerated(false)
		return nil
	}
	var raw string
	var lastErr error
	for attempt := 1; attempt <= GenerateAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, GenerateTimeout)
		raw, lastErr = g.llm.Complete(callCtx, GeneratePrompt(sig, day.Format("2006-01-02"), recentTitles))
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
	parsed := make([]Card, 0, CardsPerDay)
	for _, item := range result.Cards {
		if len(parsed) == CardsPerDay {
			break
		}
		title := strings.TrimSpace(item.Title)
		en := strings.TrimSpace(item.PromptEN)
		if title == "" || en == "" {
			continue
		}
		parsed = append(parsed, Card{
			ID:         g.newID(),
			UserID:     userID,
			ForDate:    day,
			Title:      title,
			PromptEN:   en,
			PromptZH:   strings.TrimSpace(item.PromptZH),
			CardType:   normalizeCardType(item.CardType, len(parsed)),
			SeedTags:   item.SeedTags,
			ValidUntil: validUntil,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	if len(parsed) != CardsPerDay {
		incParseError()
		incSkip(SkipParseError)
		incGenerated(false)
		return nil
	}
	// H2: keep only what can be traced to this learner's own corpus. A card that
	// fails is dropped rather than repaired — a generic topic is the failure
	// mode the PRD names, and shipping two grounded cards beats three with one
	// invented.
	cards := make([]Card, 0, len(parsed))
	for _, card := range parsed {
		grounded, ok := groundCard(card, sig, recentTitles)
		if !ok {
			incUngrounded()
			continue
		}
		cards = append(cards, grounded)
	}
	if len(cards) == 0 {
		incSkip(SkipUngrounded)
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

// recentTitles returns the titles of the last few days' cards. The store has no
// range query, and three single-day reads cost nothing next to the model call
// this feeds.
func (g *Generator) recentTitles(ctx context.Context, userID string, day time.Time) []string {
	const lookbackDays = 3
	var titles []string
	for i := 1; i <= lookbackDays; i++ {
		prior, err := g.store.ListTodayCards(ctx, userID, utcDate(day.AddDate(0, 0, -i)))
		if err != nil {
			return titles
		}
		for _, card := range prior {
			titles = append(titles, card.Title)
		}
	}
	return titles
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
