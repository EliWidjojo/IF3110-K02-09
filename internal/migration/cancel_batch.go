package migration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"

	"github.com/lib/pq"
)

var errUnsafeRevert = errors.New("data atau riwayat tidak aman untuk dipulihkan")

// Only identifiers and the boolean literal from the batch grammar enter SQL.
// The job stores the full down statement before its first approval.
func batchRestoreSQL(query string) string {
	m := batchUpdate.FindStringSubmatch(query)
	if m == nil || strings.EqualFold(m[2], "id") {
		return ""
	}
	table, column, value := batchIdentifiers(m)
	return "UPDATE ONLY " + table + " AS t SET " + column + "=r.old_value " +
		"FROM pg_catalog.jsonb_to_recordset($1::jsonb) AS r(id bigint,old_value boolean,stamp text,tuple_id text,rel bigint,att integer) " +
		"WHERE t.id=r.id AND t.tableoid::bigint=r.rel AND t.xmin::text=r.stamp AND t.ctid::text=r.tuple_id " +
		"AND r.att=$2 AND t.id=ANY($3::bigint[]) AND t." + column + " IS NOT DISTINCT FROM " + value
}

func batchIdentifiers(m []string) (table, column, value string) {
	return "public." + pq.QuoteIdentifier(strings.ToLower(m[1])), pq.QuoteIdentifier(strings.ToLower(m[2])), strings.ToUpper(m[3])
}

// Hold the relation against DDL through commit. Triggers and rewrite rules can
// change other rows that the checkpoint cannot reconstruct.
func recoveryColumn(ctx context.Context, tx *sql.Tx, table, name string) (int, error) {
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog"); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE ONLY "+table+" IN ROW EXCLUSIVE MODE"); err != nil {
		return 0, err
	}
	var att int
	err := tx.QueryRowContext(ctx, `SELECT a.attnum FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid
		WHERE c.oid=$1::regclass AND c.relkind='r' AND NOT c.relrowsecurity AND NOT c.relhasrules
		AND a.attname=$2 AND NOT a.attisdropped AND a.atttypid='bool'::regtype
		AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=c.oid)
		AND NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=c.oid AND attgenerated<>'')
		AND EXISTS(SELECT 1 FROM pg_index i JOIN pg_attribute k ON k.attrelid=c.oid AND k.attnum=i.indkey[0]
		 WHERE i.indrelid=c.oid AND i.indisprimary AND i.indnkeyatts=1 AND k.attname='id'
		 AND k.atttypid IN ('int2'::regtype,'int4'::regtype,'int8'::regtype))`, table, strings.ToLower(name)).Scan(&att)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errUnsafeRevert
	}
	return att, err
}

func captureBatch(ctx context.Context, tx *sql.Tx, query string, keys pq.Int64Array) (string, error) {
	m := batchUpdate.FindStringSubmatch(query)
	table, column, value := batchIdentifiers(m)
	att, err := recoveryColumn(ctx, tx, table, m[2])
	if err != nil {
		return "", err
	}
	// Lock before reading the old values. RETURNING identifies the precise
	// tuple version written by this transaction, including for unchanged values.
	q := `WITH prior_values AS MATERIALIZED (SELECT id,` + column + ` AS old_value FROM ONLY ` + table + `
		WHERE id=ANY($1) ORDER BY id FOR UPDATE), changed AS (
		UPDATE ONLY ` + table + ` AS t SET ` + column + `=` + value + ` FROM prior_values WHERE t.id=prior_values.id
		RETURNING t.id,prior_values.old_value,t.xmin::text AS stamp,t.ctid::text AS tuple_id,t.tableoid::bigint AS rel,$2::integer AS att)
		SELECT count(*),coalesce(jsonb_agg(to_jsonb(changed) ORDER BY id),'[]'::jsonb) FROM changed`
	var count int
	var image string
	if err = tx.QueryRowContext(ctx, q, keys, att).Scan(&count, &image); err != nil {
		return "", err
	}
	if count != len(keys) {
		return "", errUnsafeRevert
	}
	return image, nil
}

// The target advisory lock is held throughout compensation. Rotating the
// capability also fences an executor whose audit connection was disconnected.
func openRevert(ctx context.Context, audit *sql.Conn, j Job, token string) error {
	tx, err := audit.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := *j.AttemptID
	var state, code string
	var frozen bool
	err = tx.QueryRowContext(ctx, `SELECT status,error_code,batch_scope_ready FROM platform_migrations.attempts
		WHERE id=$1 AND version=$2 AND query_sql=$3 AND executed_by=$4 FOR UPDATE`, id, j.Submission.Version, j.Submission.QuerySQL, j.SubmittedBy).Scan(&state, &code, &frozen)
	if err != nil {
		return err
	}
	if state == "REVERTED" {
		return nil
	}
	startable := state == "SUCCESS" && j.CancelOrigin == JobSuccess || state == "NEEDS_REVIEW" &&
		(code == "reverting_batch" || code == "partial_batch" && j.CancelOrigin == JobPartial)
	if !startable || !frozen {
		return errUnsafeRevert
	}
	var safe bool
	err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM platform_migrations.attempts
		WHERE id<>$1 AND (status IN ('RUNNING','NEEDS_REVIEW') OR (status='SUCCESS' AND (id>$1 OR version >= $2))))
		AND (SELECT count(*)>0 AND count(*)=max(c.batch_no) AND count(c.recovery)=count(*)
		 AND bool_and(s.row_ids IS NOT NULL AND s.row_ids=c.row_ids AND jsonb_array_length(c.recovery)=cardinality(c.row_ids))
		 AND coalesce(max(c.batch_no) FILTER (WHERE c.reverted_at IS NULL),0)=count(*) FILTER (WHERE c.reverted_at IS NULL)
		 FROM platform_migrations.batch_checkpoints c LEFT JOIN platform_migrations.batch_scope s USING(attempt_id,batch_no)
		 WHERE c.attempt_id=$1)`, id, j.Submission.Version).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errUnsafeRevert
	}
	digest := sha256.Sum256([]byte(token))
	_, err = tx.ExecContext(ctx, `UPDATE platform_migrations.attempts SET status='NEEDS_REVIEW',error_code='reverting_batch',
		capability_hash=$2 WHERE id=$1`, id, digest[:])
	if err != nil {
		return err
	}
	return tx.Commit()
}

func restorePage(ctx context.Context, target *sql.DB, j Job, token string, n int64, image string, keys pq.Int64Array) error {
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m := batchUpdate.FindStringSubmatch(j.Submission.QuerySQL)
	table, _, _ := batchIdentifiers(m)
	att, err := recoveryColumn(ctx, tx, table, m[2])
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, j.Submission.DownSQL, image, att, keys)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != int64(len(keys)) {
		return errUnsafeRevert
	}
	if _, err = tx.ExecContext(ctx, "SELECT platform_migrations.record_revert($1,$2)", token, n); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) syncReverted(ctx context.Context, c *claim, audit *sql.Conn) error {
	err := audit.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(cardinality(row_ids)),0) FROM platform_migrations.batch_checkpoints
		WHERE attempt_id=$1 AND reverted_at IS NOT NULL`, *c.job.AttemptID).Scan(&c.job.RevertedBatches, &c.job.RevertedRows)
	if err != nil {
		return err
	}
	return w.saveReverted(ctx, *c)
}

func (w *Worker) saveReverted(ctx context.Context, c claim) error {
	res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET reverted_batches=$3,reverted_rows=$4 WHERE `+ownsLease, c.job.ID, c.token, c.job.RevertedBatches, c.job.RevertedRows)
	return checkOwner(res, err)
}

func (w *Worker) cancelBatch(ctx context.Context, c claim, target *sql.DB, audit *sql.Conn) (JobStatus, string) {
	j := c.job
	token := rand.Text()
	err := openRevert(ctx, audit, j, token)
	if err == nil || errors.Is(err, errUnsafeRevert) {
		if e := w.syncReverted(ctx, &c, audit); e != nil {
			return JobNeedsReview, "revert_checkpoint_unavailable"
		}
	}
	if errors.Is(err, errUnsafeRevert) {
		if c.job.RevertedBatches > 0 {
			return JobPartiallyReverted, "unsafe_revert"
		}
		return j.CancelOrigin, "unsafe_revert"
	}
	if err != nil {
		return JobNeedsReview, "revert_checkpoint_unavailable"
	}
	for {
		if err = w.heartbeat(ctx, c); err != nil {
			return JobNeedsReview, "lease_lost"
		}
		var n int64
		var keys pq.Int64Array
		var image string
		err = audit.QueryRowContext(ctx, `SELECT batch_no,row_ids,recovery FROM platform_migrations.batch_checkpoints
			WHERE attempt_id=$1 AND reverted_at IS NULL ORDER BY batch_no DESC LIMIT 1`, *j.AttemptID).Scan(&n, &keys, &image)
		if err != nil {
			break
		}
		if err = restorePage(ctx, target, j, token, n, image, keys); err != nil {
			break
		}
		c.job.RevertedRows += int64(len(keys))
		c.job.RevertedBatches++
		if err = w.saveReverted(ctx, c); err != nil {
			break
		}
	}
	// Wait for any uncertain commit, revoke this capability, then derive the
	// result from durable checkpoints before allowing another approval round.
	if _, e := audit.ExecContext(ctx, `UPDATE platform_migrations.attempts SET capability_hash=NULL
		WHERE id=$1 AND error_code='reverting_batch'`, *j.AttemptID); e != nil {
		return JobNeedsReview, "revert_outcome_unknown"
	}
	if e := w.syncReverted(ctx, &c, audit); e != nil {
		return JobNeedsReview, "revert_checkpoint_unavailable"
	}
	var state string
	if e := audit.QueryRowContext(ctx, "SELECT status FROM platform_migrations.attempts WHERE id=$1", *j.AttemptID).Scan(&state); e != nil {
		return JobNeedsReview, "revert_outcome_unknown"
	}
	if state == "REVERTED" {
		if c.job.RevertedBatches != j.CommittedBatches || c.job.RevertedRows != j.CommittedRows {
			return JobNeedsReview, "revert_checkpoint_mismatch"
		}
		return JobReverted, ""
	}
	code := "revert_failed"
	var pgErr *pq.Error
	if errors.Is(err, errUnsafeRevert) || (errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703" || pgErr.Code.Class() == "23")) {
		code = "unsafe_revert"
	}
	if c.job.RevertedBatches > 0 {
		return JobPartiallyReverted, code
	}
	state, reason := "SUCCESS", ""
	if j.CancelOrigin == JobPartial {
		state, reason = "NEEDS_REVIEW", "partial_batch"
	}
	_, e := audit.ExecContext(ctx, `UPDATE platform_migrations.attempts SET status=$2,error_code=$3
		WHERE id=$1 AND error_code='reverting_batch'`, *j.AttemptID, state, reason)
	if e != nil {
		return JobNeedsReview, "revert_outcome_unknown"
	}
	return j.CancelOrigin, code
}
