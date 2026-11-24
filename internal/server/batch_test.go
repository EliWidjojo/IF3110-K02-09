package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/testutil"
)

// Two workers share one durable queue. Pause the first in its second page,
// after the first commit, while the second worker selects its next database.
func TestBatchParallelAcrossDatabases(t *testing.T) {
	r := newBatchRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	second := testutil.NewPostgres(t)
	roles := second.TargetRoles(t)
	if err := migration.SetupPostgresHistory(ctx, roles.Audit, roles.ExecutorName); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"name": "second", "driver": "postgres", "host": second.Host, "port": second.Port, "database_name": second.Name, "username": roles.ExecutorName, "password": roles.Password, "audit_username": roles.AuditName, "audit_password": roles.AuditPassword, "ssl_mode": "disable"})
	if err != nil {
		t.Fatal(err)
	}
	resp := r.admin.request("POST", "/api/admin/databases", string(config), nil)
	status(t, resp, 201)
	secondID := objectID(t, resp)
	status(t, r.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", r.group, secondID), `{"access_level":"View + Edit"}`, nil), 200)
	r.b.login("alice")
	batchExec(t, r.roles.Executor, "CREATE TABLE items(id bigint PRIMARY KEY,done boolean); INSERT INTO items VALUES (1,false),(2,false),(3,false),(4,false)")
	r.submit()
	firstPath := r.path
	lock, err := r.roles.Executor.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = lock.QueryRowContext(ctx, "SELECT pg_backend_pid() FROM items WHERE id=3 FOR UPDATE").Scan(&pid); err != nil {
		lock.Rollback()
		t.Fatal(err)
	}
	firstWorker := r.worker()
	done := make(chan struct{})
	var worked bool
	var workerErr error
	go func() { defer close(done); worked, workerErr = firstWorker.ProcessNext(ctx) }()
	defer func() { lock.Rollback(); cancel(); <-done }()
	for {
		var waiting bool
		if err = r.target.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-done:
			t.Fatalf("first worker stopped before page 2: %v", workerErr)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if j := r.detail(); j.Status != migration.JobRunning || j.CommittedRows != 2 {
		t.Fatalf("first target: %+v", j)
	}
	// A lock tied only to the first batch transaction would be free now.
	if batchValue(t, r.roles.Audit, "SELECT pg_try_advisory_xact_lock(311009004)") != "false" {
		t.Fatal("target lock released between batch commits")
	}
	r.submitJob(`{"version":2,"query_sql":"CREATE TABLE public.successor(id int)"}`, "same-target")
	samePath := r.path
	firstID := r.databaseID
	r.databaseID = secondID
	r.submitJob(`{"version":1,"query_sql":"CREATE TABLE public.independent(id int)"}`, "other-target")
	r.process(true)
	if j := r.detail(); j.Status != migration.JobSuccess || batchValue(t, roles.Executor, "SELECT to_regclass('public.independent') IS NOT NULL") != "true" {
		t.Fatalf("other target: %+v", j)
	}
	r.path = samePath
	if j := r.detail(); j.Status != migration.JobQueued || j.AttemptID != nil {
		t.Fatalf("same target started too soon: %+v", j)
	}
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	<-done
	if !worked || workerErr != nil {
		t.Fatalf("first worker: %t %v", worked, workerErr)
	}
	r.databaseID = firstID
	r.path = firstPath
	if j := r.detail(); j.Status != migration.JobSuccess || j.CommittedRows != 4 {
		t.Fatalf("first target did not finish: %+v", j)
	}
	r.path = samePath
	r.process(true)
	if j := r.detail(); j.Status != migration.JobSuccess {
		t.Fatalf("successor: %+v", j)
	}
}

func TestQueuedRollbackForwardAndPartialBatch(t *testing.T) {
	f := fixture(t)
	f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	original, code := f.submitAndRun(b, `{"version":1,"query_sql":"CREATE TABLE public.items(id bigint PRIMARY KEY, done boolean)","down_sql":"DROP TABLE public.items"}`)
	if code != 202 || original.Status != "SUCCESS" {
		t.Fatal(original, code)
	}
	undo, code := f.submitAndRun(b, fmt.Sprintf(`{"version":2,"operation":"rollback","source_job":%d}`, original.ID))
	if code != 202 || undo.Status != "SUCCESS" {
		t.Fatal(undo, code)
	}
	redo, code := f.submitAndRun(b, fmt.Sprintf(`{"version":3,"operation":"forward","source_job":%d}`, undo.ID))
	if code != 202 || redo.Status != "SUCCESS" {
		t.Fatal(redo, code)
	}
	_, err := f.roles.Executor.Exec(`INSERT INTO items SELECT i,false FROM generate_series(1,7) i; ALTER TABLE items ADD CHECK(NOT done OR id<>5)`)
	if err != nil {
		t.Fatal(err)
	}
	job, code := f.submitAndRun(b, `{"version":4,"batch_size":3,"query_sql":"UPDATE public.items SET done = TRUE"}`)
	if code != 202 || job.Status != "PARTIAL" || job.CommittedBatches != 1 || job.CommittedRows != 3 {
		t.Fatal(job, code)
	}
	var changed, keys int
	if err = f.roles.Executor.QueryRow("SELECT count(*) FROM items WHERE done").Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if err = f.roles.Audit.QueryRow("SELECT cardinality(row_ids) FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1", job.AttemptID).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if changed != 3 || keys != 3 {
		t.Fatal(changed, keys)
	}
	blocked, _ := f.submitAndRun(b, `{"version":5,"query_sql":"DELETE FROM items"}`)
	if blocked.ErrorCode != "history_requires_review" {
		t.Fatal(blocked)
	}
}

func TestLargeQueuedBatch(t *testing.T) {
	if os.Getenv("TEST_LARGE_BATCH") != "1" {
		t.Skip("set TEST_LARGE_BATCH=1 for the timed data migration")
	}
	f := fixture(t)
	f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	const query = "UPDATE public.large_items SET done = TRUE"
	var baseline time.Duration
	rows := 250000
	for ; rows <= 4000000; rows *= 2 {
		_, err := f.roles.Executor.Exec(`DROP TABLE IF EXISTS public.large_items; CREATE TABLE public.large_items(id bigint PRIMARY KEY, done boolean NOT NULL)`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.roles.Executor.Exec("INSERT INTO public.large_items SELECT i,false FROM generate_series(1,$1::int) i", rows); err != nil {
			t.Fatal(err)
		}
		tx, err := f.roles.Executor.Begin()
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = tx.Exec(query)
		baseline = time.Since(start)
		if rb := tx.Rollback(); rb != nil {
			t.Fatal(rb)
		}
		if err != nil {
			t.Fatal(err)
		}
		if baseline > time.Second {
			break
		}
	}
	if baseline <= time.Second {
		t.Fatal("baseline below required duration")
	}
	start := time.Now()
	j, code := f.submitAndRun(b, fmt.Sprintf(`{"version":1,"query_sql":%q,"batch_size":10000}`, query))
	if code != 202 || j.Status != "SUCCESS" || j.CommittedRows != int64(rows) || j.CommittedBatches != int64((rows+9999)/10000) {
		t.Fatal(code, j)
	}
	var updated int
	if err := f.roles.Executor.QueryRow("SELECT count(*) FROM public.large_items WHERE done").Scan(&updated); err != nil || updated != rows {
		t.Fatal(updated, err)
	}
	t.Logf("rows=%d baseline UPDATE=%v queued batches=%d batch end-to-end=%v", rows, baseline, j.CommittedBatches, time.Since(start))
}
