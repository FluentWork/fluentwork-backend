package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

const dailyReadColumns = `id, user_id, gen_date, status, title, body, audio_url, used_block_ids, source_refs, read_score, generator, created_at, updated_at`

// MySQLStore persists daily reads in MySQL.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore constructs a MySQL-backed content store.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// Ping implements Store.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// GetByUserDate implements Store.
func (s *MySQLStore) GetByUserDate(ctx context.Context, userID string, genDate time.Time) (DailyRead, error) {
	return scanRead(s.db.QueryRowContext(ctx, `
                SELECT `+dailyReadColumns+`
                FROM daily_reads
                WHERE user_id = ? AND gen_date = ?
        `, userID, normalizeDateUTC(genDate)))
}

// CreatePending implements Store.
func (s *MySQLStore) CreatePending(ctx context.Context, read DailyRead) (DailyRead, error) {
	_, err := s.db.ExecContext(ctx, `
                INSERT INTO daily_reads (
                        id, user_id, gen_date, status, title, body, audio_url,
                        used_block_ids, source_refs, read_score, generator, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        `,
		read.ID,
		read.UserID,
		normalizeDateUTC(read.GenDate),
		read.Status,
		read.Title,
		read.Body,
		read.AudioURL,
		nullJSON(read.UsedBlockIDs),
		nullJSON(read.SourceRefs),
		read.ReadScore,
		read.Generator,
		read.CreatedAt,
		read.UpdatedAt,
	)
	if err != nil {
		if isDuplicateKey(err) {
			return s.GetByUserDate(ctx, read.UserID, read.GenDate)
		}
		return DailyRead{}, err
	}
	return read, nil
}

// MarkReady implements Store.
func (s *MySQLStore) MarkReady(ctx context.Context, read DailyRead) (DailyRead, error) {
	res, err := s.db.ExecContext(ctx, `
                UPDATE daily_reads
                SET status = ?, title = ?, body = ?, audio_url = ?, used_block_ids = ?, source_refs = ?, generator = ?, updated_at = ?
                WHERE id = ? AND user_id = ?
        `,
		StatusReady,
		read.Title,
		read.Body,
		read.AudioURL,
		nullJSON(read.UsedBlockIDs),
		nullJSON(read.SourceRefs),
		read.Generator,
		read.UpdatedAt,
		read.ID,
		read.UserID,
	)
	if err != nil {
		return DailyRead{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return DailyRead{}, err
	}
	if affected == 0 {
		return DailyRead{}, ErrNotFound
	}
	return s.GetBlock(ctx, read.UserID, read.ID)
}

// MarkFailed implements Store.
func (s *MySQLStore) MarkFailed(ctx context.Context, id string, updatedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
                UPDATE daily_reads SET status = ?, updated_at = ? WHERE id = ?
        `, StatusFailed, updatedAt.UTC(), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetBlock implements Store.
func (s *MySQLStore) GetBlock(ctx context.Context, userID, readID string) (DailyRead, error) {
	return scanRead(s.db.QueryRowContext(ctx, `
                SELECT `+dailyReadColumns+`
                FROM daily_reads
                WHERE id = ? AND user_id = ?
        `, readID, userID))
}

// GetLatestReadyBefore implements Store.
func (s *MySQLStore) GetLatestReadyBefore(ctx context.Context, userID string, beforeDate time.Time) (DailyRead, error) {
	return scanRead(s.db.QueryRowContext(ctx, `
                SELECT `+dailyReadColumns+`
                FROM daily_reads
                WHERE user_id = ? AND status = ? AND gen_date < ?
                ORDER BY gen_date DESC
                LIMIT 1
        `, userID, StatusReady, normalizeDateUTC(beforeDate)))
}

// ReassignUser implements Store.
func (s *MySQLStore) ReassignUser(ctx context.Context, fromUserID, toUserID string) error {
	_, err := s.db.ExecContext(ctx, `
                UPDATE daily_reads SET user_id = ?, updated_at = ? WHERE user_id = ?
        `, toUserID, time.Now().UTC(), fromUserID)
	return err
}

func scanRead(row *sql.Row) (DailyRead, error) {
	var read DailyRead
	var audioURL sql.NullString
	var usedBlockIDs, sourceRefs []byte
	var readScore sql.NullFloat64
	var generator sql.NullString
	err := row.Scan(
		&read.ID,
		&read.UserID,
		&read.GenDate,
		&read.Status,
		&read.Title,
		&read.Body,
		&audioURL,
		&usedBlockIDs,
		&sourceRefs,
		&readScore,
		&generator,
		&read.CreatedAt,
		&read.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return DailyRead{}, ErrNotFound
		}
		return DailyRead{}, err
	}
	if audioURL.Valid {
		read.AudioURL = &audioURL.String
	}
	read.UsedBlockIDs = json.RawMessage(usedBlockIDs)
	read.SourceRefs = json.RawMessage(sourceRefs)
	if readScore.Valid {
		value := readScore.Float64
		read.ReadScore = &value
	}
	if generator.Valid {
		read.Generator = generator.String
	}
	read.GenDate = normalizeDateUTC(read.GenDate)
	return read, nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func isDuplicateKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}
