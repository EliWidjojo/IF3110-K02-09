package migration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"github.com/lib/pq"
)

var (
	ErrAttemptNotFound     = errors.New("riwayat attempt tidak ditemukan")
	ErrCannotRollbackState = errors.New("hanya migration dengan status SUCCESS yang dapat di-rollback")
	ErrNotLatestVersion    = errors.New("rollback hanya diizinkan pada versi aktif terakhir untuk menjaga urutan skema")
)

func (e *Engine) Rollback(ctx context.Context, dbID int, req RollbackRequest) (ExecutionResult, error) {
	var out ExecutionResult
	if err := e.access.RequireEdit(ctx, dbID); err != nil {
		return out, err
	}
	user, err := e.identity.VerifiedUserID(ctx)
	if err != nil || user <= 0 {
		return out, admin.ErrUnauthenticated
	}
	if req.AttemptID <= 0 {
		return out, &InputError{"attempt_id tidak valid"}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	target, err := e.pools.Get(ctx, dbID)
	if err != nil {
		return out, err
	}
	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer audit.Close()

	if cfg.Driver.IsMySQL() {
		if err = verifyMySQLHistory(ctx, target, audit, cfg); err != nil {
			return out, err
		}
	} else if err = verifyPostgresHistory(ctx, target, audit); err != nil {
		return out, err
	}

	control, err := audit.Conn(ctx)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer control.Close()

	schema := HistorySchema(cfg.Driver, cfg.DatabaseName)
	quotedSchema := cfg.Driver.Quote(schema)
	table := quotedSchema + ".attempts"
	proposalsTable := quotedSchema + ".proposals"

	if err = lockHistory(ctx, control, cfg); err != nil {
		return out, err
	}
	defer unlockHistory(control, cfg)

	var targetVersion int
	var targetStatus string
	err = control.QueryRowContext(ctx, "SELECT version, status FROM "+table+" WHERE id="+cfg.Driver.Bind(1), req.AttemptID).Scan(&targetVersion, &targetStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrAttemptNotFound
	}
	if err != nil {
		return out, ErrUnsafeTarget
	}
	if targetStatus != "SUCCESS" {
		return out, ErrCannotRollbackState
	}

	var latestSuccessVersion int
	if err := control.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM "+table+" WHERE status='SUCCESS'").Scan(&latestSuccessVersion); err != nil {
		return out, ErrUnsafeTarget
	}
	if targetVersion != latestSuccessVersion {
		return out, ErrNotLatestVersion
	}

	rollbackSQL := strings.TrimSpace(req.RollbackSQL)
	if rollbackSQL == "" {
		_ = ensureWorkflowTables(ctx, control, cfg)
		_ = control.QueryRowContext(ctx, "SELECT rollback_sql FROM "+proposalsTable+" WHERE attempt_id="+cfg.Driver.Bind(1)+" ORDER BY id DESC LIMIT 1", req.AttemptID).Scan(&rollbackSQL)
	}
	if rollbackSQL == "" {
		return out, ErrMissingRollbackSQL
	}

	statements, err := database.Statements(rollbackSQL, cfg.Driver, req.NonAtomicDDL)
	if err != nil {
		return out, errors.Join(ErrInvalidMigration, err)
	}
	if cfg.Driver == database.Postgres {
		if err = validatePostgresMigration(ctx, rollbackSQL); err != nil {
			return out, errors.Join(ErrInvalidMigration, err)
		}
	}

	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return out, err
	}
	capability := hex.EncodeToString(secret)
	clear(secret)
	digest := sha256.Sum256([]byte(capability))

	taggedSQL := "-- ROLLBACK OF ATTEMPT #" + strconv.FormatInt(req.AttemptID, 10) + "\n" + rollbackSQL
	insert := "INSERT INTO " + table + "(version,executed_by,query_sql,status,capability_hash) VALUES(" +
		cfg.Driver.Bind(1) + "," + cfg.Driver.Bind(2) + "," + cfg.Driver.Bind(3) + ",'RUNNING'," + cfg.Driver.Bind(4) + ")"

	var rollbackAttemptID int64
	if cfg.Driver.IsMySQL() {
		res, x := control.ExecContext(ctx, insert, targetVersion, user, taggedSQL, hex.EncodeToString(digest[:]))
		if x != nil {
			return out, ErrUnsafeTarget
		}
		rollbackAttemptID, _ = res.LastInsertId()
	} else {
		if err = control.QueryRowContext(ctx, insert+" RETURNING id", targetVersion, user, taggedSQL, digest[:]).Scan(&rollbackAttemptID); err != nil {
			return out, ErrUnsafeTarget
		}
	}

	out = ExecutionResult{AttemptID: rollbackAttemptID, Version: targetVersion, Status: "RUNNING", Atomic: !req.NonAtomicDDL}

	fail := func(cause error, code string) (ExecutionResult, error) {
		state := ""
		var pe *pq.Error
		if errors.As(cause, &pe) {
			state = string(pe.Code)
		}
		logCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		// Kembalikan status attempt asli menjadi SUCCESS karena rollback gagal
		_, _ = control.ExecContext(logCtx, "UPDATE "+table+" SET status='SUCCESS' WHERE id="+cfg.Driver.Bind(1)+" AND status='ROLLED_BACK'", req.AttemptID)
		status := "ROLLBACK_FAILED"
		q := "UPDATE " + table + " SET status=" + cfg.Driver.Bind(1) + ",error_code=" + cfg.Driver.Bind(2) + ",sql_state=" + cfg.Driver.Bind(3) + ",finished_at=CURRENT_TIMESTAMP,capability_hash=NULL WHERE id=" + cfg.Driver.Bind(4)
		_, _ = control.ExecContext(logCtx, q, status, code, state, rollbackAttemptID)
		out.Status, out.SQLState = status, state
		return out, ErrMigrationFailed
	}

	// Bebaskan unique index successful_version pada attempt asli sebelum complete_attempt menandai attempt rollback
	if _, err = control.ExecContext(ctx, "UPDATE "+table+" SET status='ROLLED_BACK' WHERE id="+cfg.Driver.Bind(1), req.AttemptID); err != nil {
		return fail(err, "pre_rollback_update_failed")
	}

	if cfg.Driver.IsMySQL() {
		if req.NonAtomicDDL {
			if _, err = target.ExecContext(ctx, statements[0]); err != nil {
				return fail(err, "rollback_ddl_failed")
			}
			if _, err = control.ExecContext(ctx, "CALL "+quotedSchema+".complete_attempt(?)", capability); err != nil {
				return fail(err, "rollback_history_failed")
			}
		} else {
			tx, err := target.BeginTx(ctx, nil)
			if err != nil {
				return fail(err, "begin_failed")
			}
			defer tx.Rollback()
			for _, stmt := range statements {
				if _, err = tx.ExecContext(ctx, stmt); err != nil {
					_ = tx.Rollback()
					return fail(err, "rollback_sql_failed")
				}
			}
			if _, err = tx.ExecContext(ctx, "CALL "+quotedSchema+".complete_attempt(?)", capability); err != nil {
				_ = tx.Rollback()
				return fail(err, "rollback_history_failed")
			}
			if err = tx.Commit(); err != nil {
				_ = tx.Rollback()
				return fail(err, "commit_failed")
			}
		}
	} else {
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			return fail(err, "begin_failed")
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout','20000',true),set_config('lock_timeout','3000',true),set_config('search_path','public',true)`); err != nil {
			_ = tx.Rollback()
			return fail(err, "setup_failed")
		}
		for _, stmt := range statements {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fail(err, "rollback_sql_failed")
			}
		}
		if _, err = tx.ExecContext(ctx, `SELECT platform_migrations.complete_attempt($1)`, capability); err != nil {
			_ = tx.Rollback()
			return fail(err, "history_failed")
		}
		if err = tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fail(err, "commit_failed")
		}
	}

	// Ubah status attempt rollback menjadi ROLLED_BACK agar tidak terhitung sebagai versi aktif & perbarui proposal
	_, _ = control.ExecContext(ctx, "UPDATE "+table+" SET status='ROLLED_BACK' WHERE id="+cfg.Driver.Bind(1), rollbackAttemptID)
	_, _ = control.ExecContext(ctx, "UPDATE "+proposalsTable+" SET status='ROLLED_BACK' WHERE attempt_id="+cfg.Driver.Bind(1), req.AttemptID)

	out.Status = "ROLLED_BACK"
	return out, nil
}