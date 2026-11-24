package migration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"IF3110-K02-09/internal/database"
)

// A claim token identifies one acquisition, even if two processes share a worker name.
// A reclaimed job with an attempt ID is reconciled against target history.
var ErrLeaseLost = errors.New("lease job sudah berakhir")

type Worker struct {
	db   *sql.DB
	repo *database.DatabaseConnectionRepository
	name string
}

func NewWorker(db *sql.DB, repo *database.DatabaseConnectionRepository, name string) (*Worker, error) {
	if db == nil || repo == nil || strings.TrimSpace(name) == "" || len(name) > 128 {
		return nil, errors.New("konfigurasi worker tidak valid")
	}
	return &Worker{db: db, repo: repo, name: name}, nil
}

type claim struct {
	job   Job
	token string
}

func (w *Worker) claim(ctx context.Context) (claim, error) {
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return claim{}, err
	}
	token := hex.EncodeToString(secret[:])
	j, err := scanJob(w.db.QueryRowContext(ctx, `WITH candidate AS (
	 SELECT j.id FROM platform.migration_jobs j JOIN platform.job_admission a ON a.id=j.id
	 WHERE (j.status IN ('QUEUED','READY') AND (a.approved OR j.cancel_waiting)) OR
	       (j.status='RUNNING' AND j.lease_token IS NOT NULL AND j.lease_until <= clock_timestamp())
	 ORDER BY j.updated_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1
	)
	UPDATE platform.migration_jobs SET worker_id=$1,lease_token=$2,status='RUNNING',
	 heartbeat_at=clock_timestamp(),lease_until=clock_timestamp()+interval '30 seconds',
	 started_at=COALESCE(started_at,clock_timestamp()),error_code=''
	 WHERE id=(SELECT id FROM candidate) RETURNING `+jobColumns, w.name, token))
	return claim{j, token}, err
}

const ownsLease = `id=$1 AND lease_token=$2 AND status='RUNNING' AND lease_until>clock_timestamp()`

func (w *Worker) heartbeat(ctx context.Context, c claim) error {
	res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET heartbeat_at=clock_timestamp(),
	 lease_until=clock_timestamp()+interval '30 seconds' WHERE `+ownsLease, c.job.ID, c.token)
	return checkOwner(res, err)
}

func checkOwner(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrLeaseLost
	}
	return err
}

// ProcessNext claims one approved job and keeps its lease alive through execution.
func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := w.claim(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.heartbeat(ctx, c); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = w.process(ctx, c)
	cancel()
	<-done
	return true, err
}

func (w *Worker) process(ctx context.Context, c claim) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Pin the destination while opening both target connections and recording the result.
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT target_version FROM platform.database_connections WHERE id=$1 FOR SHARE`, c.job.DatabaseID).Scan(&version); err != nil {
		return err
	}
	finish := func(status JobStatus, code string) error {
		if c.job.CancelOrigin != "" && status == JobFailed {
			status = JobNeedsReview
			if c.job.RevertedBatches > 0 {
				status = JobPartiallyReverted
			}
		}
		if c.job.retryPending && status == JobFailed && c.job.CommittedBatches > 0 {
			status = JobPartial
		}
		// A cancellation may arrive after the last batch boundary. Publish
		// the actual forward outcome and hand off under the same job row lock.
		res, err := tx.ExecContext(ctx, `UPDATE platform.migration_jobs SET
		 status=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN 'QUEUED' ELSE $3 END,
		 cancel_origin=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN $3 ELSE cancel_origin END,
		 cancel_waiting=(cancel_waiting AND $3='QUEUED'),
		 error_code=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN '' ELSE $4 END,
		 retry_pending=(retry_pending AND $3='QUEUED'),
		 lease_token=NULL,lease_until=NULL,finished_at=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN NULL
		 WHEN $3 IN ('FAILED','SUCCESS','PARTIAL','NEEDS_REVIEW','REVERTED','PARTIALLY_REVERTED') THEN clock_timestamp() ELSE NULL END
		 WHERE `+ownsLease, c.job.ID, c.token, status, code)
		if err = checkOwner(res, err); err != nil {
			return err
		}
		return tx.Commit()
	}
	admission := func() (string, error) {
		var code string
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN same_target IS NOT TRUE THEN 'target_changed'
		  WHEN NOT can_edit THEN 'permission_revoked' WHEN NOT approved THEN 'approval_required'
		  ELSE '' END FROM platform.job_admission WHERE id=$1`, c.job.ID).Scan(&code)
		return code, err
	}
	check := func(code string) error {
		if code == "approval_required" {
			return finish(JobQueued, code)
		}
		return finish(JobFailed, code)
	}
	if code, err := admission(); err != nil {
		return err
	} else if code != "" && (c.job.AttemptID == nil || c.job.retryPending || (c.job.CancelOrigin != "" && !c.job.CancelWaiting) || code == "target_changed") {
		return check(code)
	}
	if err = ValidateSubmission(ctx, c.job.Submission, c.job.Driver, "preflight"); err != nil {
		return finish(JobFailed, "invalid_revision")
	}
	// Each job opens fresh pools. A pool cached in another process cannot
	// be invalidated by the administration service after a configuration change.
	pools, err := database.NewTargetConnectionManager(w.repo, database.TargetPoolOptions{})
	if err != nil {
		return err
	}
	defer pools.Close()
	target, err := pools.Get(ctx, c.job.DatabaseID)
	if err != nil {
		return finish(JobFailed, "target_unavailable")
	}
	audit, cfg, err := pools.OpenAudit(ctx, c.job.DatabaseID)
	if err != nil {
		return finish(JobFailed, "audit_unavailable")
	}
	defer audit.Close()
	control, err := audit.Conn(ctx)
	if err != nil {
		return finish(JobFailed, "audit_unavailable")
	}
	defer control.Close()
	lockCtx, cancelLock := context.WithTimeout(ctx, 12*time.Second)
	err = lockHistory(lockCtx, control, cfg)
	cancelLock()
	if err != nil {
		return finish(JobQueued, "target_busy")
	}
	defer unlockHistory(control, cfg)
	if cfg.Driver.IsMySQL() {
		err = verifyMySQLHistory(ctx, target, audit, cfg)
	} else {
		err = verifyPostgresHistory(ctx, target, audit)
	}
	if err != nil {
		return finish(JobFailed, "unsafe_target")
	}
	schema := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName))
	reconcile := func(batchError string) error {
		var state string
		var frozen bool
		if c.job.Submission.BatchSize > 0 {
			// Fence the previous executor on the same row its checkpoint and
			// completion functions lock, then read the committed target evidence.
			if _, err := control.ExecContext(ctx, `UPDATE platform_migrations.attempts SET capability_hash=NULL
				WHERE id=$1 AND status='RUNNING'`, *c.job.AttemptID); err != nil {
				return finish(JobNeedsReview, "outcome_unknown")
			}
			if err := control.QueryRowContext(ctx, `SELECT status,batch_scope_ready FROM platform_migrations.attempts
				WHERE id=$1`, *c.job.AttemptID).Scan(&state, &frozen); err != nil {
				return finish(JobNeedsReview, "outcome_unknown")
			}
			if err = w.syncProgress(ctx, &c, control, *c.job.AttemptID); err != nil {
				return finish(JobNeedsReview, "checkpoint_unavailable")
			}
			if frozen && state == "RUNNING" {
				targetState, targetCode := "FAILED", "batch_failed"
				if c.job.CommittedBatches > 0 {
					targetState, targetCode = "NEEDS_REVIEW", "partial_batch"
				}
				if _, err := control.ExecContext(ctx, `UPDATE platform_migrations.attempts
					SET status=$2,error_code=$3,finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`,
					*c.job.AttemptID, targetState, targetCode); err != nil {
					return finish(JobNeedsReview, "outcome_unknown")
				}
				if c.job.CommittedBatches == 0 {
					return finish(JobFailed, batchError)
				}
			}
		} else if err := control.QueryRowContext(ctx, "SELECT status FROM "+schema+".attempts WHERE id="+cfg.Driver.Bind(1), *c.job.AttemptID).Scan(&state); err != nil {
			return finish(JobNeedsReview, "outcome_unknown")
		}
		switch state {
		case "SUCCESS":
			return finish(JobSuccess, "")
		case "FAILED":
			return finish(JobFailed, "sql_failed")
		}
		if c.job.Submission.BatchSize > 0 && c.job.CommittedBatches > 0 {
			return finish(JobPartial, batchError)
		}
		return finish(JobNeedsReview, "outcome_unknown")
	}
	if c.job.AttemptID != nil {
		if c.job.CancelWaiting {
			return reconcile("interrupted")
		}
		if c.job.CancelOrigin != "" {
			if code, err := admission(); err != nil {
				return err
			} else if code != "" {
				return check(code)
			}
			if c.job.Submission.BatchSize == 0 || c.job.Submission.DownSQL == "" {
				return finish(JobNeedsReview, "unsafe_revert")
			}
			return finish(w.cancelBatch(ctx, c, target, control))
		}
		if c.job.retryPending {
			if code, err := admission(); err != nil {
				return err
			} else if code != "" {
				return check(code)
			}
			// Consume this approval round before issuing a fresh target capability.
			// A worker reclaiming an expired lease only reconciles the outcome.
			res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET retry_pending=FALSE WHERE `+ownsLease, c.job.ID, c.token)
			if err = checkOwner(res, err); err != nil {
				return err
			}
			token, err := reopenBatch(ctx, control, c.job)
			if errors.Is(err, errUnsafeCheckpoint) {
				return finish(JobNeedsReview, "unsafe_checkpoint")
			}
			if err != nil {
				return finish(JobNeedsReview, "checkpoint_unavailable")
			}
			if token != "" {
				if err := w.runBatch(ctx, c, target, control, token, *c.job.AttemptID); err != nil && !errors.Is(err, ErrOutcomeUnknown) {
					return reconcile("batch_failed")
				}
			}
			return reconcile("interrupted")
		}
		return reconcile("interrupted")
	}
	var latest, unresolved int
	if err = control.QueryRowContext(ctx, "SELECT COALESCE(MAX(CASE WHEN status='SUCCESS' THEN version END),0),COUNT(CASE WHEN status IN ('RUNNING','NEEDS_REVIEW') THEN 1 END) FROM "+schema+".attempts").Scan(&latest, &unresolved); err != nil {
		return finish(JobFailed, "history_unavailable")
	}
	if unresolved > 0 {
		return finish(JobFailed, "history_requires_review")
	}
	if c.job.Submission.Version <= latest {
		return finish(JobFailed, "version_conflict")
	}
	legacySchema := "public"
	if cfg.Driver.IsMySQL() {
		legacySchema = cfg.DatabaseName
	}
	var legacy int
	if err = target.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema="+cfg.Driver.Bind(1)+" AND table_name='schema_migrations'", legacySchema).Scan(&legacy); err != nil {
		return finish(JobFailed, "history_unavailable")
	}
	if legacy > 0 {
		var imported int
		if err = control.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+schema+".legacy_sources WHERE id=1").Scan(&imported); err != nil || imported == 0 {
			return finish(JobFailed, "legacy_history")
		}
	}
	if code, err := admission(); err != nil {
		return err
	} else if code != "" {
		return check(code)
	}
	if c.job.Submission.SourceJob != 0 {
		var matches bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform.migration_jobs src JOIN platform.migration_jobs j ON j.source_job=src.id
    WHERE j.id=$1 AND j.target_version=src.target_version AND j.database_id=src.database_id AND src.status='SUCCESS' AND src.version=$2)`, c.job.ID, latest).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return finish(JobFailed, "source_not_current")
		}
	}
	req := MigrationRequest{Version: c.job.Submission.Version, QuerySQL: c.job.Submission.QuerySQL, NonAtomicDDL: c.job.Submission.NonAtomicDDL}
	statements, err := database.Statements(req.QuerySQL, cfg.Driver, req.NonAtomicDDL)
	if err != nil {
		return finish(JobFailed, "invalid_revision")
	}
	if err = w.heartbeat(ctx, c); err != nil {
		return err
	}
	start := func(id int64) error {
		res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET attempt_id=$3 WHERE `+ownsLease, c.job.ID, c.token, id)
		if err = checkOwner(res, err); err != nil {
			return err
		}
		c.job.AttemptID = &id
		return nil
	}
	var batch func(string, int64) error
	if c.job.Submission.BatchSize > 0 {
		batch = func(token string, id int64) error { return w.runBatch(ctx, c, target, control, token, id) }
	}
	result, err := executeLocked(ctx, target, control, cfg, c.job.SubmittedBy, req, statements, latest, start, batch)
	switch {
	case err == nil:
		return finish(JobSuccess, "")
	case errors.Is(err, errPartial):
		return finish(JobPartial, "batch_failed")
	case errors.Is(err, ErrOutcomeUnknown) && c.job.AttemptID != nil:
		return reconcile("interrupted")
	case errors.Is(err, ErrOutcomeUnknown) || result.Status == "NEEDS_REVIEW":
		return finish(JobNeedsReview, "outcome_unknown")
	default:
		return finish(JobFailed, "execution_failed")
	}
}
