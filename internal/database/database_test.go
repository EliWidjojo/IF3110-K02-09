package database_test

import (
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/testutil"
	"IF3110-K02-09/internal/viewer"
	"context"
	"errors"
	"testing"
)

func TestStatements(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		dialect   database.Dialect
		ddl, ok   bool
		count     int
	}{
		{"postgres multi", "CREATE TABLE t (x TEXT); INSERT INTO t VALUES ('a;b');", database.Postgres, false, true, 2},
		{"dollar literal", "INSERT INTO t VALUES ($tag$a;b$tag$)", database.Postgres, false, true, 1},
		{"quoted identifier", "CREATE TABLE \"a;b\" (x INT)", database.Postgres, false, true, 1},
		{"comments", "/* header */ INSERT INTO t VALUES (1); -- tail", database.MySQL, false, true, 1},
		{"transaction bypass", "INSERT INTO t VALUES (1); COMMIT;", database.Postgres, false, false, 0},
		{"transaction bypass after quote", "INSERT INTO t VALUES ('a'';b'); COMMIT", database.Postgres, false, false, 0},
		{"unclosed quote", "INSERT INTO t VALUES ('a)", database.Postgres, false, false, 0},
		{"mysql implicit commit", "CREATE TABLE t (id INT)", database.MySQL, false, false, 0},
		{"mysql explicit ddl", "CREATE TABLE t (id INT)", database.MySQL, true, true, 1},
		{"maria explicit ddl", "ALTER TABLE t ADD x INT", database.MariaDB, true, true, 1},
		{"mysql multiple ddl", "CREATE TABLE t (id INT); DROP TABLE t", database.MySQL, true, false, 0},
		{"mysql executable comment", "INSERT INTO t VALUES (1) /*! ; COMMIT */", database.MySQL, false, false, 0},
		{"procedure", "CREATE PROCEDURE foo() COMMIT", database.MySQL, true, false, 0},
		{"empty", " -- no SQL", database.Postgres, false, false, 0},
		{"literal keyword", "INSERT INTO t VALUES ('function event trigger')", database.Postgres, false, true, 1},
		{"mysql arithmetic", "INSERT INTO t VALUES (1--2)", database.MySQL, false, true, 1},
		{"database DDL rejected", "DROP DATABASE target", database.MariaDB, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := database.Statements(tc.sql, tc.dialect, tc.ddl)
			if (err == nil) != tc.ok || tc.ok && len(got) != tc.count {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestQuotedIdentifiersAndReadOnlyPagination(t *testing.T) {
	p := testutil.NewPostgres(t)
	roles := p.TargetRoles(t)
	name := "items`\";DROP TABLE users;--"
	quoted := database.Postgres.Quote(name)
	if _, err := roles.Executor.Exec("CREATE TABLE " + quoted + "(id INT PRIMARY KEY,note TEXT); INSERT INTO " + quoted + " VALUES(1,'hello'),(2,'world')"); err != nil {
		t.Fatal(err)
	}
	r := viewer.NewViewerMetadataRepository()
	data, err := r.ReadTableData(context.Background(), roles.Executor, "public", name, viewer.ViewerPagination{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 1 || !data.HasMore || !data.StableOrder || data.Rows[0][0] != int64(1) {
		t.Fatal("pagination/urutan tidak sesuai")
	}
	data, err = r.ReadTableData(context.Background(), roles.Executor, "public", name, viewer.ViewerPagination{Limit: 1, Offset: 1})
	if err != nil || len(data.Rows) != 1 || data.HasMore || data.Rows[0][0] != int64(2) {
		t.Fatal("halaman berikutnya salah", err)
	}
	if _, err = r.ReadTableData(context.Background(), roles.Executor, "public", name, viewer.ViewerPagination{Limit: 101}); !errors.Is(err, viewer.ErrInvalidViewerInput) {
		t.Fatal("limit tidak dibatasi")
	}
	var count int
	if roles.Executor.QueryRow("SELECT count(*) FROM "+quoted).Scan(&count) != nil || count != 2 {
		t.Fatal("viewer mengubah data atau quoting gagal")
	}
}
func TestMetadataAndDatabaseFailure(t *testing.T) {
	p := testutil.NewPostgres(t)
	roles := p.TargetRoles(t)
	if _, err := roles.Executor.Exec("CREATE TABLE items(id INT PRIMARY KEY); CREATE INDEX items_id_index ON items(id)"); err != nil {
		t.Fatal(err)
	}
	r := viewer.NewViewerMetadataRepository()
	meta, err := r.GetTableMetadata(context.Background(), roles.Executor, "public", "items")
	if err != nil || len(meta.Columns) != 1 || len(meta.Constraints) != 1 || len(meta.Indexes) != 2 {
		t.Fatal("metadata tidak lengkap", err)
	}
	roles.Executor.Close()
	if _, err = r.ListSchemas(context.Background(), roles.Executor); err == nil {
		t.Fatal("kegagalan database ditelan")
	}
}
