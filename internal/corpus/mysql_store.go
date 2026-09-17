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
	// schedule is the ladder the B7 hit writeback applies. Zero value means the
	// PRD defaults (see Schedule.Normalize); OpenStore fills it from config.
	schedule Schedule
}

// NewMySQLStore constructs a MySQL-backed corpus store with the default ladder.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db, schedule: DefaultSchedule()}
}

// SetSchedule replaces the ladder used by the hit writeback (E3).
func (s *MySQLStore) SetSchedule(schedule Schedule) {
	if s == nil {
		return
	}
	s.schedule = schedule.Normalize()
}

// Ping implements Store.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

const blockColumns = `id, user_id, intent_zh, expression_en, expression_version, anchor_user_said, scene_tag, function_tag, state, success_streak, next_due_at, ease_factor, real_use_count, total_uses, last_used_at, is_favorite, pinned_at, source_session_id, deleted_at, created_at, updated_at`

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
		if block.ExpressionVersion < 1 {
			// Matches the column default, and keeps the value the caller gets
			// back in step with what the database now holds.
			block.ExpressionVersion = 1
		}
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

// ListDueBlocks implements Store.
func (s *MySQLStore) ListDueBlocks(ctx context.Context, userID string, now time.Time, states []string, limit int) ([]PhraseBlock, error) {
	if len(states) == 0 || limit <= 0 {
		return []PhraseBlock{}, nil
	}
	placeholders := strings.Repeat("?,", len(states))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{userID}
	for _, st := range states {
		args = append(args, st)
	}
	args = append(args, now.UTC(), limit)
	rows, err := s.db.QueryContext(ctx, `
                SELECT `+blockColumns+`
                FROM phrase_blocks
                WHERE user_id = ? AND deleted_at IS NULL
                  AND state IN (`+placeholders+`)
                  AND next_due_at <= ?
                ORDER BY next_due_at ASC, id ASC
                LIMIT ?
        `, args...)
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

// UpdateSchedule implements Store.
func (s *MySQLStore) UpdateSchedule(ctx context.Context, userID, blockID, state string, successStreak int, nextDueAt, updatedAt time.Time) (PhraseBlock, error) {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET state = ?, success_streak = ?, next_due_at = ?, updated_at = ?
                WHERE id = ? AND user_id = ? AND deleted_at IS NULL
        `, state, successStreak, nextDueAt.UTC(), updatedAt.UTC(), blockID, userID)
	if err != nil {
		return PhraseBlock{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return PhraseBlock{}, err
	}
	if n != 1 {
		return PhraseBlock{}, ErrNotFound
	}
	return s.GetBlock(ctx, userID, blockID)
}

// SoftDeleteAllForUser implements Store.
func (s *MySQLStore) SoftDeleteAllForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET deleted_at = ?, updated_at = ?, pinned_at = NULL, is_favorite = 0
                WHERE user_id = ? AND deleted_at IS NULL
        `, deletedAt.UTC(), deletedAt.UTC(), userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RestoreDeletedForUser implements Store.
func (s *MySQLStore) RestoreDeletedForUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
                UPDATE phrase_blocks
                SET deleted_at = NULL
                WHERE user_id = ? AND deleted_at IS NOT NULL
        `, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RecordHits implements Store. One transaction UPSERTs the ledger; on a fresh
// insert (RowsAffected==1) it also writes back the hit per PRD §5.2.3:
// real_use_count/total_uses +1, last_used_at, and 视同一次成功 — the shared
// ladder advances state/success_streak/next_due_at. Rows are locked FOR UPDATE
// in block_id order (see sortedHits) so concurrent reports cannot deadlock.
func (s *MySQLStore) RecordHits(ctx context.Context, userID, sessionID, turnID string, hits []Hit) (int, error) {
	if len(hits) == 0 {
		return 0, nil
	}
	hits = sortedHits(hits)
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
		var state string
		var streak int
		var dueAt time.Time
		err = tx.QueryRowContext(ctx, `
                        SELECT state, success_streak, next_due_at
                        FROM phrase_blocks
                        WHERE id = ? AND user_id = ? AND deleted_at IS NULL
                        FOR UPDATE
                `, hit.BlockID, userID).Scan(&state, &streak, &dueAt)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return 0, err
		}
		updated := s.schedule.ApplyJudge(PhraseBlock{State: state, SuccessStreak: streak, NextDueAt: dueAt}, true, usedAt)
		// Provenance in the same transaction: a use that is not in the ledger
		// cannot be split into L1/L2 later (86_ M9).
		if _, err := tx.ExecContext(ctx, `
                        INSERT INTO phrase_block_real_uses (id, user_id, block_id, source, ref_id, used_at, created_at)
                        VALUES (?, ?, ?, ?, ?, ?, ?)
                        ON DUPLICATE KEY UPDATE id = id
                `, hit.BlockID+"-"+turnID, userID, hit.BlockID, RealUseSourceHit, turnID, usedAt, usedAt); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
                        UPDATE phrase_blocks
                        SET total_uses = total_uses + 1,
                            real_use_count = real_use_count + 1,
                            last_used_at = ?,
                            state = ?,
                            success_streak = ?,
                            next_due_at = ?,
                            updated_at = ?
                        WHERE id = ? AND user_id = ? AND deleted_at IS NULL
                `, usedAt, updated.State, updated.SuccessStreak, updated.NextDueAt, updated.UpdatedAt, hit.BlockID, userID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recorded, nil
}

// SweepOverdue implements Store: one statement, scoped to the user.
func (s *MySQLStore) SweepOverdue(ctx context.Context, userID string, dueBefore, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE phrase_blocks
		SET next_due_at = ?, updated_at = ?
		WHERE user_id = ? AND deleted_at IS NULL AND next_due_at < ?
	`, at.UTC(), at.UTC(), userID, dueBefore.UTC())
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RecordRealUses implements Store in one transaction: ledger, counters and
// schedule move together or not at all.
func (s *MySQLStore) RecordRealUses(ctx context.Context, userID string, blockIDs []string, source, refID string, at time.Time) (int, error) {
	if len(blockIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	usedAt := at.UTC()
	credited := 0
	for _, blockID := range blockIDs {
		var state string
		var streak int
		var dueAt time.Time
		err := tx.QueryRowContext(ctx, `
			SELECT state, success_streak, next_due_at FROM phrase_blocks
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
			FOR UPDATE
		`, blockID, userID).Scan(&state, &streak, &dueAt)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO phrase_block_real_uses (id, user_id, block_id, source, ref_id, used_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE id = id
		`, refID+"-"+blockID, userID, blockID, source, refID, usedAt, usedAt)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != 1 {
			continue // already credited for this ref
		}
		updated := s.schedule.Normalize().ApplyJudge(PhraseBlock{State: state, SuccessStreak: streak, NextDueAt: dueAt}, true, usedAt)
		if _, err := tx.ExecContext(ctx, `
			UPDATE phrase_blocks
			SET real_use_count = real_use_count + 1, total_uses = total_uses + 1,
			    last_used_at = ?, state = ?, success_streak = ?, next_due_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		`, usedAt, updated.State, updated.SuccessStreak, updated.NextDueAt, usedAt, blockID, userID); err != nil {
			return 0, err
		}
		credited++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return credited, nil
}

// CountRealUsesBySource implements Store.
func (s *MySQLStore) CountRealUsesBySource(ctx context.Context, userID string, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, COUNT(*) FROM phrase_block_real_uses
		WHERE user_id = ? AND used_at >= ?
		GROUP BY source
	`, userID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, err
		}
		out[source] = count
	}
	return out, rows.Err()
}

// SaveBlockEdit implements Store.
func (s *MySQLStore) SaveBlockEdit(ctx context.Context, edit BlockEdit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO phrase_block_edits (id, user_id, block_id, version, old_expression, new_expression, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, edit.ID, edit.UserID, edit.BlockID, edit.Version, edit.OldExpression, edit.NewExpression, edit.CreatedAt.UTC()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE phrase_blocks SET expression_version = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, edit.Version, edit.CreatedAt.UTC(), edit.BlockID, edit.UserID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ResetSchedule implements Store, using this store's configured ladder.
func (s *MySQLStore) ResetSchedule(ctx context.Context, userID, blockID string, at time.Time) (PhraseBlock, error) {
	now := at.UTC()
	dueAt := now.Add(s.schedule.Normalize().TrainingInterval)
	result, err := s.db.ExecContext(ctx, `
		UPDATE phrase_blocks
		SET state = ?, success_streak = 0, next_due_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, StateNew, dueAt, now, blockID, userID)
	if err != nil {
		return PhraseBlock{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return PhraseBlock{}, err
	}
	if n != 1 {
		return PhraseBlock{}, ErrNotFound
	}
	return s.GetBlock(ctx, userID, blockID)
}

// SaveFeedback implements Store.
func (s *MySQLStore) SaveFeedback(ctx context.Context, feedback Feedback) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO phrase_block_feedback (id, user_id, block_id, reason, deleted_at, created_at)
		VALUES (?, ?, ?, ?, NULL, ?)
		ON DUPLICATE KEY UPDATE deleted_at = NULL
	`, feedback.ID, feedback.UserID, feedback.BlockID, feedback.Reason, feedback.CreatedAt.UTC())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	// MySQL reports 1 for an insert and 2 for an ON DUPLICATE KEY update that
	// changed a value; a re-tap that changes nothing reports 0.
	return n == 1, nil
}

// CountFeedback implements Store.
func (s *MySQLStore) CountFeedback(ctx context.Context, userID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT reason, COUNT(*) FROM phrase_block_feedback
		WHERE user_id = ? AND deleted_at IS NULL
		GROUP BY reason
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			return nil, err
		}
		out[reason] = count
	}
	return out, rows.Err()
}

// SoftDeleteFeedbackForUser implements Store.
func (s *MySQLStore) SoftDeleteFeedbackForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE phrase_block_feedback
		SET deleted_at = ?
		WHERE user_id = ? AND deleted_at IS NULL
	`, deletedAt.UTC(), userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RestoreFeedbackForUser implements Store.
func (s *MySQLStore) RestoreFeedbackForUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE phrase_block_feedback
		SET deleted_at = NULL
		WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
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
		&block.ExpressionVersion,
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
