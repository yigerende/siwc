package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Starts a disposable, loopback-only database. Never accepts a production DSN.
func spaceWorkflowTestDB(t *testing.T) *sql.DB {
	t.Helper()
	bin := os.Getenv("SPACE_MARKER_POSTGRES_BIN")
	if bin == "" {
		t.Skip("set SPACE_MARKER_POSTGRES_BIN for isolated PostgreSQL integration")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "pg")
	logPath := filepath.Join(dir, "pg.log")
	run := func(name string, args ...string) {
		t.Helper()
		output, err := os.Create(filepath.Join(dir, name+".output"))
		require.NoError(t, err)
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Stdout, cmd.Stderr = output, output
		err = cmd.Run()
		output.Close()
		out, _ := os.ReadFile(filepath.Join(dir, name+".output"))
		require.NoError(t, err, "%s: %s", name, out)
	}
	run("initdb", "-D", data, "-U", "postgres", "-A", "trust", "--encoding=UTF8", "--no-locale")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	run("pg_ctl", "-D", data, "-l", logPath, "-o", fmt.Sprintf("-h 127.0.0.1 -p %d", port), "-w", "start")
	t.Cleanup(func() {
		out, e := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-m", "fast", "-w", "stop").CombinedOutput()
		if e != nil {
			t.Errorf("stop disposable pg: %s %v", out, e)
		}
	})
	db, err := sql.Open("postgres", fmt.Sprintf("host=127.0.0.1 port=%d user=postgres dbname=postgres sslmode=disable", port))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE accounts(id bigint PRIMARY KEY, platform text,type text,credentials jsonb,deleted_at timestamptz,updated_at timestamptz, parent_account_id bigint,quota_dimension text,schedulable boolean DEFAULT true);
 CREATE TABLE settings(key text PRIMARY KEY,value text);
 CREATE TABLE account_groups(account_id bigint,group_id bigint);
 CREATE TABLE scheduled_test_plans(account_id bigint);
 CREATE TABLE scheduler_outbox(id bigserial PRIMARY KEY,event_type text,account_id bigint,payload jsonb);
 CREATE TABLE usage_logs(id bigint PRIMARY KEY,account_id bigint,cost numeric);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/246_space_account_workflows.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db
}

func TestSpaceWorkflowPostgresVersionsConcurrencyAndCleanup(t *testing.T) {
	db := spaceWorkflowTestDB(t)
	r := &accountRepository{sql: db}
	ctx := context.Background()
	for id := 1; id <= 6; id++ {
		_, err := db.Exec(`INSERT INTO accounts(id,platform,type,credentials) VALUES($1,'openai','oauth','{"auth_mode":"siwc","email":"a@example.com"}')`, id)
		require.NoError(t, err)
	}
	marker := service.SpaceWorkflow{SourceID: "0123456789abcdef0123456789abcdef", Revision: 1, Email: "a@example.com", FlowID: "flow", Stage: "merge", Completed: true, ChangedAt: time.Now().UTC().Truncate(time.Millisecond)}
	receipt, err := r.SaveSpaceWorkflow(ctx, 1, marker)
	require.NoError(t, err)
	require.True(t, receipt.Applied)
	receipt, err = r.SaveSpaceWorkflow(ctx, 1, marker)
	require.NoError(t, err)
	require.False(t, receipt.Applied)
	bad := marker
	bad.Completed = false
	_, err = r.SaveSpaceWorkflow(ctx, 1, bad)
	require.ErrorIs(t, err, service.ErrSpaceWorkflowConflict)
	bad = marker
	bad.SourceID = "abcdef0123456789abcdef0123456789"
	bad.Revision = 99
	_, err = r.SaveSpaceWorkflow(ctx, 1, bad)
	require.ErrorIs(t, err, service.ErrSpaceWorkflowConflict)
	var wg sync.WaitGroup
	for rev := int64(2); rev <= 32; rev++ {
		wg.Add(1)
		go func(rev int64) {
			defer wg.Done()
			v := marker
			v.Revision = rev
			v.Completed = rev%2 == 0
			_, e := r.SaveSpaceWorkflow(ctx, 1, v)
			if e != nil {
				t.Error(e)
			}
		}(rev)
	}
	wg.Wait()
	got, err := r.SpaceWorkflows(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, int64(32), got[1].Revision)
	require.True(t, got[1].Completed)
	// An identical completed flow keeps its original retention clock.
	var before, after time.Time
	require.NoError(t, db.QueryRow(`SELECT completed_at FROM space_account_workflows WHERE account_id=1`).Scan(&before))
	marker.Revision = 33
	_, err = r.SaveSpaceWorkflow(ctx, 1, marker)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT completed_at FROM space_account_workflows WHERE account_id=1`).Scan(&after))
	require.True(t, before.Equal(after))
	for _, id := range []int64{2, 3, 4, 5, 6} {
		_, err = r.SaveSpaceWorkflow(ctx, id, marker)
		require.NoError(t, err)
	}
	_, err = db.Exec(`UPDATE space_account_workflows SET completed_at=NOW()-interval '2 hours' WHERE account_id IN (1,2,3,4,5,6);
 UPDATE accounts SET credentials='{"auth_mode":"oauth","email":"a@example.com"}' WHERE id=3;
 UPDATE accounts SET credentials='{"auth_mode":"siwc","email":"other@example.com"}' WHERE id=4;
 UPDATE space_account_workflows SET completed_at=NOW() WHERE account_id=5;
 INSERT INTO accounts(id,platform,type,parent_account_id,quota_dimension)VALUES(10,'openai','oauth',1,'spark');
 INSERT INTO account_groups VALUES(1,8),(10,8);INSERT INTO scheduled_test_plans VALUES(1),(10);
 INSERT INTO usage_logs VALUES(1,1,12.5);`)
	require.NoError(t, err)
	reset := marker
	reset.Revision++
	reset.Completed = false
	_, err = r.SaveSpaceWorkflow(ctx, 2, reset)
	require.NoError(t, err)
	cfg := service.DefaultSpaceWorkflowCleanupSettings()
	cfg.Enabled = true
	raw, _ := json.Marshal(cfg)
	_, err = db.Exec(`INSERT INTO settings VALUES($1,$2)`, service.SettingKeySpaceWorkflowCleanup, string(raw))
	require.NoError(t, err)
	now := time.Now().UTC()
	// Serialize with a simultaneously reset marker: no deletion of that account.
	tx, err := db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT id FROM accounts WHERE id=6 FOR UPDATE`)
	require.NoError(t, err)
	count, err := r.CleanupSpaceWorkflows(ctx, cfg, now)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	_, err = tx.Exec(`UPDATE space_account_workflows SET completed_at=NULL,payload=jsonb_set(payload,'{completed}','false') WHERE account_id=6`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	count, err = r.CleanupSpaceWorkflows(ctx, cfg, now.Add(6*time.Minute))
	require.NoError(t, err)
	require.Zero(t, count)
	var ids string
	require.NoError(t, db.QueryRow(`SELECT string_agg(id::text,',' ORDER BY id) FROM accounts WHERE deleted_at IS NOT NULL`).Scan(&ids))
	require.Equal(t, "1,10", ids)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_groups`).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduled_test_plans`).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduler_outbox`).Scan(&n))
	require.Equal(t, 2, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_logs`).Scan(&n))
	require.Equal(t, 1, n)
	cfg.Enabled = false
	raw, _ = json.Marshal(cfg)
	_, err = db.Exec(`UPDATE settings SET value=$1`, string(raw))
	require.NoError(t, err)
	stale := cfg
	stale.Enabled = true
	count, err = r.CleanupSpaceWorkflows(ctx, stale, now.Add(24*time.Hour))
	require.NoError(t, err)
	require.Zero(t, count)
}
