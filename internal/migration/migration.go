package migration

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type MigrationRequest struct {
	UserID      int
	UserAccess  string // "View Only" atau "View + Edit"
	Version     int
	Description string
	QuerySQL    string
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
}

func NewMigrationService(db *sql.DB) *MigrationService {
	return &MigrationService{TargetDB: db}
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
		return 0, nil // Belum ada migrasi
	}
	return int(latestVersion.Int32), nil
}

func (ms *MigrationService) ExecuteMigration(req MigrationRequest) error {
	// Check Permission Access
	if req.UserAccess != "View + Edit" {
		return errors.New("akses ditolak: hanya user dengan role 'View + Edit' yang dapat menjalankan migrasi")
	}

	// Check Linear Migration Version Order
	latest, err := ms.GetLatestVersion()
	if err == nil && req.Version <= latest {
		return fmt.Errorf("versi migrasi (%d) harus lebih besar dari versi terakhir (%d)", req.Version, latest)
	}

	// Begin Transaction
	tx, err := ms.TargetDB.Begin()
	if err != nil {
		return fmt.Errorf("gagal memulai transaksi: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()

	// Execute User SQL Query
	_, err = tx.Exec(req.QuerySQL)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("migrasi gagal di-execute, seluruh perubahan dibatalkan (rollback): %w", err)
	}

	// Log Success to schema_migrations inside transaction
	logQuery := `
		INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql)
		VALUES ($1, $2, $3, $4, $5);
	`
	_, err = tx.Exec(logQuery, req.Version, req.UserID, time.Now(), "SUCCESS", req.QuerySQL)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("gagal mencatat riwayat migrasi: %w", err)
	}

	// Commit Transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gagal commit transaksi: %w", err)
	}

	fmt.Printf("Migrasi versi %d berhasil dijalankan dan di-commit!\n", req.Version)
	return nil
}

func (ms *MigrationService) GetMigrationHistory(userAccess string) ([]MigrationHistory, error) {
	if userAccess != "View Only" && userAccess != "View + Edit" {
		return nil, errors.New("akses ditolak: anda tidak memiliki izin untuk melihat riwayat migrasi")
	}

	query := `
		SELECT id, version, executed_by, executed_at, status, query_sql 
		FROM schema_migrations 
		ORDER BY version ASC;
	`
	rows, err := ms.TargetDB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("gagal mengambil riwayat migrasi: %w", err)
	}
	defer rows.Close()

	var histories []MigrationHistory
	for rows.Next() {
		var h MigrationHistory
		if err := rows.Scan(&h.ID, &h.Version, &h.ExecutedBy, &h.ExecutedAt, &h.Status, &h.QuerySQL); err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}
	return histories, nil
}