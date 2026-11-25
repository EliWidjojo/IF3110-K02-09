package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"IF3110-K02-09/internal/migration"
	"github.com/lib/pq"
)

// Reproduce the durable state left at a crash boundary. All subsequent
// state transitions go through a fresh worker and the public job endpoint.
func expireAttempt(t *testing.T, r *batchRun, jobID, attempt int64, hash string) {
	t.Helper()
	batchExec(t, r.internal.DB, `UPDATE platform.migration_jobs SET status='RUNNING',attempt_id=$2,
		registration_hash=$3,lease_token='expired',lease_until=clock_timestamp()-interval '1 second',
		finished_at=NULL WHERE id=$1`, jobID, attempt, hash)
}

func TestRecoveryInterruptedRegistration(t *testing.T) {
	for _, mode := range []string{"before_target_commit", "after_target_commit", "wrong_registration_hash"} {
		t.Run(mode, func(t *testing.T) {
			r := newBatchRun(t)
			batchExec(t, r.roles.Executor, "CREATE TABLE counter(n int); INSERT INTO counter VALUES(0)")
			r.submitJob(`{"version":1,"query_sql":"UPDATE public.counter SET n=n+1"}`, "increment")
			j := r.detail()
			hash := strings.Repeat("a", 64)
			var attempt int64
			// PostgreSQL sequences consume the ID even when registration rolls back.
			tx, err := r.roles.Audit.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = tx.QueryRow(`INSERT INTO platform_migrations.attempts(version,executed_by,query_sql,status,capability_hash)
				VALUES(1,$1,$2,'RUNNING',decode($3,'hex')) RETURNING id`, j.SubmittedBy, j.Submission.QuerySQL, hash).Scan(&attempt)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before_target_commit" {
				err = tx.Rollback()
			} else {
				err = tx.Commit()
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong_registration_hash" {
				hash = strings.Repeat("b", 64)
			}
			expireAttempt(t, r, j.ID, attempt, hash)
			r.process(true)
			j = r.detail()
			if mode == "wrong_registration_hash" {
				if j.Status != migration.JobNeedsReview || j.ErrorCode != "attempt_mismatch" || batchValue(t, r.roles.Executor, "SELECT n FROM counter") != "0" || batchValue(t, r.roles.Audit, "SELECT status FROM platform_migrations.attempts WHERE id=$1", attempt) != "RUNNING" {
					t.Fatalf("mismatched registration: %+v", j)
				}
				return
			}
			if j.Status != migration.JobSuccess || j.AttemptID == nil || *j.AttemptID == attempt || batchValue(t, r.roles.Executor, "SELECT n FROM counter") != "1" {
				t.Fatalf("registration recovery: %+v", j)
			}
			if mode == "after_target_commit" && batchValue(t, r.roles.Audit, "SELECT status FROM platform_migrations.attempts WHERE id=$1", attempt) != "INTERRUPTED" {
				t.Fatal("abandoned registration not retired")
			}
			r.process(false)
		})
	}
}

func TestRecoveryUnavailableTargetBackoff(t *testing.T) {
	r := newBatchRun(t)
	db := r.target.DB
	sql := "ALTER DATABASE " + pq.QuoteIdentifier(r.target.Name) + " CONNECTION LIMIT "
	r.submitJob(`{"version":1,"query_sql":"CREATE TABLE public.applied(id int)"}`, "outage")
	batchExec(t, db, sql+"0")
	defer batchExec(t, db, sql+"-1")
	for i := 1; i <= 3; i++ {
		before := time.Now()
		r.process(true)
		j := r.detail()
		delay := time.Second << (i - 1)
		if j.Status != migration.JobRecovering || j.RecoveryCount != i || j.ErrorCode != "target_unavailable" ||
			j.AttemptID != nil || j.NextAttemptAt == nil || j.NextAttemptAt.Sub(before) < delay-50*time.Millisecond {
			t.Fatalf("backoff round %d: %+v", i, j)
		}
		r.process(false)
		time.Sleep(time.Until(*j.NextAttemptAt) + 20*time.Millisecond)
	}
	r.process(true)
	j := r.detail()
	if j.Status != migration.JobFailed || j.RecoveryCount != 3 || j.NextAttemptAt != nil || j.AttemptID != nil {
		t.Fatalf("budget not exhausted: %+v", j)
	}
	r.process(false)
	if batchValue(t, db, "SELECT to_regclass('public.applied') IS NULL") != "true" {
		t.Fatal("failed job executed SQL")
	}
	// Show a newly approved job can also recover when connectivity returns.
	r.submitJob(`{"version":2,"query_sql":"CREATE TABLE public.applied(id int)"}`, "restored")
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobRecovering || j.NextAttemptAt == nil {
		t.Fatalf("expected scheduled recovery: %+v", j)
	}
	batchExec(t, db, sql+"-1")
	time.Sleep(time.Until(*j.NextAttemptAt) + 20*time.Millisecond)
	r.process(true)
	if j = r.detail(); j.Status != migration.JobSuccess || j.RecoveryCount != 1 || batchValue(t, db, "SELECT count(*) FROM platform_migrations.attempts") != "1" {
		t.Fatalf("reconnection: %+v", j)
	}
}

func TestRecoveryAtomicRollbackRetriesOnceCommitted(t *testing.T) {
	r := newBatchRun(t)
	batchExec(t, r.roles.Executor, `CREATE SEQUENCE update_attempts;
		CREATE FUNCTION public.update_gate() RETURNS boolean LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('public.update_attempts')=1 THEN
				RAISE EXCEPTION 'transient serialization failure' USING ERRCODE='40001';
			END IF;
			RETURN true;
		END $$;
		CREATE TABLE counter(n int CHECK(CASE WHEN n>0 THEN public.update_gate() ELSE true END));
		INSERT INTO counter VALUES(0)`)
	r.submitJob(`{"version":1,"query_sql":"UPDATE public.counter SET n=n+1"}`, "atomic-update")
	j := r.detail()
	var id int64
	err := r.roles.Audit.QueryRow(`INSERT INTO platform_migrations.attempts(version,executed_by,query_sql,status)
		VALUES(1,$1,$2,'RUNNING') RETURNING id`, j.SubmittedBy, j.Submission.QuerySQL).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	expireAttempt(t, r, j.ID, id, "")
	for i, code := range []string{"interrupted_rollback", "transient_rollback"} {
		r.process(true)
		j = r.detail()
		if j.Status != migration.JobRecovering || j.ErrorCode != code || j.RecoveryCount != i+1 || j.NextAttemptAt == nil || j.AttemptID != nil ||
			batchValue(t, r.roles.Executor, "SELECT n FROM counter") != "0" {
			t.Fatalf("after %s: %+v", code, j)
		}
		time.Sleep(time.Until(*j.NextAttemptAt) + 20*time.Millisecond)
	}
	r.process(true)
	j = r.detail()
	if j.Status != migration.JobSuccess || j.RecoveryCount != 2 || batchValue(t, r.roles.Executor, "SELECT n FROM counter") != "1" ||
		batchValue(t, r.roles.Audit, "SELECT string_agg(status,',' ORDER BY id) FROM platform_migrations.attempts") != "FAILED,FAILED,SUCCESS" {
		t.Fatalf("atomic retry: %+v", j)
	}
}

func TestRecoveryEvidenceBlocksUntilReconciled(t *testing.T) {
	r := newBatchRun(t)
	batchExec(t, r.roles.Executor, "CREATE TABLE items(id bigint PRIMARY KEY,done boolean); INSERT INTO items VALUES(1,false),(2,false),(3,false)")
	r.submit()
	r.process(true)
	j := r.detail()
	if j.Status != migration.JobSuccess || j.AttemptID == nil {
		t.Fatalf("initial batch: %+v", j)
	}
	initial := r.path
	id := *j.AttemptID
	const stamps = "SELECT array_agg(xmin::text ORDER BY id)::text FROM items"
	before := batchValue(t, r.roles.Executor, stamps)
	batchExec(t, r.roles.Audit, "UPDATE platform_migrations.batch_checkpoints SET cursor_id=99 WHERE attempt_id=$1 AND batch_no=1", id)
	expireAttempt(t, r, j.ID, id, "")
	r.process(true)
	if j = r.detail(); j.Status != migration.JobNeedsReview || j.ErrorCode != "unsafe_checkpoint" {
		t.Fatalf("corrupt evidence accepted: %+v", j)
	}
	r.submitJob(`{"version":2,"query_sql":"CREATE TABLE public.next_version(id int)"}`, "successor")
	next := r.path
	r.process(true)
	blocked := r.detail()
	if blocked.Status != migration.JobBlocked || blocked.AttemptID != nil || batchValue(t, r.roles.Executor, "SELECT to_regclass('public.next_version') IS NULL") != "true" {
		t.Fatalf("successor escaped blocker: %+v", blocked)
	}
	r.path = initial
	status(t, r.b.request("POST", initial+"/recovery", "", map[string]string{"X-CSRF-Token": "wrong"}), 403)
	status(t, r.b.request("POST", initial+"/recovery", "", nil), 202)
	r.process(true)
	if j = r.detail(); j.Status != migration.JobNeedsReview {
		t.Fatalf("inspection bypassed invalid evidence: %+v", j)
	}
	batchExec(t, r.roles.Audit, "UPDATE platform_migrations.batch_checkpoints SET cursor_id=row_ids[cardinality(row_ids)] WHERE attempt_id=$1 AND batch_no=1", id)
	status(t, r.b.request("POST", initial+"/recovery", "", nil), 202)
	r.process(true)
	if j = r.detail(); j.Status != migration.JobSuccess || j.CommittedRows != 3 || batchValue(t, r.roles.Executor, stamps) != before {
		t.Fatalf("inspection repeated SQL or lost progress: %+v", j)
	}
	r.path = next
	if blocked.NextAttemptAt == nil {
		t.Fatal("blocked job has no wakeup")
	}
	time.Sleep(time.Until(*blocked.NextAttemptAt) + 20*time.Millisecond)
	r.process(true)
	if j = r.detail(); j.Status != migration.JobSuccess || batchValue(t, r.roles.Executor, "SELECT to_regclass('public.next_version') IS NOT NULL") != "true" {
		t.Fatalf("successor never ran: %+v", j)
	}
}

func TestRecoveryNonAtomicDDLNeedsEvidence(t *testing.T) {
	f, m := mariaTarget(t)
	r := jobRunForTarget(t, f)
	r.submitJob(`{"version":1,"query_sql":"CREATE TABLE applied(id INT PRIMARY KEY) ENGINE=InnoDB","non_atomic_ddl":true}`, "ddl")
	j := r.detail()
	table := fmt.Sprintf("`%s`.attempts", m.History)
	res, err := m.Audit.Exec("INSERT INTO "+table+"(version,executed_by,query_sql,status) VALUES(?,?,?,'RUNNING')", j.Submission.Version, j.SubmittedBy, j.Submission.QuerySQL)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	batchExec(t, m.Executor, j.Submission.QuerySQL)
	batchExec(t, m.Executor, "INSERT INTO applied VALUES(7)")
	expireAttempt(t, r, j.ID, id, "")
	r.process(true)
	if j = r.detail(); j.Status != migration.JobNeedsReview || j.ErrorCode != "outcome_unknown" || batchValue(t, m.Audit, "SELECT status FROM "+table+" WHERE id=?", id) != "NEEDS_REVIEW" {
		t.Fatalf("non-atomic outcome: %+v", j)
	}
	status(t, r.b.request("POST", r.path+"/recovery", "", nil), 202)
	r.process(true)
	if r.detail().Status != migration.JobNeedsReview {
		t.Fatal("inspection guessed a DDL result")
	}
	batchExec(t, m.Audit, "UPDATE "+table+" SET status='SUCCESS',error_code='' WHERE id=?", id)
	status(t, r.b.request("POST", r.path+"/recovery", "", nil), 202)
	r.process(true)
	if j = r.detail(); j.Status != migration.JobSuccess || batchValue(t, m.Audit, "SELECT count(*) FROM "+table) != "1" || batchValue(t, m.Executor, "SELECT id FROM applied") != "7" {
		t.Fatalf("operator reconciliation: %+v", j)
	}
}
