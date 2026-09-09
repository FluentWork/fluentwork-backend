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

const blockColumns = `id, user_id, intent_zh, expression_en, anchor_user_said, scene_tag, function_tag, state, success_streak, next_due_at, ease_factor, real_use_count, total_uses, last_used_at, is_favorite, pinned_at, source_session_id, deleted_at, created_at, updated_at`

// ListBlocks implements Store.
func (s *MySQLStore) ListBlocks(ctx context.Context, filter ListFilter) ([]PhraseBlock, error) {
	args := []any{filter.UserID}
	query := `
                SELECT ` + blockColumns + `
                FROM phrase_blocks
                WHERE user_id = ?
        `
	if filter.Incremental {
		if filter.UpdatedAfter != nil {
			query += ` AND updated_at > ?`
			args = append(args, filter.UpdatedAfter.UTC())
		}
		if filter.After != nil {
			query += ` AND (
                                updated_at > ?
                                OR (updated_at = ? AND id > ?)
                        )`
			args = append(args, filter.After.UpdatedAt.UTC(), filter.After.UpdatedAt.UTC(), filter.After.ID)
		}
		query += ` ORDER BY updated_at ASC, id ASC LIMIT ?`
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

	query += ` AND deleted_at IS NULL`
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
	if filter.PinnedOnly {
		query += ` AND pinned_at IS NOT NULL`
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		like := "%" + kw + "%"
		query += ` AND (expression_en LIKE ? OR intent_zh LIKE ? OR anchor_user_said LIKE ?)`
		args = append(args, like, like, like)
	}
	if filter.After != nil {
		pin := pinRank(filter.After.IsPinned)
		fav := favRank(filter.After.IsFavorite)
		query += ` AND (
                        (CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END) > ?
                        OR ((CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END) = ? AND (CASE WHEN is_favorite = 1 THEN 0 ELSE 1 END) > ?)
                        OR ((CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END) = ? AND (CASE WHEN is_favorite = 1 THEN 0 ELSE 1 END) = ? AND updated_at < ?)
                        OR ((CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END) = ? AND (CASE WHEN is_favorite = 1 THEN 0 ELSE 1 END) = ? AND updated_at = ? AND id < ?)
                )`
		args = append(args, pin, pin, fav, pin, fav, filter.After.UpdatedAt.UTC(), pin, fav, filter.After.UpdatedAt.UTC(), filter.After.ID)
	}
	query += ` ORDER BY CASE WHEN pinned_at IS NOT NULL THEN 0 ELSE 1 END ASC, CASE WHEN is_favorite = 1 THEN 0 ELSE 1 END ASC, updated_at DESC, id DESC LIMIT ?`
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

// PeekBlock implements Store.
func (s *MySQLStore) PeekBlock(ctx context.Context, blockID string) (PhraseBlock, error) {
	return scanBlock(s.db.QueryRowContext(ctx, `
                SELECT `+blockColumns+`
                FROM phrase_blocks
                WHERE id = ?
        `, blockID))
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

// RecordHits implements Store. One transaction UPSERTs the ledger and
// increments total_uses only when MySQL reports a fresh insert (RowsAffected==1).
func (s *MySQLStore) RecordHits(ctx context.Context, userID, sessionID, turnID string, hits []Hit) (int, error) {
	if len(hits) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	recorded := 0
	for _, hit := range hits {
		res, err := tx.ExecContext(ctx, `
                        INSERT INTO phrase_block_uses (user_id, session_id, turn_id, block_id, used_at_ms)
                        VALUES (?, ?, ?, ?, ?)
                        ON DUPLICATE KEY UPDATE used_at_ms = VALUES(used_at_ms)
                `, userID, sessionID, turnID, hit.BlockID, hit.DetectedAtMs)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		recorded++
		if n != 1 {
			continue
		}
		usedAt := time.UnixMilli(hit.DetectedAtMs).UTC()
		if _, err := tx.ExecContext(ctx, `
                        UPDATE phrase_blocks
                        SET total_uses = total_uses + 1, last_used_at = ?
                        WHERE id = ? AND user_id = ? AND deleted_at IS NULL
                `, usedAt, hit.BlockID, userID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recorded, nil
}

// ListSessionHits implements Store. Soft-deleted blocks are omitted.
func (s *MySQLStore) ListSessionHits(ctx context.Context, sessionID string) ([]RecentHit, error) {
	rows, err := s.db.QueryContext(ctx, `
                SELECT pbu.block_id, pbu.turn_id, pbu.used_at_ms, pb.intent_zh, pb.expression_en
                FROM phrase_block_uses pbu
                INNER JOIN phrase_blocks pb ON pb.id = pbu.block_id
                WHERE pbu.session_id = ?
                  AND pb.deleted_at IS NULL
                ORDER BY pbu.used_at_ms DESC, pbu.block_id ASC
        `, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RecentHit, 0)
	for rows.Next() {
		var hit RecentHit
		if err := rows.Scan(&hit.BlockID, &hit.TurnID, &hit.UsedAtMs, &hit.IntentZH, &hit.ChunkEN); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBlock(row scanner) (PhraseBlock, error) {
	var (
		block         PhraseBlock
		lastUsedAt    sql.NullTime
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
		&block.TotalUses,
		&lastUsedAt,
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
	block.LastUsedAt = nullableTimePtr(lastUsedAt)
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
