package migration

import (
	"errors"
	"regexp"
	"testing"

	"IF3110-K02-09/internal/database"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLTransactionalMigration(t *testing.T) {
	for _, dialect := range []database.Dialect{database.MySQL, database.MariaDB} {
		t.Run(string(dialect), func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			service := NewMigrationServiceWithDialect(db, dialect)
			mock.ExpectQuery(`SELECT MAX\(version\)`).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
			mock.ExpectQuery("SELECT COUNT.*information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO items").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO schema_migrations (version, executed_by, executed_at, status, query_sql) VALUES (?, ?, ?, ?, ?)")).WithArgs(1, 7, sqlmock.AnyArg(), "SUCCESS", "INSERT INTO items VALUES (1)").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			if err := service.ExecuteMigration(MigrationRequest{UserID: 7, UserAccess: "View + Edit", Version: 1, QuerySQL: "INSERT INTO items VALUES (1)"}); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMySQLDDLExplicitAndFailureHistory(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			service := NewMigrationServiceWithDialect(db, database.MySQL)
			req := MigrationRequest{UserID: 7, UserAccess: "View + Edit", Version: 1, QuerySQL: "CREATE TABLE items (id INT)"}
			if err := service.ExecuteMigration(req); err == nil {
				t.Fatal("implicit commit accepted without acknowledgement")
			}
			req.NonAtomicDDL = true
			mock.ExpectQuery(`SELECT MAX\(version\)`).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
			mock.ExpectExec("INSERT INTO schema_migrations").WithArgs(1, 7, sqlmock.AnyArg(), "RUNNING", req.QuerySQL).WillReturnResult(sqlmock.NewResult(1, 1))
			exec := mock.ExpectExec("CREATE TABLE items")
			status := "SUCCESS"
			if fail {
				exec.WillReturnError(errors.New("DDL failed"))
				status = "FAILED"
			} else {
				exec.WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectExec("UPDATE schema_migrations SET status").WithArgs(status, 1).WillReturnResult(sqlmock.NewResult(0, 1))
			if err := service.ExecuteMigration(req); (err != nil) != fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestVersionQueryFailureStopsMigration(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	service := NewMigrationService(db)
	mock.ExpectQuery(`SELECT MAX\(version\)`).WillReturnError(errors.New("database offline"))
	if err := service.ExecuteMigration(MigrationRequest{UserID: 1, UserAccess: "View + Edit", Version: 1, QuerySQL: "CREATE TABLE x (id INT)"}); err == nil {
		t.Fatal("version lookup failure ignored")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLNonTransactionalEngineRejected(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	service := NewMigrationServiceWithDialect(db, database.MySQL)
	mock.ExpectQuery(`SELECT MAX\(version\)`).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
	mock.ExpectQuery("SELECT COUNT.*information_schema.tables").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if err := service.ExecuteMigration(MigrationRequest{UserID: 1, UserAccess: "View + Edit", Version: 1, QuerySQL: "INSERT INTO legacy VALUES (1)"}); err == nil {
		t.Fatal("nontransactional table accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
