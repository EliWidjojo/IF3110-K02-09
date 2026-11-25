package migration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/observability"
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
	 WHERE ((j.next_attempt_at IS NULL OR j.next_attempt_at<=clock_timestamp()) AND
	       (j.status IN ('RECOVERING','BLOCKED') OR (j.status IN ('QUEUED','READY') AND (a.approved OR j.cancel_waiting)))
	       AND NOT EXISTS (SELECT 1 FROM platform.migration_jobs busy WHERE busy.database_id=j.database_id
	         AND busy.id<>j.id AND busy.status='RUNNING' AND busy.lease_until>clock_timestamp())) OR
	       (j.status='RUNNING' AND j.lease_token IS NOT NULL AND j.lease_until <= clock_timestamp())
	 ORDER BY CASE WHEN j.status IN ('RUNNING','RECOVERING') THEN 0 ELSE 1 END,j.updated_at,j.id
	 FOR UPDATE OF j SKIP LOCKED LIMIT 1
	)
	UPDATE platform.migration_jobs SET worker_id=$1,lease_token=$2,status='RUNNING',
	 heartbeat_at=clock_timestamp(),lease_until=clock_timestamp()+interval '30 seconds',
	 started_at=COALESCE(started_at,clock_timestamp()),next_attempt_at=NULL,error_code=''
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
	// Sambungkan span worker ke trace request pengaju yang tersimpan pada job.
	var parent string
	_ = w.db.QueryRowContext(ctx, `SELECT trace_context FROM platform.migration_jobs WHERE id=$1`, c.job.ID).Scan(&parent)
	ctx, span := observability.Start(observability.ExtractTrace(ctx, parent), "worker.process",
		"worker_id", w.name, "job_id", c.job.ID, "database_id", c.job.DatabaseID, "version", c.job.Submission.Version,
		"operation", c.job.Submission.Operation, "batch_size", c.job.Submission.BatchSize, "recovery_count", c.job.RecoveryCount)
	if c.job.AttemptID != nil {
		ctx = observability.With(ctx, "attempt_id", *c.job.AttemptID)
	}
	slog.InfoContext(ctx, "worker.job.claim", "status", "running", "previous_status", string(c.job.Status), "retry_count", c.job.RetryCount)
	started := time.Now()
	defer func() {
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
		}
		slog.Log(ctx, level, "worker.job.done", "status", map[bool]string{true: "error", false: "processed"}[err != nil],
			"duration_ms", float64(time.Since(started).Microseconds())/1000)
		observability.End(span, err)
	}()
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
		level := slog.LevelInfo
		if status == JobFailed || status == JobNeedsReview || status == JobPartial {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "worker.job.result", "status", string(status), "error_code", code,
			"committed_batches", c.job.CommittedBatches, "committed_rows", c.job.CommittedRows)
		// A cancellation may arrive after the last batch boundary. Publish
		// the actual forward outcome and hand off under the same job row lock.
		res, err := tx.ExecContext(ctx, `UPDATE platform.migration_jobs SET
		 status=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN 'QUEUED' ELSE $3 END,
		 cancel_origin=CASE WHEN cancel_waiting AND $3 IN ('PARTIAL','SUCCESS') THEN $3 ELSE cancel_origin END,
		 cancel_waiting=(cancel_waiting AND $3='QUEUED'),
		 next_attempt_at=CASE WHEN $3='BLOCKED' THEN clock_timestamp()+interval '5 seconds' END,
		 inspect_only=false,
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
	// Queue the next attempt on the database clock. Counting and releasing the
	// lease in one update keeps the retry budget intact across restarts.
	scheduleRecovery := func(code string, restart bool) error {
		if c.job.RecoveryCount >= 3 {
			if restart {
				return finish(JobFailed, code)
			}
			if c.job.AttemptID != nil || c.job.inspectOnly {
				return finish(JobNeedsReview, code)
			}
			return finish(JobFailed, code)
		}
		slog.WarnContext(ctx, "worker.job.retry_scheduled", "status", "RECOVERING", "error_code", code,
			"delay_seconds", 1<<c.job.RecoveryCount, "recovery_count", c.job.RecoveryCount+1)
		res, e := tx.ExecContext(ctx, `UPDATE platform.migration_jobs SET status='RECOVERING',error_code=$3,
			recovery_count=recovery_count+1,next_attempt_at=clock_timestamp()+$4 * interval '1 second',inspect_only=$5,
			attempt_id=CASE WHEN $6 THEN NULL ELSE attempt_id END,
			lease_token=NULL,lease_until=NULL,finished_at=NULL WHERE `+ownsLease, c.job.ID, c.token, code, 1<<c.job.RecoveryCount, c.job.inspectOnly, restart)
		if e = checkOwner(res, e); e != nil {
			return e
		}
		return tx.Commit()
	}
	recoverLater := func(code string) error { return scheduleRecovery(code, false) }
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
	} else if code != "" && (code == "target_changed" || (!c.job.inspectOnly && (c.job.AttemptID == nil || c.job.retryPending || (c.job.CancelOrigin != "" && !c.job.CancelWaiting)))) {
		if code == "target_changed" && c.job.AttemptID != nil {
			return finish(JobNeedsReview, code)
		}
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
		if temporaryTargetError(err) {
			return recoverLater("target_unavailable")
		}
		if c.job.AttemptID != nil {
			return finish(JobNeedsReview, "target_unavailable")
		}
		return finish(JobFailed, "target_unavailable")
	}
	audit, cfg, err := pools.OpenAudit(ctx, c.job.DatabaseID)
	if err != nil {
		if temporaryTargetError(err) {
			return recoverLater("audit_unavailable")
		}
		if c.job.AttemptID != nil {
			return finish(JobNeedsReview, "audit_unavailable")
		}
		return finish(JobFailed, "audit_unavailable")
	}
	defer audit.Close()
	control, err := audit.Conn(ctx)
	if err != nil {
		return recoverLater("audit_unavailable")
	}
	defer control.Close()
	lockCtx, cancelLock := context.WithTimeout(ctx, 12*time.Second)
	lockCtx, lockSpan := observability.Start(lockCtx, "worker.lock")
	lockStarted := time.Now()
	err = lockHistory(lockCtx, control, cfg)
	observability.End(lockSpan, err)
	cancelLock()
	lockStatus := "acquired"
	if errors.Is(err, errTargetBusy) {
		lockStatus = "busy"
	} else if err != nil {
		lockStatus = "error"
	}
	slog.InfoContext(ctx, "worker.lock", "status", lockStatus, "wait_ms", float64(time.Since(lockStarted).Microseconds())/1000)
	if err == nil {
		defer slog.InfoContext(ctx, "worker.lock", "status", "released")
	}
	if err != nil {
		if errors.Is(err, errTargetBusy) {
			// Waiting for another migration does not consume the failure budget.
			res, e := tx.ExecContext(ctx, `UPDATE platform.migration_jobs SET status='RECOVERING',error_code='target_busy',
				next_attempt_at=clock_timestamp()+interval '2 seconds',lease_token=NULL,lease_until=NULL WHERE `+ownsLease, c.job.ID, c.token)
			if e = checkOwner(res, e); e != nil {
				return e
			}
			return tx.Commit()
		}
		return recoverLater("lock_unavailable")
	}
	defer unlockHistory(control, cfg)
	if cfg.Driver.IsMySQL() {
		err = verifyMySQLHistory(ctx, target, audit, cfg)
	} else {
		err = verifyPostgresHistory(ctx, target, audit)
	}
	if err != nil {
		if temporaryTargetError(err) {
			return recoverLater("history_unavailable")
		}
		if c.job.AttemptID != nil {
			return finish(JobNeedsReview, "unsafe_target")
		}
		return finish(JobFailed, "unsafe_target")
	}
	schema := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName))
	if c.job.registrationHash != "" {
		err = w.retireRegistration(ctx, c, control, cfg)
		if errors.Is(err, errAttemptMismatch) {
			return finish(JobNeedsReview, "attempt_mismatch")
		}
		if err != nil {
			return recoverLater("registration_unavailable")
		}
		c.job.AttemptID = nil
		c.job.registrationHash = ""
		if c.job.inspectOnly {
			return finish(JobFailed, "interrupted_before_execution")
		}
	}
	reconcile := func(batchError string) error {
		var state string
		var frozen bool
		if c.job.Submission.BatchSize > 0 {
			// Fence the previous executor on the same row its checkpoint and
			// completion functions lock, then read the committed target evidence.
			if _, err := control.ExecContext(ctx, `UPDATE platform_migrations.attempts SET capability_hash=NULL
				WHERE id=$1 AND status='RUNNING'`, *c.job.AttemptID); err != nil {
				return recoverLater("outcome_unknown")
			}
			if err := control.QueryRowContext(ctx, `SELECT status,batch_scope_ready FROM platform_migrations.attempts
				WHERE id=$1`, *c.job.AttemptID).Scan(&state, &frozen); err != nil {
				return recoverLater("outcome_unknown")
			}
			if frozen {
				ok, err := validBatchEvidence(ctx, control, *c.job.AttemptID, state == "SUCCESS")
				if err != nil {
					return recoverLater("checkpoint_unavailable")
				}
				if !ok {
					return finish(JobNeedsReview, "unsafe_checkpoint")
				}
			}
			if err = w.syncProgress(ctx, &c, control, *c.job.AttemptID); err != nil {
				return recoverLater("checkpoint_unavailable")
			}
			if (state == "FAILED" || state == "INTERRUPTED") && c.job.CommittedBatches > 0 {
				return finish(JobNeedsReview, "checkpoint_status_mismatch")
			}
			if state == "RUNNING" && (frozen || c.job.CommittedBatches == 0) {
				targetState, targetCode := "FAILED", "batch_failed"
				if c.job.CommittedBatches > 0 {
					targetState, targetCode = "NEEDS_REVIEW", "partial_batch"
				}
				if _, err := control.ExecContext(ctx, `UPDATE platform_migrations.attempts
					SET status=$2,error_code=$3,finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`,
					*c.job.AttemptID, targetState, targetCode); err != nil {
					return recoverLater("outcome_unknown")
				}
				if c.job.CommittedBatches == 0 {
					return finish(JobFailed, batchError)
				}
			}
		} else {
			// The completion marker participates in the data transaction. This
			// update waits out its commit and fences any later completion.
			endState := "FAILED"
			if c.job.Submission.NonAtomicDDL {
				endState = "NEEDS_REVIEW"
			}
			q := "UPDATE " + schema + ".attempts SET status=" + cfg.Driver.Bind(1) + ",error_code='interrupted',finished_at=CURRENT_TIMESTAMP,capability_hash=NULL WHERE id=" + cfg.Driver.Bind(2) + " AND status='RUNNING'"
			if _, err := control.ExecContext(ctx, q, endState, *c.job.AttemptID); err != nil {
				return recoverLater("outcome_unknown")
			}
			var reason string
			if err := control.QueryRowContext(ctx, "SELECT status,error_code FROM "+schema+".attempts WHERE id="+cfg.Driver.Bind(1), *c.job.AttemptID).Scan(&state, &reason); err != nil {
				return recoverLater("outcome_unknown")
			}
			if state == "FAILED" && reason == "interrupted" && !c.job.Submission.NonAtomicDDL && !c.job.inspectOnly {
				return scheduleRecovery("interrupted_rollback", true)
			}
		}
		switch state {
		case "SUCCESS":
			return finish(JobSuccess, "")
		case "FAILED", "INTERRUPTED":
			return finish(JobFailed, "sql_failed")
		}
		if c.job.Submission.BatchSize > 0 && c.job.CommittedBatches > 0 {
			return finish(JobPartial, batchError)
		}
		return finish(JobNeedsReview, "outcome_unknown")
	}
	if c.job.AttemptID != nil {
		if err := verifyAttempt(ctx, control, cfg, c.job); errors.Is(err, errAttemptMismatch) {
			return finish(JobNeedsReview, "attempt_mismatch")
		} else if err != nil {
			return recoverLater("history_unavailable")
		}
		if c.job.CancelWaiting {
			return reconcile("interrupted")
		}
		if c.job.CancelOrigin != "" {
			if c.job.Submission.BatchSize == 0 || c.job.Submission.DownSQL == "" {
				return finish(JobNeedsReview, "unsafe_revert")
			}
			var status JobStatus
			var code string
			if c.job.inspectOnly {
				status, code = w.settleRevert(ctx, c, control, ErrOutcomeUnknown)
			} else {
				if denied, err := admission(); err != nil {
					return err
				} else if denied != "" {
					return check(denied)
				}
				status, code = w.cancelBatch(ctx, c, target, control)
			}
			if status == JobNeedsReview && (code == "revert_checkpoint_unavailable" || code == "revert_outcome_unknown") {
				c.job.inspectOnly = true
				return recoverLater(code)
			}
			return finish(status, code)
		}
		if c.job.inspectOnly {
			return reconcile("interrupted")
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
				return recoverLater("checkpoint_unavailable")
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
	if c.job.inspectOnly {
		return finish(JobNeedsReview, "attempt_missing")
	}
	// Target history protects aliases of the same physical database. Also
	// hold new work while this registration still has unresolved evidence
	// on the platform, even when the target has already completed its commit.
	var held bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform.migration_jobs
		WHERE database_id=$1 AND target_version=$2 AND id<>$3 AND attempt_id IS NOT NULL AND
		(status IN ('NEEDS_REVIEW','PARTIAL','PARTIALLY_REVERTED','RECOVERING','RUNNING') OR
		(status='QUEUED' AND (retry_pending OR cancel_origin<>''))))`, c.job.DatabaseID, version, c.job.ID).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return finish(JobBlocked, "history_requires_review")
	}
	var latest, unresolved int
	if err = control.QueryRowContext(ctx, "SELECT COALESCE(MAX(CASE WHEN status='SUCCESS' THEN version END),0),COUNT(CASE WHEN status IN ('RUNNING','NEEDS_REVIEW') THEN 1 END) FROM "+schema+".attempts").Scan(&latest, &unresolved); err != nil {
		return recoverLater("history_unavailable")
	}
	if unresolved > 0 {
		return finish(JobBlocked, "history_requires_review")
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
		return recoverLater("history_unavailable")
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
	start := func(id int64, hash string) error {
		res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET attempt_id=$3,registration_hash=$4 WHERE `+ownsLease, c.job.ID, c.token, id, hash)
		if err = checkOwner(res, err); err != nil {
			return errors.Join(errRegistration, err)
		}
		c.job.AttemptID = &id
		c.job.registrationHash = hash
		slog.InfoContext(ctx, "migration.attempt.registered", "status", "RUNNING", "attempt_id", id)
		return nil
	}
	var batch func(string, int64) error
	if c.job.Submission.BatchSize > 0 {
		batch = func(token string, id int64) error { return w.runBatch(ctx, c, target, control, token, id) }
	}
	result, err := executeLocked(ctx, target, control, cfg, c.job.SubmittedBy, req, statements, latest, start, batch)
	switch {
	case errors.Is(err, errRegistration):
		return err
	case errors.Is(err, errTransientRollback) && result.Status == "FAILED":
		return scheduleRecovery("transient_rollback", true)
	case c.job.registrationHash != "":
		return recoverLater("registration_unavailable")
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
