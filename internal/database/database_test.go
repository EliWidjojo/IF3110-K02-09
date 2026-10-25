package database

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStatements(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		dialect   Dialect
		ddl, ok   bool
		count     int
	}{
		{"postgres multi", "CREATE TABLE t (x TEXT); INSERT INTO t VALUES ('a;b');", Postgres, false, true, 2},
		{"dollar literal", "INSERT INTO t VALUES ($tag$a;b$tag$)", Postgres, false, true, 1},
		{"quoted identifier", "CREATE TABLE \"a;b\" (x INT)", Postgres, false, true, 1},
		{"comments", "/* header */ INSERT INTO t VALUES (1); -- tail", MySQL, false, true, 1},
		{"transaction bypass", "INSERT INTO t VALUES (1); COMMIT;", Postgres, false, false, 0},
		{"transaction bypass after quote", "INSERT INTO t VALUES ('a'';b'); COMMIT", Postgres, false, false, 0},
		{"unclosed quote", "INSERT INTO t VALUES ('a)", Postgres, false, false, 0},
		{"mysql implicit commit", "CREATE TABLE t (id INT)", MySQL, false, false, 0},
		{"mysql explicit ddl", "CREATE TABLE t (id INT)", MySQL, true, true, 1},
		{"maria explicit ddl", "ALTER TABLE t ADD x INT", MariaDB, true, true, 1},
		{"mysql multiple ddl", "CREATE TABLE t (id INT); DROP TABLE t", MySQL, true, false, 0},
		{"mysql executable comment", "INSERT INTO t VALUES (1) /*! ; COMMIT */", MySQL, false, false, 0},
		{"procedure", "CREATE PROCEDURE foo() COMMIT", MySQL, true, false, 0},
		{"empty", " -- no SQL", Postgres, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Statements(tc.sql, tc.dialect, tc.ddl)
			if (err == nil) != tc.ok || tc.ok && len(got) != tc.count {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
func TestQuotedIdentifiersAndReadOnlyPagination(t *testing.T) {
	for _, dialect := range []Dialect{Postgres, MySQL, MariaDB} {
		t.Run(string(dialect), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			target := Target{DB: db, Dialect: dialect}
			name := "items`\";DROP TABLE users;--"
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COUNT").WithArgs("app", name).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			query := "SELECT * FROM " + dialect.Quote("app") + "." + dialect.Quote(name) + " LIMIT " + dialect.Bind(1) + " OFFSET " + dialect.Bind(2)
			mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(25, 0).WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow([]byte("hello")))
			mock.ExpectCommit()
			result, err := target.Data(context.Background(), "app", name, 25, 0)
			if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "hello" {
				t.Fatalf("%+v %v", result, err)
			}
			if _, err := target.Data(context.Background(), "app", "items", 101, 0); err == nil {
				t.Fatal("unbounded page accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMetadataAndDatabaseFailure(t *testing.T) {
	for _, dialect := range []Dialect{Postgres, MySQL, MariaDB} {
		t.Run(string(dialect), func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			mock.MatchExpectationsInOrder(false)
			for _, pattern := range []string{"SELECT schema_name", "SELECT table_schema, table_name, table_type", "SELECT table_schema, table_name, column_name", "SELECT table_schema, table_name, constraint_name"} {
				mock.ExpectQuery(pattern).WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("example"))
			}
			index := "FROM pg_indexes"
			if dialect.IsMySQL() {
				index = "FROM information_schema.statistics"
			}
			mock.ExpectQuery(index).WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("index"))
			target := Target{DB: db, Dialect: dialect}
			result, err := target.Metadata(context.Background())
			if err != nil || len(result) != 5 {
				t.Fatalf("%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("connection lost"))
	target := Target{DB: db, Dialect: Postgres}
	if _, err := target.Metadata(context.Background()); err == nil {
		t.Fatal("database failure ignored")
	}
}
