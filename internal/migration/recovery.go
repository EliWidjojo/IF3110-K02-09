package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"IF3110-K02-09/internal/database"
	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
)

var errRegistration = errors.New("pendaftaran attempt terputus")
var errAttemptMismatch = errors.New("attempt target tidak sesuai dengan job")
var errTargetBusy = errors.New("migration sedang dikunci; coba kembali")
var errTransientRollback = errors.New("transaksi sementara gagal dan sudah dibatalkan")

func transientTransaction(err error) bool {
	var pg *pq.Error
	if errors.As(err, &pg) {
		return pg.Code == "40001" || pg.Code == "40P01" || pg.Code == "55P03"
	}
	var my *mysql.MySQLError
	return errors.As(err, &my) && (my.Number == 1213 || my.Number == 1205)
}

func temporaryTargetError(err error) bool {
	err = database.WrapTargetError("memeriksa target", err)
	if errors.Is(err, database.ErrTargetConnectionNetwork) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var failure *database.TargetConnectionError
	if errors.As(err, &failure) {
		return strings.HasPrefix(failure.SQLState, "08") || failure.SQLState == "57P01" || failure.SQLState == "57P03" || failure.SQLState == "53300"
	}
	return false
}

// Keep raw driver details out of the HTTP response. Distinguish an
// unavailable privilege check from an affirmative unsafe configuration.
func historyCheckError(err error) error {
	if temporaryTargetError(err) {
		return database.WrapTargetError("memeriksa riwayat target", err)
	}
	return ErrUnsafeTarget
}

// The audit connection holds the physical database lock. A pending registration
// can have committed its history row, but it could not have executed user SQL.
func (w *Worker) retireRegistration(ctx context.Context, c claim, control *sql.Conn, cfg database.DatabaseConnection) error {
	if c.job.AttemptID == nil {
		return errAttemptMismatch
	}
	d := cfg.Driver
	table := d.Quote(HistorySchema(d, cfg.DatabaseName)) + ".attempts"
	hash := "encode(capability_hash,'hex')"
	if d.IsMySQL() {
		hash = "capability_hash"
	}
	var state, digest string
	err := control.QueryRowContext(ctx, "SELECT status,coalesce("+hash+",'') FROM "+table+" WHERE id="+d.Bind(1), *c.job.AttemptID).Scan(&state, &digest)
	if err == nil {
		if digest != c.job.registrationHash || (state != "RUNNING" && state != "INTERRUPTED") {
			return errAttemptMismatch
		}
		if state == "RUNNING" {
			res, err := control.ExecContext(ctx, "UPDATE "+table+" SET status='INTERRUPTED',error_code='registration_interrupted',finished_at=CURRENT_TIMESTAMP WHERE id="+d.Bind(1)+" AND "+hash+"="+d.Bind(2)+" AND status='RUNNING'", *c.job.AttemptID, digest)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errAttemptMismatch
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	res, err := w.db.ExecContext(ctx, `UPDATE platform.migration_jobs SET attempt_id=NULL,registration_hash='' WHERE `+ownsLease+` AND registration_hash=$3`, c.job.ID, c.token, c.job.registrationHash)
	return checkOwner(res, err)
}

func verifyAttempt(ctx context.Context, control *sql.Conn, cfg database.DatabaseConnection, j Job) error {
	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".attempts"
	var version, user int
	var query string
	err := control.QueryRowContext(ctx, "SELECT version,executed_by,query_sql FROM "+table+" WHERE id="+cfg.Driver.Bind(1), *j.AttemptID).Scan(&version, &user, &query)
	if errors.Is(err, sql.ErrNoRows) {
		return errAttemptMismatch
	}
	if err != nil {
		return err
	}
	if version != j.Submission.Version || user != j.SubmittedBy || query != j.Submission.QuerySQL {
		return errAttemptMismatch
	}
	return nil
}

func validBatchEvidence(ctx context.Context, control *sql.Conn, id int64, complete bool) (bool, error) {
	var ok bool
	err := control.QueryRowContext(ctx, `WITH scope AS (
		SELECT count(*) AS pages,coalesce(max(batch_no),0) AS last FROM platform_migrations.batch_scope WHERE attempt_id=$1
	), committed AS (
		SELECT count(*) AS pages,coalesce(max(c.batch_no),0) AS last,coalesce(bool_and(
			s.batch_no IS NOT NULL AND s.row_ids=c.row_ids AND c.cursor_id=c.row_ids[cardinality(c.row_ids)]),true) AS matches
		FROM platform_migrations.batch_checkpoints c LEFT JOIN platform_migrations.batch_scope s USING(attempt_id,batch_no)
		WHERE c.attempt_id=$1
	) SELECT s.pages=s.last AND c.pages=c.last AND c.matches AND (NOT $2 OR s.pages=c.pages)
	  FROM scope s CROSS JOIN committed c`, id, complete).Scan(&ok)
	return ok, err
}
