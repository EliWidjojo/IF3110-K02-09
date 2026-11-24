package server

import (
	"fmt"
	"os"
	"testing"
	"time"
)

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
