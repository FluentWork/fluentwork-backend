package topic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MySQLStore persists topic cards in MySQL 8.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore wraps an opened handle.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

const cardColumns = `id, user_id, for_date, title, prompt_en, prompt_zh, card_type, seed_tags, valid_until, checked_in_at, deleted_at, created_at, updated_at`

// Ping verifies connectivity.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ListTodayCards returns non-deleted cards for the UTC date.
func (s *MySQLStore) ListTodayCards(ctx context.Context, userID string, forDate time.Time) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+cardColumns+` FROM topic_cards
		WHERE user_id = ? AND for_date = ? AND deleted_at IS NULL
		ORDER BY created_at, id
	`, userID, utcDate(forDate).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Card
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, rows.Err()
}

// HasCardsForDate is true if any card (including A4-deleted) exists for the date.
func (s *MySQLStore) HasCardsForDate(ctx context.Context, userID string, forDate time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM topic_cards WHERE user_id = ? AND for_date = ?
	`, userID, utcDate(forDate).Format("2006-01-02")).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetCard loads by id.
func (s *MySQLStore) GetCard(ctx context.Context, cardID string) (Card, error) {
	return scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardColumns+` FROM topic_cards WHERE id = ?`, cardID))
}

// InsertCards stores generated cards.
func (s *MySQLStore) InsertCards(ctx context.Context, cards []Card) error {
	for _, card := range cards {
		tags, err := json.Marshal(card.SeedTags)
		if err != nil {
			return err
		}
		if card.SeedTags == nil {
			tags = []byte("[]")
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO topic_cards (
				id, user_id, for_date, title, prompt_en, prompt_zh, card_type, seed_tags,
				valid_until, checked_in_at, deleted_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, card.ID, card.UserID, utcDate(card.ForDate).Format("2006-01-02"), card.Title, card.PromptEN, card.PromptZH, card.CardType, tags,
			card.ValidUntil.UTC(), nullTime(card.CheckedInAt), nullTime(card.DeletedAt), card.CreatedAt.UTC(), card.UpdatedAt.UTC())
		if err != nil {
			return err
		}
	}
	return nil
}

// MarkCheckedIn sets checked_in_at once.
func (s *MySQLStore) MarkCheckedIn(ctx context.Context, cardID string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE topic_cards SET checked_in_at = ?, updated_at = ?
		WHERE id = ? AND checked_in_at IS NULL AND deleted_at IS NULL
	`, at.UTC(), at.UTC(), cardID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	_, getErr := s.GetCard(ctx, cardID)
	if getErr != nil {
		return getErr
	}
	return ErrConflict
}

// InsertCheckin records a checkin row.
func (s *MySQLStore) InsertCheckin(ctx context.Context, row Checkin) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO topic_checkins (id, card_id, user_id, reflection, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, row.ID, row.CardID, row.UserID, row.Reflection, row.CreatedAt.UTC())
	return err
}

// GetStreak returns the user streak or a zero row.
func (s *MySQLStore) GetStreak(ctx context.Context, userID string) (Streak, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, current_streak, longest_streak, last_checkin_date, deleted_at, updated_at
		FROM topic_streaks WHERE user_id = ?
	`, userID)
	var st Streak
	var last sql.NullTime
	var deleted sql.NullTime
	err := row.Scan(&st.UserID, &st.CurrentStreak, &st.LongestStreak, &last, &deleted, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Streak{UserID: userID}, nil
	}
	if err != nil {
		return Streak{}, err
	}
	if last.Valid {
		t := utcDate(last.Time)
		st.LastCheckinDate = &t
	}
	if deleted.Valid {
		t := deleted.Time.UTC()
		st.DeletedAt = &t
	}
	return st, nil
}

// UpsertStreak writes the streak aggregate.
func (s *MySQLStore) UpsertStreak(ctx context.Context, streak Streak) error {
	var last any
	if streak.LastCheckinDate != nil {
		last = utcDate(*streak.LastCheckinDate).Format("2006-01-02")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO topic_streaks (user_id, current_streak, longest_streak, last_checkin_date, deleted_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			current_streak = VALUES(current_streak),
			longest_streak = VALUES(longest_streak),
			last_checkin_date = VALUES(last_checkin_date),
			deleted_at = VALUES(deleted_at),
			updated_at = VALUES(updated_at)
	`, streak.UserID, streak.CurrentStreak, streak.LongestStreak, last, nullTime(streak.DeletedAt), streak.UpdatedAt.UTC())
	return err
}

// SoftDeleteAllForUser sets deleted_at.
func (s *MySQLStore) SoftDeleteAllForUser(ctx context.Context, userID string, at time.Time) (int, error) {
	n := 0
	res, err := s.db.ExecContext(ctx, `
		UPDATE topic_cards SET deleted_at = ?, updated_at = ?
		WHERE user_id = ? AND deleted_at IS NULL
	`, at.UTC(), at.UTC(), userID)
	if err != nil {
		return 0, err
	}
	c, _ := res.RowsAffected()
	n += int(c)
	res, err = s.db.ExecContext(ctx, `
		UPDATE topic_checkins SET deleted_at = ? WHERE user_id = ? AND deleted_at IS NULL
	`, at.UTC(), userID)
	if err != nil {
		return n, err
	}
	c, _ = res.RowsAffected()
	n += int(c)
	res, err = s.db.ExecContext(ctx, `
		UPDATE topic_streaks SET deleted_at = ?, updated_at = ?
		WHERE user_id = ? AND deleted_at IS NULL
	`, at.UTC(), at.UTC(), userID)
	if err != nil {
		return n, err
	}
	c, _ = res.RowsAffected()
	n += int(c)
	return n, nil
}

// RestoreDeletedForUser clears deleted_at.
func (s *MySQLStore) RestoreDeletedForUser(ctx context.Context, userID string) (int, error) {
	n := 0
	res, err := s.db.ExecContext(ctx, `
		UPDATE topic_cards SET deleted_at = NULL WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	c, _ := res.RowsAffected()
	n += int(c)
	res, err = s.db.ExecContext(ctx, `
		UPDATE topic_checkins SET deleted_at = NULL WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return n, err
	}
	c, _ = res.RowsAffected()
	n += int(c)
	res, err = s.db.ExecContext(ctx, `
		UPDATE topic_streaks SET deleted_at = NULL WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return n, err
	}
	c, _ = res.RowsAffected()
	n += int(c)
	return n, nil
}

type cardScanner interface {
	Scan(dest ...any) error
}

func scanCard(row cardScanner) (Card, error) {
	var card Card
	var tags []byte
	var checked sql.NullTime
	var deleted sql.NullTime
	var forDate time.Time
	err := row.Scan(&card.ID, &card.UserID, &forDate, &card.Title, &card.PromptEN, &card.PromptZH, &card.CardType, &tags, &card.ValidUntil, &checked, &deleted, &card.CreatedAt, &card.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, ErrNotFound
	}
	if err != nil {
		return Card{}, err
	}
	card.ForDate = utcDate(forDate)
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &card.SeedTags)
	}
	if card.SeedTags == nil {
		card.SeedTags = []string{}
	}
	if checked.Valid {
		t := checked.Time.UTC()
		card.CheckedInAt = &t
	}
	if deleted.Valid {
		t := deleted.Time.UTC()
		card.DeletedAt = &t
	}
	return card, nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
