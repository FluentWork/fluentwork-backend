package materials

import (
	"context"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func newLimitService() *Service {
	return NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)
}

func TestCreate_AcceptsTwoThousandHanzi(t *testing.T) {
	svc := newLimitService()
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("字", 2000)}); err != nil {
		t.Fatalf("2000 hanzi rejected: %v", err)
	}
}

func TestCreate_AcceptsContentAtTheCharacterCap(t *testing.T) {
	svc := newLimitService()
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("字", MaxContentLen)}); err != nil {
		t.Fatalf("content at the cap rejected: %v", err)
	}
}

func TestCreate_RejectsContentPastTheCharacterCap(t *testing.T) {
	svc := newLimitService()
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("字", MaxContentLen+1)}); err == nil {
		t.Fatal("content past the cap accepted")
	}
}
