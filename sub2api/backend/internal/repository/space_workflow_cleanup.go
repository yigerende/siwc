package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// The final predicate and deletion share row locks with marker updates. The
// soft-delete/group/test-plan cleanup and scheduler outbox commit atomically.
// Historical usage, billing and Space orders are never deleted.
func (r *accountRepository) CleanupSpaceWorkflows(ctx context.Context, cfg service.SpaceWorkflowCleanupSettings, now time.Time) (int, error) {
	if e := cfg.Validate(); e != nil {
		return 0, e
	}
	if !cfg.Enabled {
		return 0, nil
	}
	db, ok := r.sql.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return 0, fmt.Errorf("Space cleanup requires transactional SQL")
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var locked bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(73629013511)`).Scan(&locked); e != nil || !locked {
		return 0, e
	}
	// A concurrent disable/config change either precedes this check, or waits
	// for this bounded batch. Never clean based on stale process settings.
	var raw string
	if e = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeySpaceWorkflowCleanup).Scan(&raw); e == sql.ErrNoRows {
		return 0, nil
	} else if e != nil {
		return 0, e
	}
	var current service.SpaceWorkflowCleanupSettings
	if e = json.Unmarshal([]byte(raw), &current); e != nil {
		return 0, e
	}
	if current != cfg {
		return 0, nil
	}
	var last sql.NullTime
	if e = tx.QueryRowContext(ctx, `SELECT last_run FROM space_workflow_cleanup_runs WHERE id=1 FOR UPDATE`).Scan(&last); e != nil {
		return 0, e
	}
	if last.Valid && now.Before(last.Time.Add(time.Duration(cfg.IntervalMinutes)*time.Minute)) {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(cfg.RetainMinutes) * time.Minute)
	rows, e := tx.QueryContext(ctx, `SELECT a.id FROM space_account_workflows w JOIN accounts a ON a.id=w.account_id
 WHERE w.completed_at <= $1 AND w.payload->>'completed'='true' AND a.deleted_at IS NULL
 AND a.platform='openai' AND a.type='oauth'
 AND (lower(trim(a.credentials->>'auth_mode'))='siwc' OR lower(trim(a.credentials->>'openai_auth_mode'))='siwc')
 AND lower(trim(a.credentials->>'email'))=w.payload->>'email'
 ORDER BY w.completed_at,a.id LIMIT 100 FOR UPDATE OF a,w SKIP LOCKED`, cutoff)
	if e != nil {
		return 0, e
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := len(ids)
	if count > 0 {
		// Preserve the normal administrative deletion's Spark-shadow cascade.
		rows, e = tx.QueryContext(ctx, `SELECT id FROM accounts WHERE parent_account_id=ANY($1) AND quota_dimension='spark' AND deleted_at IS NULL FOR UPDATE`, pq.Array(ids))
		if e != nil {
			return 0, e
		}
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				break
			}
			ids = append(ids, id)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return 0, e
		}
		// Outbox survives a crash between commit and cache invalidation.
		_, e = tx.ExecContext(ctx, `INSERT INTO scheduler_outbox(event_type,account_id,payload)
   SELECT 'account_changed',a.id,jsonb_build_object('group_ids',COALESCE(jsonb_agg(g.group_id) FILTER(WHERE g.group_id IS NOT NULL),'[]'::jsonb))
   FROM accounts a LEFT JOIN account_groups g ON g.account_id=a.id WHERE a.id=ANY($1) GROUP BY a.id`, pq.Array(ids))
		if e != nil {
			return 0, e
		}
		for _, query := range []string{`DELETE FROM account_groups WHERE account_id=ANY($1)`, `DELETE FROM scheduled_test_plans WHERE account_id=ANY($1)`, `UPDATE accounts SET deleted_at=NOW(),updated_at=NOW(),schedulable=false WHERE id=ANY($1) AND deleted_at IS NULL`} {
			if _, e = tx.ExecContext(ctx, query, pq.Array(ids)); e != nil {
				return 0, e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE space_workflow_cleanup_runs SET last_run=$1,deleted_count=$2,total_deleted=total_deleted+$2 WHERE id=1`, now, count); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	for _, id := range ids {
		r.deleteSchedulerAccountSnapshot(ctx, id)
	}
	return count, nil
}
