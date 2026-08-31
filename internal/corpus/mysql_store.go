package corpus

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// MySQLStore persists phrase blocks in MySQL.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore constructs a MySQL-backed corpus store.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// Ping implements Store.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

const blockColumns = `id, user_id, intent_zh, expression_en, anchor_user_said, scene_tag, function_tag, state, success_streak, next_due_at, ease_factor, real_use_count, is_favorite, pinned_at, source_session_id, deleted_at, created_at, updated_at`

// ListBlocks implements Store.
func (s *MySQLStore) ListBlocks(ctx context.Context, filter ListFilter) ([]PhraseBlock, error) {
	args := []any{filter.UserID}
	query := `
                SELECT ` + blockColumns + `
                FROM phrase_blocks
                WHERE user_id = ? AND deleted_at IS NULL
        `
	if filter.SceneTag != "" {
		query += ` AND scene_tag = ?`
		args = append(args, filter.SceneTag)
	}
	if filter.FunctionTag != "" {
		query += ` AND function_tag = ?`
		args = append(args, filter.FunctionTag)
	}
	if filter.FavoriteOnly {
		query += ` AND is_favorite = 1`
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		like := "%" + kw + "%"
		query += ` AND (expression_en LIKE ? OR intent_zh LIKE ? OR anchor_user_said LIKE ?)`
		args = append(args, like, like, like)
	}
	if filter.After != nil {
		query += ` AND (
                        COALESCE(pinned_at, TIMESTAMP('1000-01-01 00:00:00.000')) < ?
                        OR (COALESCE(pinned_at, TIMESTAMP('1000-01-01 00:00:00.000')) = ? AND created_at < ?)
                        OR (COALESCE(pinned_at, TIMESTAMP('1000-01-01 00:00:00.000')) = ? AND created_at = ? AND id < ?)
                )`
		pin := pinnedValue(filter.After.PinnedAt)
		args = append(args, pin, pin, filter.After.CreatedAt, pin, filter.After.CreatedAt, filter.After.ID)
	}
	query += ` ORDER BY COALESCE(pinned_at, TIMESTAMP('1000-01-01 00:00:00.000')) DESC, created_at DESC, id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	blocks := make([]PhraseBlock, 0)
	for rows.Next() {
		block, err := scanBlock(rows)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

// GetBlock implements Store.
func (s *MySQLStore) GetBlock(ctx context.Context, userID, blockID string) (PhraseBlock, error) {
	return scanBlock(s.db.QueryRowContext(ctx, `
                SELECT `+blockColumns+`
                FROM phrase_blocks
                WHERE id = ? AND user_id = ? AND deleted_at IS NULL
        `, blockID, userID))
}

// SaveAcceptedBlocks implements Store.
func (s *MySQLStore) SaveAcceptedBlocks(ctx context.Context, blocks []PhraseBlock) ([]PhraseBlock, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	saved := make([]PhraseBlock, 0, len(blocks))
	for _, block := range blocks {
		row := tx.QueryRowContext(ctx, `
                        SELECT `+blockColumns+`
                        FROM phrase_blocks
                        WHERE user_id = ? AND source_session_id = ? AND expression_en = ? AND anchor_user_said = ? AND scene_tag = ? AND function_tag = ?
                        FOR UPDATE
                `, block.UserID, nullString(block.SourceSessionID), block.ExpressionEN, block.AnchorUserSaid, block.SceneTag, block.FunctionTag)
		existing, err := scanBlock(row)
		switch err {
		case nil:
			if existing.DeletedAt != nil {
				_, err = tx.ExecContext(ctx, `
                                        UPDATE phrase_blocks
                                        SET intent_zh = ?, expression_en = ?, anchor_user_said = ?, scene_tag = ?, function_tag = ?, source_session_id = ?, deleted_at = NULL, updated_at = ?
                                        WHERE id = ?
                                `, block.IntentZH, block.ExpressionEN, block.AnchorUserSaid, block.SceneTag, block.FunctionTag, nullString(block.SourceSessionID), block.UpdatedAt, existing.ID)
				if err != nil {
					return nil, err
				}
				existing.IntentZH = block.IntentZH
				existing.ExpressionEN = block.ExpressionEN
				existing.AnchorUserSaid = block.AnchorUserSaid
				existing.SceneTag = block.SceneTag
				existing.FunctionTag = block.FunctionTag
				existing.SourceSessionID = cloneStringPtr(block.SourceSessionID)
				existing.DeletedAt = nil
				existing.UpdatedAt = block.UpdatedAt
			}
			saved = append(saved, existing)
		case ErrNotFound:
			_, err = tx.ExecContext(ctx, `
                                INSERT INTO phrase_blocks (
                                        id, user_id, intent_zh, expression_en, anchor_user_said, scene_tag, function_tag,
                                        state, success_streak, next_due_at, ease_factor, real_use_count, is_favorite, pinned_at,
                                        source_session_id, deleted_at, created_at, updated_at
                                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                        `, block.ID, block.UserID, block.IntentZH, block.ExpressionEN, block.AnchorUserSaid, block.SceneTag, block.FunctionTag,
				block.State, block.SuccessStreak, block.NextDueAt, block.EaseFactor, block.RealUseCount, block.IsFavorite, nullTime(block.PinnedAt),
				nullString(block.SourceSessionID), nullTime(block.DeletedAt), block.CreatedAt, block.UpdatedAt)
			if err != nil {
				return nil, err
			}
			saved = append(saved, block)
		default:
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// UpdateBlock implements Store.
func (s *MySQLStore) UpdateBlock(ctx context.Context, block PhraseBlock) (PhraseBlock, error) {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET intent_zh = ?, expression_en = ?, anchor_user_said = ?, scene_tag = ?, function_tag = ?, updated_at = ?
                WHERE id = ? AND user_id = ? AND deleted_at IS NULL
        `, block.IntentZH, block.ExpressionEN, block.AnchorUserSaid, block.SceneTag, block.FunctionTag, block.UpdatedAt, block.ID, block.UserID)
	if err != nil {
		return PhraseBlock{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return PhraseBlock{}, err
	}
	if count != 1 {
		return PhraseBlock{}, ErrNotFound
	}
	return s.GetBlock(ctx, block.UserID, block.ID)
}

// SetFavorite implements Store.
func (s *MySQLStore) SetFavorite(ctx context.Context, userID, blockID string, isFavorite bool, pinnedAt *time.Time, updatedAt time.Time) (PhraseBlock, error) {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET is_favorite = ?, pinned_at = ?, updated_at = ?
                WHERE id = ? AND user_id = ? AND deleted_at IS NULL
        `, isFavorite, nullTime(pinnedAt), updatedAt, blockID, userID)
	if err != nil {
		return PhraseBlock{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return PhraseBlock{}, err
	}
	if count != 1 {
		return PhraseBlock{}, ErrNotFound
	}
	return s.GetBlock(ctx, userID, blockID)
}

// SoftDeleteBlock implements Store.
func (s *MySQLStore) SoftDeleteBlock(ctx context.Context, userID, blockID string, deletedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET deleted_at = ?, updated_at = ?, pinned_at = NULL, is_favorite = 0
                WHERE id = ? AND user_id = ? AND deleted_at IS NULL
        `, deletedAt, deletedAt, blockID, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// ReassignUser implements Store.
func (s *MySQLStore) ReassignUser(ctx context.Context, fromUserID, toUserID string) error {
	_, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET user_id = ?
                WHERE user_id = ?
        `, toUserID, fromUserID)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBlock(row scanner) (PhraseBlock, error) {
	var (
		block         PhraseBlock
		pinnedAt      sql.NullTime
		sourceSession sql.NullString
		deletedAt     sql.NullTime
	)
	err := row.Scan(
		&block.ID,
		&block.UserID,
		&block.IntentZH,
		&block.ExpressionEN,
		&block.AnchorUserSaid,
		&block.SceneTag,
		&block.FunctionTag,
		&block.State,
		&block.SuccessStreak,
		&block.NextDueAt,
		&block.EaseFactor,
		&block.RealUseCount,
		&block.IsFavorite,
		&pinnedAt,
		&sourceSession,
		&deletedAt,
		&block.CreatedAt,
		&block.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return PhraseBlock{}, ErrNotFound
		}
		return PhraseBlock{}, err
	}
	block.PinnedAt = nullableTimePtr(pinnedAt)
	block.SourceSessionID = nullableStringPtr(sourceSession)
	block.DeletedAt = nullableTimePtr(deletedAt)
	return block, nil
}

func nullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}

func nullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func nullableTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}
