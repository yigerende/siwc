package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// A separate table prevents quota/token/admin updates from overwriting this
// marker. Lock the identity for the duration of the atomic versioned upsert.
const saveSpaceWorkflowSQL = `WITH target AS (
 SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL
 AND platform='openai' AND type='oauth'
 AND (lower(trim(credentials->>'auth_mode'))='siwc' OR lower(trim(credentials->>'openai_auth_mode'))='siwc')
 AND lower(trim(credentials->>'email'))=$2 FOR SHARE
)
INSERT INTO space_account_workflows(account_id,source_id,revision,payload,completed_at)
SELECT id,$3,$4,$5::jsonb,CASE WHEN ($5::jsonb->>'completed')::boolean THEN NOW() END FROM target
ON CONFLICT(account_id) DO UPDATE SET revision=excluded.revision,payload=excluded.payload,updated_at=NOW(),
completed_at=CASE WHEN NOT (excluded.payload->>'completed')::boolean THEN NULL
 WHEN space_account_workflows.payload->>'flow_id'=excluded.payload->>'flow_id'
 AND space_account_workflows.completed_at IS NOT NULL THEN space_account_workflows.completed_at
 ELSE NOW() END
WHERE space_account_workflows.source_id=excluded.source_id AND space_account_workflows.revision<excluded.revision
RETURNING revision`

func (r *accountRepository) SaveSpaceWorkflow(ctx context.Context, id int64, v service.SpaceWorkflow) (*service.SpaceWorkflowReceipt, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, saveSpaceWorkflowSQL, id, v.Email, v.SourceID, v.Revision, string(raw))
	if err != nil {
		return nil, err
	}
	var revision int64
	if rows.Next() {
		err = rows.Scan(&revision)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if revision > 0 {
		return &service.SpaceWorkflowReceipt{Revision: revision, Applied: true}, nil
	}
	// Same-version retries acknowledge the original result. Older requests can
	// never replace a newer completion/reset; cross-instance writes conflict.
	current, err := r.SpaceWorkflows(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	old := current[id]
	if old == nil || old.SourceID != v.SourceID || old.Revision < v.Revision {
		return nil, service.ErrSpaceWorkflowConflict
	}
	if old.Revision == v.Revision {
		previous, _ := json.Marshal(old)
		if string(previous) != string(raw) {
			return nil, service.ErrSpaceWorkflowConflict
		}
	}
	return &service.SpaceWorkflowReceipt{Revision: old.Revision, Applied: false}, nil
}

func (r *accountRepository) SpaceWorkflows(ctx context.Context, ids []int64) (map[int64]*service.SpaceWorkflow, error) {
	out := map[int64]*service.SpaceWorkflow{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 1000 {
		return nil, fmt.Errorf("too many Space workflow accounts")
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT w.account_id,w.payload FROM space_account_workflows w JOIN accounts a ON a.id=w.account_id WHERE w.account_id=ANY($1) AND a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND (lower(trim(a.credentials->>'auth_mode'))='siwc' OR lower(trim(a.credentials->>'openai_auth_mode'))='siwc') AND lower(trim(a.credentials->>'email'))=w.payload->>'email'`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw []byte
		var v service.SpaceWorkflow
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out[id] = &v
	}
	return out, rows.Err()
}
