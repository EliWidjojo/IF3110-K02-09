package migration

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestExecuteMigration_Success(t *testing.T) {
	var db *sql.DB
	var mock sqlmock.Sqlmock
	var err error

	db, mock, err = sqlmock.New()
	if err != nil {
		t.Fatalf("gagal membuat mock database: %v", err)
	}
	defer db.Close()

	service := NewMigrationService(db)

	req := MigrationRequest{
		UserID:     1,
		UserAccess: "View + Edit",
		Version:    1,
		QuerySQL:   "CREATE TABLE test (id INT);",
	}

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations WHERE status = 'SUCCESS';`).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))

	mock.ExpectBegin()

	mock.ExpectExec("CREATE TABLE test").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`INSERT INTO schema_migrations`).
		WithArgs(req.Version, req.UserID, sqlmock.AnyArg(), "SUCCESS", req.QuerySQL).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	err = service.ExecuteMigration(req)
	if err != nil {
		t.Errorf("diharapkan sukses, tetapi dapat error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("ada ekspektasi query yang tidak terpenuhi: %v", err)
	}
}

func TestExecuteMigration_SQLError_Rollback(t *testing.T) {
	var db *sql.DB
	var mock sqlmock.Sqlmock
	var err error

	db, mock, err = sqlmock.New()
	if err != nil {
		t.Fatalf("gagal membuat mock database: %v", err)
	}
	defer db.Close()

	service := NewMigrationService(db)

	req := MigrationRequest{
		UserID:     1,
		UserAccess: "View + Edit",
		Version:    1,
		QuerySQL:   "INSERT INTO missing_table VALUES (1);",
	}

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations WHERE status = 'SUCCESS';`).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))

	mock.ExpectBegin()

	mock.ExpectExec("INSERT INTO missing_table").
		WillReturnError(errors.New("syntax error at or near 'QUERY'"))

	mock.ExpectRollback()

	err = service.ExecuteMigration(req)
	if err == nil {
		t.Errorf("diharapkan error dan rollback, tetapi fungsi mengembalikan sukses")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("ada ekspektasi rollback yang tidak terpenuhi: %v", err)
	}
}

func TestExecuteMigration_AccessDenied(t *testing.T) {
	var db *sql.DB
	var err error

	db, _, err = sqlmock.New()
	if err != nil {
		t.Fatalf("gagal membuat mock database: %v", err)
	}
	defer db.Close()

	service := NewMigrationService(db)

	req := MigrationRequest{
		UserID:     2,
		UserAccess: "View Only",
		Version:    1,
		QuerySQL:   "CREATE TABLE test (id INT);",
	}

	err = service.ExecuteMigration(req)

	if err == nil {
		t.Errorf("diharapkan error penolakan akses, tetapi berhasil")
	} else if err.Error() != "akses ditolak: hanya user dengan role 'View + Edit' yang dapat menjalankan migrasi" {
		t.Errorf("pesan error penolakan akses tidak sesuai: %v", err)
	}
}

func TestGetMigrationHistory_ViewOnly_Success(t *testing.T) {
	var db *sql.DB
	var mock sqlmock.Sqlmock
	var err error

	db, mock, err = sqlmock.New()
	if err != nil {
		t.Fatalf("gagal membuat mock database: %v", err)
	}
	defer db.Close()

	service := NewMigrationService(db)

	rows := sqlmock.NewRows([]string{"id", "version", "executed_by", "executed_at", "status", "query_sql"}).
		AddRow(1, 1, 101, time.Now(), "SUCCESS", "CREATE TABLE users (id INT);")

	mock.ExpectQuery(`SELECT id, version, executed_by, executed_at, status, query_sql FROM schema_migrations`).
		WillReturnRows(rows)

	histories, err := service.GetMigrationHistory("View Only")
	if err != nil {
		t.Errorf("user View Only seharusnya boleh melihat riwayat, error: %v", err)
	}

	if len(histories) != 1 {
		t.Errorf("diharapkan 1 riwayat, didapat: %d", len(histories))
	}
}
