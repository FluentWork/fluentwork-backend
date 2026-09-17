package topic

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The grounding columns are what H1/H2 deliver to the client; if the SQL stops
// carrying them, every card silently loses its block list and provenance.
func TestMySQLStore_InsertAndScanGroundingColumns(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	day := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	card := Card{
		ID: "c1", UserID: "u1", ForDate: day, Title: "Standup sync",
		PromptEN: "Share progress.", PromptZH: "同步进度", CardType: CardTypeWarmup,
		SeedTags:   []string{"standup"},
		BlockIDs:   []string{"b1", "b2"},
		SourceNote: "来自你 standup 的语料；可调用话术块 ×2",
		ValidUntil: day.Add(24 * time.Hour),
		CreatedAt:  day, UpdatedAt: day,
	}

	mock.ExpectExec(`INSERT INTO topic_cards`).
		WithArgs("c1", "u1", "2026-09-18", "Standup sync", "Share progress.", "同步进度",
			CardTypeWarmup, []byte(`["standup"]`), []byte(`["b1","b2"]`), card.SourceNote,
			card.ValidUntil, nil, nil, day, day).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.InsertCards(context.Background(), []Card{card}); err != nil {
		t.Fatalf("InsertCards: %v", err)
	}

	mock.ExpectQuery(`SELECT .* FROM topic_cards WHERE id = \?`).
		WithArgs("c1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "for_date", "title", "prompt_en", "prompt_zh", "card_type",
			"seed_tags", "block_ids", "source_note", "valid_until", "checked_in_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow("c1", "u1", day, "Standup sync", "Share progress.", "同步进度", CardTypeWarmup,
			[]byte(`["standup"]`), []byte(`["b1","b2"]`), card.SourceNote,
			card.ValidUntil, nil, nil, day, day))

	got, err := store.GetCard(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if len(got.BlockIDs) != 2 || got.BlockIDs[0] != "b1" {
		t.Fatalf("block ids = %v", got.BlockIDs)
	}
	if got.SourceNote != card.SourceNote {
		t.Fatalf("source note = %q", got.SourceNote)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// A card written before the grounding columns existed still scans.
func TestMySQLStore_ScanWithoutGroundingColumns(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	day := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT .* FROM topic_cards WHERE id = \?`).
		WithArgs("c1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "for_date", "title", "prompt_en", "prompt_zh", "card_type",
			"seed_tags", "block_ids", "source_note", "valid_until", "checked_in_at",
			"deleted_at", "created_at", "updated_at",
		}).AddRow("c1", "u1", day, "Old", "text", "", CardTypePractice,
			[]byte(`["standup"]`), nil, "", day.Add(24*time.Hour), nil, nil, day, day))

	got, err := store.GetCard(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if got.BlockIDs == nil || len(got.BlockIDs) != 0 {
		t.Fatalf("block ids = %v, want an empty list", got.BlockIDs)
	}
}

func TestMySQLStore_CountsSince(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	since := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM topic_checkins`).
		WithArgs("user-1", since).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))
	n, err := store.CountCheckinsSince(context.Background(), "user-1", since)
	if err != nil || n != 3 {
		t.Fatalf("checkins = %d err = %v", n, err)
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM topic_cards`).
		WithArgs("user-1", since).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(9))
	n, err = store.CountCardsSince(context.Background(), "user-1", since)
	if err != nil || n != 9 {
		t.Fatalf("cards = %d err = %v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
