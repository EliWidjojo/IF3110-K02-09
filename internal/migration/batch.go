package migration

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/lib/pq"
)

var errPartial = errors.New("sebagian batch sudah committed")

// The deliberately narrow template lets the worker constrain every UPDATE to
// the selected primary keys without rewriting arbitrary user SQL.
var batchUpdate = regexp.MustCompile("(?i)^\\s*UPDATE\\s+public\\.([a-z_][a-z0-9_]{0,62})\\s+SET\\s+([a-z_][a-z0-9_]{0,62})\\s*=\\s*(TRUE|FALSE)\\s*;?\\s*$")

func committedProgress(ctx context.Context, audit *sql.Conn, id int64) (batches, rows int64, err error) {
	err = audit.QueryRowContext(ctx, `SELECT coalesce(max(batch_no),0),coalesce(sum(cardinality(row_ids)),0)
		FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1`, id).Scan(&batches, &rows)
	return
}

func (w *Worker) syncProgress(ctx context.Context, c *claim, audit *sql.Conn, id int64) error {
	batches, rows, err := committedProgress(ctx, audit, id)
	if err != nil {
		return err
	}
	c.job.CommittedBatches, c.job.CommittedRows = batches, rows
	return w.progress(ctx, *c, batches, rows)
}

// The executor reads the application table using one statement snapshot. Only
// the audit account writes scope pages. Publishing the ready flag in the same
// transaction also distinguishes a frozen empty scope from an interrupted capture.
func freezeBatchScope(ctx context.Context, executor, audit *sql.Conn, table string, attempt int64, size int) error {
	tx, err := audit.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ready, hasCheckpoints bool
	err = tx.QueryRowContext(ctx, `SELECT batch_scope_ready, EXISTS(
		SELECT 1 FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1)
		FROM platform_migrations.attempts WHERE id=$1 AND status='RUNNING' FOR UPDATE`, attempt).Scan(&ready, &hasCheckpoints)
	if err != nil || ready {
		return err
	}
	if hasCheckpoints {
		return errors.New("cakupan asli attempt lama tidak tersedia")
	}
	ids, err := executor.QueryContext(ctx, "SELECT id FROM ONLY "+table+" ORDER BY id")
	if err != nil {
		return err
	}
	defer ids.Close()
	page := make(pq.Int64Array, 0, size)
	batchNo := int64(1)
	save := func() error {
		_, err := tx.ExecContext(ctx, `INSERT INTO platform_migrations.batch_scope(attempt_id,batch_no,row_ids)
			VALUES ($1,$2,$3)`, attempt, batchNo, page)
		batchNo++
		page = page[:0]
		return err
	}
	for ids.Next() {
		var key int64
		if err := ids.Scan(&key); err != nil {
			return err
		}
		page = append(page, key)
		if len(page) == size {
			if err := save(); err != nil {
				return err
			}
		}
	}
	if err := ids.Err(); err != nil {
		return err
	}
	if len(page) > 0 {
		if err := save(); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_migrations.attempts SET batch_scope_ready=TRUE WHERE id=$1`, attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) progress(ctx context.Context, c claim, batches, rows int64) error {
	res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET committed_batches=$3, committed_rows=$4 WHERE `+ownsLease, c.job.ID, c.token, batches, rows)
	return checkOwner(res, err)
}

func (w *Worker) runBatch(ctx context.Context, c claim, target *sql.DB, audit *sql.Conn, token string, id int64) (outErr error) {
	match := batchUpdate.FindStringSubmatch(c.job.Submission.QuerySQL)
	if match == nil || strings.EqualFold(match[2], "id") {
		return ErrInvalidMigration
	}
	table := "public." + pq.QuoteIdentifier(strings.ToLower(match[1]))
	column := pq.QuoteIdentifier(strings.ToLower(match[2]))
	var valid bool
	err := audit.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0]
 WHERE i.indrelid=$1::regclass AND i.indisprimary AND i.indnkeyatts=1 AND a.attname='id' AND a.atttypid IN ('int2'::regtype,'int4'::regtype,'int8'::regtype))`, table).Scan(&valid)
	if err != nil || !valid {
		return ErrInvalidMigration
	}
	conn, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = freezeBatchScope(ctx, conn, audit, table, id, c.job.Submission.BatchSize); err != nil {
		return err
	}
	batches, rows, err := committedProgress(ctx, audit, id)
	if err != nil {
		return err
	}
	defer func() {
		if outErr != nil && batches > 0 && !errors.Is(outErr, ErrOutcomeUnknown) {
			outErr = errors.Join(errPartial, outErr)
		}
	}()
	for {
		if err = w.heartbeat(ctx, c); err != nil {
			return err
		}
		var keys pq.Int64Array
		err := audit.QueryRowContext(ctx, `SELECT row_ids FROM platform_migrations.batch_scope
			WHERE attempt_id=$1 AND batch_no=$2`, id, batches+1).Scan(&keys)
		last := errors.Is(err, sql.ErrNoRows)
		if err != nil && !last {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if last {
			_, err = tx.ExecContext(ctx, "SELECT platform_migrations.complete_attempt($1)", token)
			if err != nil {
				tx.Rollback()
				return err
			}
			if err = tx.Commit(); err != nil {
				return ErrOutcomeUnknown
			}
			return nil
		}
		res, err := tx.ExecContext(ctx, "UPDATE ONLY "+table+" SET "+column+" = "+strings.ToUpper(match[3])+" WHERE id=ANY($1)", keys)
		if err != nil {
			tx.Rollback()
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil || affected != int64(len(keys)) {
			tx.Rollback()
			return errors.New("cakupan row berubah")
		}
		if _, err = tx.ExecContext(ctx, "SELECT platform_migrations.record_batch($1,$2,$3)", token, batches+1, keys); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return ErrOutcomeUnknown
		}
		batches++
		rows += affected
		if err = w.progress(ctx, c, batches, rows); err != nil {
			return ErrOutcomeUnknown
		}
	}
}
