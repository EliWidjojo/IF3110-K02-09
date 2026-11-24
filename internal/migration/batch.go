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

func (w *Worker) syncProgress(ctx context.Context, c *claim, audit *sql.Conn, id int64) error {
	var batches, rows int64
	err := audit.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(cardinality(row_ids)),0) FROM platform_migrations.batch_checkpoints WHERE attempt_id=$1`, id).Scan(&batches, &rows)
	if err != nil {
		return err
	}
	c.job.CommittedBatches, c.job.CommittedRows = batches, rows
	return w.progress(ctx, *c, batches, rows)
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
	// Freeze exact membership in PostgreSQL. Only one page of IDs enters Go memory.
	if _, err = conn.ExecContext(ctx, "CREATE TEMP TABLE migration_scope ON COMMIT PRESERVE ROWS AS SELECT id FROM ONLY "+table); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "DROP TABLE IF EXISTS pg_temp.migration_scope")
	if _, err = conn.ExecContext(ctx, "CREATE UNIQUE INDEX ON pg_temp.migration_scope(id)"); err != nil {
		return err
	}
	var batches, rows int64
	var cursor any
	defer func() {
		if outErr != nil && batches > 0 && !errors.Is(outErr, ErrOutcomeUnknown) {
			outErr = errors.Join(errPartial, outErr)
		}
	}()
	for {
		if err = w.heartbeat(ctx, c); err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		keys := pq.Int64Array{}
		err = tx.QueryRowContext(ctx, `SELECT ARRAY(SELECT id FROM pg_temp.migration_scope WHERE ($1::bigint IS NULL OR id>$1) ORDER BY id LIMIT $2)`, cursor, c.job.Submission.BatchSize).Scan(&keys)
		if err != nil {
			tx.Rollback()
			return err
		}
		if len(keys) == 0 {
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
		cursor = keys[len(keys)-1]
		if err = w.progress(ctx, c, batches, rows); err != nil {
			return ErrOutcomeUnknown
		}
	}
}
