package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
)

type batchRun struct {
	*setup
	b     *browser
	group int
	path  string
}

func newBatchRun(t *testing.T) *batchRun {
	t.Helper()
	return jobRunForTarget(t, fixture(t))
}

func jobRunForTarget(t *testing.T, f *setup) *batchRun {
	t.Helper()
	group := f.grant(t, 2, "View + Edit")
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/leader", group), `{"user_id":2}`, nil), 204)
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	return &batchRun{setup: f, b: b, group: group}
}

func (r *batchRun) submit() {
	r.b.t.Helper()
	r.submitJob(`{"version":1,"batch_size":2,"query_sql":"UPDATE public.items SET done = TRUE","prepare_cancellation":true}`, "recover-items")
}

func (r *batchRun) submitJob(body, key string) {
	t := r.b.t
	t.Helper()
	resp := r.b.request("POST", fmt.Sprintf("/api/databases/%d/migrations/jobs", r.databaseID),
		body, map[string]string{"Idempotency-Key": key})
	status(t, resp, 202)
	r.path = resp.Header().Get("Location")
	r.approve(0)
}

func (r *batchRun) approve(round int) {
	r.b.t.Helper()
	status(r.b.t, r.b.request("PUT", fmt.Sprintf("%s/approvals/%d?retry_count=%d", r.path, r.group, round), "", nil), 204)
}

func (r *batchRun) action(action string, round, want int) {
	r.b.t.Helper()
	status(r.b.t, r.b.request("PUT", fmt.Sprintf("%s/%s/%d", r.path, action, round), "", nil), want)
}

func (r *batchRun) worker() *migration.Worker {
	t := r.b.t
	t.Helper()
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	w, err := migration.NewWorker(r.internal.DB, database.NewDatabaseConnectionRepository(r.internal.DB, cipher), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (r *batchRun) process(want bool) {
	r.b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if worked, err := r.worker().ProcessNext(ctx); err != nil || worked != want {
		r.b.t.Fatalf("worker returned worked=%t err=%v, want worked=%t", worked, err, want)
	}
}

func (r *batchRun) detail() migration.Job {
	t := r.b.t
	t.Helper()
	resp := r.b.request("GET", r.path, "", nil)
	status(t, resp, 200)
	var envelope struct{ Data migration.Job }
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func batchExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func batchValue(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBatchCheckpointRetryAndCancel(t *testing.T) {
	r := newBatchRun(t)
	db, audit := r.roles.Executor, r.roles.Audit
	batchExec(t, db, `CREATE TABLE items(id bigint PRIMARY KEY,done boolean,CONSTRAINT fail_second CHECK(id<>3 OR done IS NOT TRUE));
		INSERT INTO items VALUES(1,NULL),(2,false),(3,false),(4,true),(5,NULL)`)
	r.submit()
	r.process(true)
	j := r.detail()
	if j.Status != migration.JobPartial || j.AttemptID == nil || j.CommittedBatches != 1 || j.CommittedRows != 2 {
		t.Fatalf("partial job: %+v", j)
	}
	id := *j.AttemptID
	if scope := batchValue(t, audit, `SELECT string_agg(row_ids::text,';' ORDER BY batch_no) FROM platform_migrations.batch_scope WHERE attempt_id=$1`, id); scope != "{1,2};{3,4};{5}" {
		t.Fatal("frozen scope:", scope)
	}
	if checkpoint := batchValue(t, audit, `SELECT string_agg(concat_ws(':',batch_no,cursor_id,row_ids),'/' ORDER BY batch_no) FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1`, id); checkpoint != "1:2:{1,2}" {
		t.Fatal("checkpoint after failure:", checkpoint)
	}
	const values = `SELECT array_agg(done ORDER BY id)::text FROM items`
	if got := batchValue(t, db, values); got != "{t,t,f,t,NULL}" {
		t.Fatal("data after failed batch:", got)
	}
	const firstStamp = "SELECT xmin::text FROM items WHERE id=1"
	stamp := batchValue(t, db, firstStamp)
	batchExec(t, db, "ALTER TABLE items DROP CONSTRAINT fail_second; INSERT INTO items VALUES(6,false)")
	r.action("retries", 1, 202)
	r.action("retries", 1, 202)
	r.process(false)
	status(t, r.b.request("PUT", fmt.Sprintf("%s/approvals/%d?retry_count=0", r.path, r.group), "", nil), 403)
	r.approve(1)
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobSuccess || j.AttemptID == nil || *j.AttemptID != id || j.CommittedBatches != 3 || j.CommittedRows != 5 {
		t.Fatalf("retried job: %+v", j)
	}
	if batchValue(t, db, firstStamp) != stamp || batchValue(t, db, values) != "{t,t,t,t,t,f}" {
		t.Fatal("retry rewrote a committed row or changed the frozen scope")
	}

	// Block restoration of page 1. Pages 3 and 2 must already have committed.
	batchExec(t, db, "ALTER TABLE items ADD CONSTRAINT block_restore CHECK(id<>2 OR done IS TRUE)")
	r.action("cancellations", 2, 202)
	r.process(false)
	r.approve(2)
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobPartiallyReverted || j.RevertedBatches != 2 || j.RevertedRows != 3 {
		t.Fatalf("partially reverted job: %+v", j)
	}
	if got := batchValue(t, audit, `SELECT array_agg(batch_no ORDER BY batch_no)::text FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1 AND reverted_at IS NOT NULL`, id); got != "{2,3}" {
		t.Fatal("reverted checkpoints:", got)
	}
	if got := batchValue(t, db, values); got != "{t,t,f,t,NULL,f}" {
		t.Fatal("partial compensation:", got)
	}
	r.action("retries", 3, 409)
	const lastStamp = "SELECT xmin::text FROM items WHERE id=5"
	restored := batchValue(t, db, lastStamp)
	batchExec(t, db, "ALTER TABLE items DROP CONSTRAINT block_restore")
	r.action("cancellations", 3, 202)
	r.approve(3)
	r.process(true)
	r.action("cancellations", 3, 202)
	r.process(false)
	j = r.detail()
	if j.Status != migration.JobReverted || j.RevertedBatches != 3 || j.RevertedRows != 5 || j.RetryCount != 3 {
		t.Fatalf("reverted job: %+v", j)
	}
	if batchValue(t, db, lastStamp) != restored || batchValue(t, db, values) != "{NULL,f,f,t,NULL,f}" {
		t.Fatal("resume changed a restored row or did not restore the original values")
	}
	if got := batchValue(t, audit, "SELECT status FROM platform_migrations.attempts WHERE id=$1", id); got != "REVERTED" {
		t.Fatal("target attempt:", got)
	}
}

func TestBatchCancelPreservesLaterWrite(t *testing.T) {
	r := newBatchRun(t)
	db := r.roles.Executor
	batchExec(t, db, "CREATE TABLE items(id bigint PRIMARY KEY,done boolean); INSERT INTO items VALUES(1,false),(2,NULL)")
	r.submit()
	r.process(true)
	if j := r.detail(); j.Status != migration.JobSuccess {
		t.Fatalf("apply: %+v", j)
	}
	// Even the same boolean value is a new tuple owned by another transaction.
	batchExec(t, db, "UPDATE items SET done=true WHERE id=2")
	r.action("cancellations", 1, 202)
	r.approve(1)
	r.process(true)
	j := r.detail()
	if j.Status != migration.JobSuccess || j.ErrorCode != "unsafe_revert" || j.RevertedRows != 0 {
		t.Fatalf("unsafe compensation: %+v", j)
	}
	if got := batchValue(t, db, "SELECT array_agg(done ORDER BY id)::text FROM items"); got != "{t,t}" {
		t.Fatal("failed compensation leaked writes:", got)
	}
}

func TestBatchCancelWhileRunningAndReclaim(t *testing.T) {
	r := newBatchRun(t)
	db := r.roles.Executor
	batchExec(t, db, `CREATE TABLE items(id bigint PRIMARY KEY,done boolean);
		INSERT INTO items SELECT i,false FROM generate_series(1,6) i`)
	r.submit()
	// Hold one row from page 2 so approval lands between two known commits.
	lock, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer lock.Rollback()
	var blocker int
	if err := lock.QueryRow("SELECT pg_backend_pid() FROM items WHERE id=3 FOR UPDATE").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	worker := r.worker()
	done := make(chan error, 1)
	go func() {
		worked, err := worker.ProcessNext(ctx)
		if !worked && err == nil {
			err = fmt.Errorf("no queued job claimed")
		}
		done <- err
	}()
	for {
		var blocked bool
		err := r.target.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked)
		if err != nil {
			t.Fatal("waiting for page 2:", err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("worker stopped before page 2: %v", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
	if j := r.detail(); j.Status != migration.JobRunning || j.CommittedBatches != 1 {
		t.Fatalf("blocked worker: %+v", j)
	}
	r.action("cancellations", 1, 202)
	r.approve(1)
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	j := r.detail()
	if j.Status != migration.JobQueued || j.CancelWaiting || j.CancelOrigin != migration.JobPartial || j.CommittedBatches != 2 || j.CommittedRows != 4 {
		t.Fatalf("boundary handoff: %+v", j)
	}
	if got := batchValue(t, db, "SELECT array_agg(done ORDER BY id)::text FROM items"); got != "{t,t,t,t,f,f}" {
		t.Fatal("data at boundary:", got)
	}
	r.process(true)
	if j = r.detail(); j.Status != migration.JobReverted || j.RevertedRows != 4 {
		t.Fatalf("revert: %+v", j)
	}
	stamps := `SELECT array_agg(xmin::text ORDER BY id)::text FROM items`
	before := batchValue(t, db, stamps)
	// Model loss of the final platform update after the target already committed.
	batchExec(t, r.internal.DB, `UPDATE platform.migration_jobs SET status='RUNNING',finished_at=NULL,
		reverted_batches=0,reverted_rows=0,lease_token='expired',lease_until=now()-interval '1 minute' WHERE id=$1`, j.ID)
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobReverted || j.RevertedBatches != 2 || j.RevertedRows != 4 || batchValue(t, db, stamps) != before {
		t.Fatalf("reconciliation repeated writes or lost progress: %+v", j)
	}
}

func TestBatchTransientRetryLimit(t *testing.T) {
	r := newBatchRun(t)
	db := r.roles.Executor
	// Sequence increments survive rollbacks, so they count failed transactions.
	// Only the first row of page 2 participates in the injected contention.
	batchExec(t, db, `CREATE SEQUENCE retry_events;
		CREATE FUNCTION public.retry_gate() RETURNS boolean LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('public.retry_events') <= 5 THEN
				RAISE EXCEPTION 'simulated serialization failure' USING ERRCODE='40001';
			END IF;
			RETURN true;
		END $$;
		CREATE TABLE items(id bigint PRIMARY KEY,done boolean,
			CHECK(CASE WHEN id=3 AND done THEN public.retry_gate() ELSE true END));
		INSERT INTO items SELECT i,false FROM generate_series(1,4) i`)
	r.submit()
	start := time.Now()
	r.process(true)
	j := r.detail()
	if j.Status != migration.JobPartial || j.CommittedRows != 2 || batchValue(t, db, "SELECT last_value FROM retry_events") != "4" {
		t.Fatalf("expected first attempt plus three retries on page 2: %+v", j)
	}
	if elapsed := time.Since(start); elapsed < 7*time.Second {
		t.Fatalf("three retries took %v, below cumulative backoff", elapsed)
	}
	r.action("retries", 1, 202)
	r.approve(1)
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobSuccess || j.CommittedBatches != 2 || j.CommittedRows != 4 || batchValue(t, db, "SELECT last_value FROM retry_events") != "6" {
		t.Fatalf("expected a transient failure followed by a successful retry: %+v", j)
	}
}
