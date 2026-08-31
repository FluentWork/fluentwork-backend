package content

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const maxBlocksInDailyRead = 5

// SourceBlock is the minimal corpus input for daily read generation.
type SourceBlock struct {
	ID             string
	IntentZH       string
	ExpressionEN   string
	AnchorUserSaid string
	SceneTag       string
	FunctionTag    string
}

// BlockSource lists recent phrase blocks for one user.
type BlockSource interface {
	ListRecentBlocks(ctx context.Context, userID string, limit int) ([]SourceBlock, error)
}

// GeneratedDailyRead is the normalized output of one generation attempt.
type GeneratedDailyRead struct {
	Title        string
	Body         string
	Generator    string
	UsedBlockIDs json.RawMessage
	SourceRefs   json.RawMessage
}

// GenerateDailyRead builds today's article from corpus blocks or preset fallback.
func GenerateDailyRead(blocks []SourceBlock) GeneratedDailyRead {
	if len(blocks) == 0 {
		return presetDailyRead()
	}
	used := blocks
	if len(used) > maxBlocksInDailyRead {
		used = used[:maxBlocksInDailyRead]
	}
	ids := make([]string, 0, len(used))
	lines := make([]string, 0, len(used)+2)
	lines = append(lines, "Today's read draws from phrases you recently saved in your corpus.")
	for i, block := range used {
		ids = append(ids, block.ID)
		lines = append(lines, fmt.Sprintf("%d. %s — %s", i+1, strings.TrimSpace(block.ExpressionEN), strings.TrimSpace(block.IntentZH)))
	}
	lines = append(lines, "Try reading each line aloud and swapping one phrase into your next standup.")
	usedJSON, _ := json.Marshal(ids)
	sourceJSON, _ := json.Marshal(map[string]any{
		"kind":   "corpus_blocks",
		"count":  len(used),
		"blocks": used,
	})
	return GeneratedDailyRead{
		Title:        "Daily Read: Your Saved Phrases",
		Body:         strings.Join(lines, "\n\n"),
		Generator:    GeneratorCorpusStub,
		UsedBlockIDs: usedJSON,
		SourceRefs:   sourceJSON,
	}
}

func presetDailyRead() GeneratedDailyRead {
	sourceJSON, _ := json.Marshal(map[string]any{
		"kind": "preset",
		"id":   "daily-read-preset-v1",
	})
	return GeneratedDailyRead{
		Title: "Daily Read: Start With One Useful Line",
		Body: strings.TrimSpace(`You do not need a long article to keep momentum.

Pick one useful English line you want to sound natural saying today. Read it aloud twice, then say it once without looking.

When you finish a practice session, save one refined phrase into your corpus so tomorrow's read can be more personal.`),
		Generator:  GeneratorPreset,
		SourceRefs: sourceJSON,
	}
}
