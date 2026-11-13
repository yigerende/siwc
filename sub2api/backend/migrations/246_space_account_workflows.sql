-- Administrative display only. No scheduler/account state or usage changes.
CREATE TABLE IF NOT EXISTS space_account_workflows (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 source_id TEXT NOT NULL,
 revision BIGINT NOT NULL CHECK (revision > 0),
 payload JSONB NOT NULL,
 completed_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS space_account_workflows_completed ON space_account_workflows(completed_at) WHERE completed_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS space_workflow_cleanup_runs (
 id INTEGER PRIMARY KEY CHECK(id=1), last_run TIMESTAMPTZ,
 deleted_count BIGINT NOT NULL DEFAULT 0, total_deleted BIGINT NOT NULL DEFAULT 0
);
INSERT INTO space_workflow_cleanup_runs(id) VALUES(1) ON CONFLICT DO NOTHING;
