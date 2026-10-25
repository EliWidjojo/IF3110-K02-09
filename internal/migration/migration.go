package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"IF3110-K02-09/internal/database"
)

type MigrationRequest struct {
	UserID       int    `json:"-"` // Set by authenticated backend only.
	UserAccess   string `json:"-"`
	Version      int    `json:"version"`
	Description  string `json:"description"`
	QuerySQL     string `json:"query_sql"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}

type MigrationHistory struct {
	ID         int       `json:"id"`
	Version    int       `json:"version"`
	ExecutedBy int       `json:"executed_by"`
	ExecutedAt time.Time `json:"executed_at"`
	Status     string    `json:"status"`
	QuerySQL   string    `json:"query_sql"`
}

type MigrationService struct {
	TargetDB *sql.DB
	Dialect  database.Dialect
	mu       sync.Mutex
}

func NewMigrationService(db *sql.DB) *MigrationService {
	return NewMigrationServiceWithDialect(db, database.Postgres)
}

func NewMigrationServiceWithDialect(db *sql.DB, dialect database.Dialect) *MigrationService {
	return &MigrationService{TargetDB: db, Dialect: dialect}
}

func (ms *MigrationService) InitMigrationTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		id SERIAL PRIMARY KEY,
		version INT NOT NULL UNIQUE,
		executed_by INT NOT NULL,
		executed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status VARCHAR(20) NOT NULL,
		query_sql TEXT NOT NULL
	);`
	if ms.Dialect.IsMySQL() {
		query = `CREATE TABLE IF NOT EXISTS schema_migrations (
		id BIGINT AUTO_INCREMENT PRIMARY KEY, version INT NOT NULL UNIQUE,
		executed_by INT NOT NULL, executed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status VARCHAR(20) NOT NULL, query_sql LONGTEXT NOT NULL) ENGINE=InnoDB;`
	}
	_, err := ms.TargetDB.Exec(query)
	return err
}

func (ms *MigrationService) GetLatestVersion() (int, error) {
	var latestVersion sql.NullInt32
	query := `SELECT MAX(version) FROM schema_migrations WHERE status = 'SUCCESS';`
	err := ms.TargetDB.QueryRow(query).Scan(&latestVersion)
	if err != nil {
		return 0, err
	}
	if !latestVersion.Valid {
		return 0, nil
	}
	return int(latestVersion.Int32), nil
}

func (ms *MigrationService) ExecuteMigration(req MigrationRequest) error {
	return ms.ExecuteMigrationContext(context.Background(), req)
}

func (ms *MigrationService) ExecuteMigrationContext(ctx context.Context, req MigrationRequest) error {
	// 1. Validasi Akses dan Input Awal
	if req.UserAccess != "View + Edit" {
		return errors.New("akses ditolak: hanya user dengan role 'View + Edit' yang dapat menjalankan migrasi")
	}

	if req.UserID <= 0 || req.Version <= 0 {
		return errors.New("invalid user or migration version")
	}

	statements, err := database.Statements(req.QuerySQL, ms.Dialect, req.NonAtomicDDL)
	if err != nil {
		return err
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	// 2. Validasi Versi Linear
	var latest sql.NullInt32
	err = ms.TargetDB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations WHERE status = 'SUCCESS';`).Scan(&latest)
	if err != nil {
		return fmt.Errorf("cannot read migration version: %w", err)
	}
	if req.Version <= int(latest.Int32) {
		return fmt.Errorf("versi migrasi (%d) harus lebih besar dari versi terakhir (%d)", req.Version, latest.Int32)
	}

	// 3. Eksekusi Non-Atomic DDL
	if req.NonAtomicDDL {
		return ms.executeNonAtomicDDL(ctx, req, statements)
	}

	// 4. Validasi Engine MySQL jika menggunakan transaksi
	if ms.Dialect.IsMySQL() {
		var unsupported int
		err := ms.TargetDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND engine <> 'InnoDB'`).Scan(&unsupported)
		if err != nil {
			return errors.New("cannot verify transactional storage engines")
		}
		if unsupported != 0 {
			return errors.New("transactional migrations require all target tables to use InnoDB")
		}
	}

	// 5. Transaksi Database
	tx, err := ms.TargetDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi: %w", err)
	}

	// Safe Rollback Guard
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		if p := recover(); p != nil {
			panic(p) // Re-throw panic setelah rollback
		}
	}()

	for _, statement := range statements {
		_, err = tx.ExecContext(ctx, statement)
		if err != nil {
			return fmt.Errorf("migrasi gagal di-execute, seluruh perubahan dibatalkan (rollback): %w", err)
		}
	}

	logQuery := `
		INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql)
		VALUES ($1, $2, $3, $4, $5);
	`
	if ms.Dialect.IsMySQL() {
		logQuery = `INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql) VALUES (?, ?, ?, ?, ?);`
	}
	_, err = tx.ExecContext(ctx, logQuery, req.Version, req.UserID, time.Now(), "SUCCESS", req.QuerySQL)
	if err != nil {
		return fmt.Errorf("gagal mencatat riwayat migrasi: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gagal commit transaksi: %w", err)
	}

	committed = true
	return nil
}

// Support Dialect placeholders pada Non-Atomic DDL & eksekusi multi-statements
func (ms *MigrationService) executeNonAtomicDDL(ctx context.Context, req MigrationRequest, statements []string) error {
	insertQuery := `INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql) VALUES ($1, $2, $3, $4, $5);`
	updateQuery := `UPDATE schema_migrations SET status = $1 WHERE version = $2;`

	if ms.Dialect.IsMySQL() {
		insertQuery = `INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql) VALUES (?, ?, ?, ?, ?);`
		updateQuery = `UPDATE schema_migrations SET status = ? WHERE version = ?;`
	}

	_, err := ms.TargetDB.ExecContext(ctx, insertQuery, req.Version, req.UserID, time.Now(), "RUNNING", req.QuerySQL)
	if err != nil {
		return err
	}

	var execErr error
	for _, stmt := range statements {
		_, execErr = ms.TargetDB.ExecContext(ctx, stmt)
		if execErr != nil {
			break
		}
	}

	status := "SUCCESS"
	if execErr != nil {
		status = "FAILED"
	}

	logCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, logErr := ms.TargetDB.ExecContext(logCtx, updateQuery, status, req.Version)
	if logErr != nil {
		return errors.New("non-atomic DDL history unresolved; inspect target and RUNNING entry before retry")
	}
	if execErr != nil {
		return errors.New("non-atomic DDL failed; inspect target, rollback is unavailable")
	}
	return nil
}

func (ms *MigrationService) GetMigrationHistory(userAccess string) ([]MigrationHistory, error) {
	return ms.GetMigrationHistoryContext(context.Background(), userAccess)
}

func (ms *MigrationService) GetMigrationHistoryContext(ctx context.Context, userAccess string) ([]MigrationHistory, error) {
	if userAccess != "View Only" && userAccess != "View + Edit" {
		return nil, errors.New("akses ditolak: anda tidak memiliki izin untuk melihat riwayat migrasi")
	}

	query := `
		SELECT id, version, executed_by, executed_at, status, query_sql 
		FROM schema_migrations 
		ORDER BY version ASC;
	`
	rows, err := ms.TargetDB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("gagal mengambil riwayat migrasi: %w", err)
	}
	defer rows.Close()

	histories := []MigrationHistory{}
	for rows.Next() {
		var h MigrationHistory
		if err := rows.Scan(&h.ID, &h.Version, &h.ExecutedBy, &h.ExecutedAt, &h.Status, &h.QuerySQL); err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}
	return histories, rows.Err()
}