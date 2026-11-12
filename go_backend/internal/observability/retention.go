package observability

import (
	"chatgpt2api/go_backend/internal/callrecord"
	"context"
	"database/sql"
	"time"
)

type LogRetentionResult struct {
	Removed          int64 `json:"removed"`
	Kept             int64 `json:"kept"`
	RemovedSizeBytes int64 `json:"removed_size_bytes"`
	RetentionHours   int   `json:"retention_hours"`
	DryRun           bool  `json:"dry_run"`
}

func (s *Service) CleanupLogs(parent context.Context, hours int, dryRun bool) (result LogRetentionResult, err error) {
	result = LogRetentionResult{RetentionHours: hours, DryRun: dryRun}
	defer func() {
		if err != nil && !dryRun {
			result.Kept += result.Removed
			result.Removed = 0
			result.RemovedSizeBytes = 0
		}
	}()
	if hours < 1 || hours > 876000 {
		return result, callrecord.ErrInput
	}
	if s.db == nil {
		return result, callrecord.ErrStorage
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: dryRun})
	if err != nil {
		return result, canceled(ctx, err)
	}
	defer tx.Rollback()
	if !dryRun {
		if _, err = prepareProjection(ctx, tx, time.Now(), ""); err != nil {
			return result, canceled(ctx, err)
		}
	}
	before := time.Now().Add(-time.Duration(hours) * time.Hour).In(beijing).Format("2006-01-02 15:04:05")
	rows, err := tx.QueryContext(ctx, "SELECT id,octet_length(payload::text) FROM call_records WHERE event_time<$1 AND outcome NOT IN ('running','queued') ORDER BY sequence LIMIT 1000", before)
	if err != nil {
		return result, canceled(ctx, err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		var size int64
		if err = rows.Scan(&id, &size); err != nil {
			rows.Close()
			return result, canceled(ctx, err)
		}
		ids = append(ids, id)
		result.RemovedSizeBytes += size
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, canceled(ctx, err)
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM call_records").Scan(&result.Kept); err != nil {
		return result, canceled(ctx, err)
	}
	result.Removed = int64(len(ids))
	result.Kept -= result.Removed
	if !dryRun && len(ids) > 0 {
		deleted, err := tx.ExecContext(ctx, "DELETE FROM call_records WHERE id=ANY($1) AND outcome NOT IN ('running','queued')", ids)
		if err != nil {
			return result, canceled(ctx, err)
		}
		count, err := deleted.RowsAffected()
		if err != nil || count != result.Removed {
			return result, callrecord.ErrStorage
		}
	}
	if err = tx.Commit(); err != nil {
		return result, canceled(ctx, err)
	}
	return result, nil
}
