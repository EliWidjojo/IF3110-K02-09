package migration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"github.com/lib/pq"
	"time"
)

var ErrInvalidMigration = errors.New("SQL migration tidak valid atau tidak didukung")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

var ErrNeedsReview = errors.New("history memiliki NEEDS_REVIEW; DBA harus memeriksa dan menyelesaikan percobaan tersebut sebelum melanjutkan")
var ErrLegacyHistory = errors.New("history legacy ditemukan; jalankan impor history eksplisit sebelum migration baru")

var ErrUnsafeTarget = errors.New("setup history atau privilege target belum aman; gunakan dua akun terbatas sesuai README")
var ErrVersionConflict = errors.New("versi harus lebih besar dari versi sukses terakhir")
var ErrMigrationFailed = errors.New("migration gagal; perubahan transaksi dibatalkan")
var ErrOutcomeUnknown = errors.New("hasil belum dapat dipastikan; periksa history sebelum mengulangi")

type Attempt struct {
	ID         int64      `json:"id"`
	Version    int        `json:"version"`
	ExecutedBy int        `json:"executed_by"`
	ExecutedAt time.Time  `json:"executed_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	QuerySQL   string     `json:"query_sql"`
	SQLState   string     `json:"sql_state"`
	ErrorCode  string     `json:"error_code"`
}
type ExecutionResult struct {
	AttemptID int64  `json:"attempt_id"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Atomic    bool   `json:"atomic"`
	SQLState  string `json:"sql_state,omitempty"`
}
type Engine struct {
	access   access.DatabasePermissionChecker
	pools    *database.TargetConnectionManager
	identity admin.VerifiedUserProvider
}

func NewEngine(a access.DatabasePermissionChecker, p *database.TargetConnectionManager, i admin.VerifiedUserProvider) *Engine {
	return &Engine{a, p, i}
}

func (e *Engine) Run(ctx context.Context, id int, req MigrationRequest) (ExecutionResult, error) {
	var out ExecutionResult
	if err := e.access.RequireEdit(ctx, id); err != nil {
		return out, err
	}
	user, err := e.identity.VerifiedUserID(ctx)
	if err != nil || user <= 0 {
		return out, admin.ErrUnauthenticated
	}
	if req.Version <= 0 || req.Version > 2147483647 || len(req.QuerySQL) > 12288 {
		return out, &InputError{"versi atau ukuran SQL tidak valid"}
	}
	cfg, err := e.pools.Configuration(ctx, id)
	if err != nil {
		return out, err
	}
	statements, err := database.Statements(req.QuerySQL, cfg.Driver, req.NonAtomicDDL)
	if err != nil {
		return out, errors.Join(ErrInvalidMigration, err)
	}
	if cfg.Driver == database.Postgres {
		if err = validatePostgresMigration(ctx, req.QuerySQL); err != nil {
			return out, errors.Join(ErrInvalidMigration, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target, err := e.pools.Get(ctx, id)
	if err != nil {
		return out, err
	}
	audit, cfg, err := e.pools.OpenAudit(ctx, id)
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
	table := cfg.Driver.Quote(schema) + ".attempts"
	if err = lockHistory(ctx, control, cfg); err != nil {
		return out, err
	}
	defer unlockHistory(control, cfg)
	// Percobaan tertinggal dipagari sebelum versi baru dibaca agar proses lama tidak dapat menyelesaikannya kembali.
	staleStatus := "INTERRUPTED"
	if cfg.Driver.IsMySQL() {
		staleStatus = "NEEDS_REVIEW"
	}
	stale := "UPDATE " + table + " SET status='" + staleStatus + "',error_code='interrupted',capability_hash=NULL,finished_at=CURRENT_TIMESTAMP WHERE status='RUNNING'"
	if _, err = control.ExecContext(ctx, stale); err != nil {
		return out, ErrUnsafeTarget
	}
	if cfg.Driver.IsMySQL() {
		var unresolved int
		if control.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE status='NEEDS_REVIEW'").Scan(&unresolved) != nil {
			return out, ErrUnsafeTarget
		}
		if unresolved > 0 {
			return out, ErrNeedsReview
		}
	}
	legacySchema := "public"
	if cfg.Driver.IsMySQL() {
		legacySchema = cfg.DatabaseName
	}
	var legacyCount int
	q := "SELECT count(*) FROM information_schema.tables WHERE table_schema=" + cfg.Driver.Bind(1) + " AND table_name='schema_migrations'"
	if target.QueryRowContext(ctx, q, legacySchema).Scan(&legacyCount) != nil {
		return out, ErrUnsafeTarget
	}
	if legacyCount > 0 {
		var imported int
		if control.QueryRowContext(ctx, "SELECT count(*) FROM "+cfg.Driver.Quote(schema)+".legacy_sources WHERE id=1").Scan(&imported) != nil || imported == 0 {
			return out, ErrLegacyHistory
		}
	}
	var latest int
	if err = control.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM "+table+" WHERE status='SUCCESS'").Scan(&latest); err != nil {
		return out, ErrUnsafeTarget
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return out, err
	}
	capability := hex.EncodeToString(secret)
	clear(secret)
	digest := sha256.Sum256([]byte(capability))
	insert := "INSERT INTO " + table + "(version,executed_by,query_sql,status,capability_hash) VALUES(" + cfg.Driver.Bind(1) + "," + cfg.Driver.Bind(2) + "," + cfg.Driver.Bind(3) + ",'RUNNING'," + cfg.Driver.Bind(4) + ")"
	var attemptID int64
	if cfg.Driver.IsMySQL() {
		result, x := control.ExecContext(ctx, insert, req.Version, user, req.QuerySQL, hex.EncodeToString(digest[:]))
		if x != nil {
			return out, ErrUnsafeTarget
		}
		attemptID, _ = result.LastInsertId()
	} else if err = control.QueryRowContext(ctx, insert+" RETURNING id", req.Version, user, req.QuerySQL, digest[:]).Scan(&attemptID); err != nil {
		return out, ErrUnsafeTarget
	}
	out = ExecutionResult{AttemptID: attemptID, Version: req.Version, Status: "RUNNING", Atomic: !req.NonAtomicDDL}
	fail := func(cause error, code string) (ExecutionResult, error) {
		state := ""
		var pe *pq.Error
		if errors.As(cause, &pe) {
			state = string(pe.Code)
		}
		logCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		status := "FAILED"
		if req.NonAtomicDDL {
			status = "NEEDS_REVIEW"
		}
		q := "UPDATE " + table + " SET status=" + cfg.Driver.Bind(1) + ",error_code=" + cfg.Driver.Bind(2) + ",sql_state=" + cfg.Driver.Bind(3) + ",finished_at=CURRENT_TIMESTAMP,capability_hash=NULL WHERE id=" + cfg.Driver.Bind(4) + " AND status='RUNNING'"
		result, x := control.ExecContext(logCtx, q, status, code, state, attemptID)
		if x != nil {
			return out, ErrOutcomeUnknown
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			var recorded string
			if control.QueryRowContext(logCtx, "SELECT status FROM "+table+" WHERE id="+cfg.Driver.Bind(1), attemptID).Scan(&recorded) == nil && recorded == "SUCCESS" {
				out.Status = "SUCCESS"
				return out, nil
			}
			return out, ErrOutcomeUnknown
		}
		out.Status, out.SQLState = status, state
		if code == "version_conflict" {
			return out, ErrVersionConflict
		}
		return out, ErrMigrationFailed
	}
	if req.Version <= latest {
		return fail(ErrVersionConflict, "version_conflict")
	}
	if cfg.Driver.IsMySQL() {
		return e.runMySQL(ctx, target, control, cfg, req, statements, capability, out, fail)
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fail(err, "begin_failed")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout','20000',true),set_config('lock_timeout','3000',true),set_config('search_path','public',true)`); err != nil {
		_ = tx.Rollback()
		return fail(err, "setup_failed")
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fail(err, "sql_failed")
		}
	}
	// Capability tidak tersedia pada SQL pengguna dan penyelesaian history ikut transaksi perubahan target.
	if _, err = tx.ExecContext(ctx, `SELECT platform_migrations.complete_attempt($1)`, capability); err != nil {
		_ = tx.Rollback()
		return fail(err, "history_failed")
	}
	if err = tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fail(err, "commit_failed")
	}
	out.Status = "SUCCESS"
	return out, nil
}

func (e *Engine) History(ctx context.Context, id, limit, offset int) ([]Attempt, int, error) {
	if err := e.access.RequireRead(ctx, id); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errors.New("pagination tidak valid")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	audit, cfg, err := e.pools.OpenAudit(ctx, id)
	if err != nil {
		return nil, 0, ErrUnsafeTarget
	}
	defer audit.Close()
	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".attempts"
	rows, err := audit.QueryContext(ctx, "SELECT id,version,executed_by,executed_at,finished_at,status,query_sql,sql_state,error_code FROM "+table+" ORDER BY id DESC LIMIT "+cfg.Driver.Bind(1)+" OFFSET "+cfg.Driver.Bind(2), limit, offset)
	if err != nil {
		return nil, 0, ErrUnsafeTarget
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		if rows.Scan(&a.ID, &a.Version, &a.ExecutedBy, &a.ExecutedAt, &a.FinishedAt, &a.Status, &a.QuerySQL, &a.SQLState, &a.ErrorCode) != nil {
			return nil, 0, ErrUnsafeTarget
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, 0, ErrUnsafeTarget
	}
	rows.Close()
	var latest int
	err = audit.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM "+table+" WHERE status='SUCCESS'").Scan(&latest)
	if err != nil {
		return nil, 0, ErrUnsafeTarget
	}
	return out, latest, nil
}

func lockHistory(ctx context.Context, c *sql.Conn, cfg database.DatabaseConnection) error {
	if cfg.Driver.IsMySQL() {
		var ok int
		if err := c.QueryRowContext(ctx, `SELECT GET_LOCK(?,10)`, HistorySchema(cfg.Driver, cfg.DatabaseName)).Scan(&ok); err != nil || ok != 1 {
			return errors.New("migration sedang dikunci; coba kembali")
		}
		return nil
	}
	if _, err := c.ExecContext(ctx, `SELECT pg_advisory_lock(311009004)`); err != nil {
		return errors.New("migration tidak dapat dikunci")
	}
	return nil
}
func unlockHistory(c *sql.Conn, cfg database.DatabaseConnection) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if cfg.Driver.IsMySQL() {
		_, _ = c.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, HistorySchema(cfg.Driver, cfg.DatabaseName))
	} else {
		_, _ = c.ExecContext(ctx, `SELECT pg_advisory_unlock(311009004)`)
	}
}
