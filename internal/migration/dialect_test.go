package migration_test

import (
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"context"
	"testing"
)

func TestMySQLTransactionalMigration(t *testing.T) {
	for _, d := range []database.Dialect{database.MySQL, database.MariaDB} {
		statements, err := database.Statements("INSERT INTO items VALUES(1); UPDATE items SET id=2", d, false)
		if err != nil || len(statements) != 2 {
			t.Fatal(d, err)
		}
	}
}
func TestMySQLDDLExplicitAndFailureHistory(t *testing.T) {
	for _, d := range []database.Dialect{database.MySQL, database.MariaDB} {
		if _, err := database.Statements("CREATE TABLE items(id INT)", d, false); err == nil {
			t.Fatal("DDL tanpa pilihan eksplisit diterima")
		}
		if _, err := database.Statements("CREATE TABLE items(id INT)", d, true); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Statements("CREATE TABLE x(id INT);DROP TABLE x", d, true); err == nil {
			t.Fatal("batch DDL non-atomic diterima")
		}
	}
}
func TestVersionQueryFailureStopsMigration(t *testing.T) {
	f := newFixture(t)
	f.roles.Audit.Exec(`DROP TABLE platform_migrations.attempts CASCADE`)
	if _, err := f.run(1, "CREATE TABLE blocked(id INT)"); err == nil {
		t.Fatal("history tidak tersedia tetapi SQL berjalan")
	}
	var exists bool
	f.roles.Executor.QueryRow(`SELECT to_regclass('public.blocked') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("SQL tetap dijalankan")
	}
}
func TestMySQLNonTransactionalEngineRejected(t *testing.T) {
	if _, err := database.Statements("CREATE PROCEDURE unsafe() COMMIT", database.MySQL, true); err == nil {
		t.Fatal("stored program diterima")
	}
}
func TestSchemaDiffRequiresReviewAndQuotesIdentifiers(t *testing.T) {
	f := newFixture(t)
	f.run(1, `CREATE TABLE public.items(id INT NOT NULL, old TEXT)`)
	result, err := f.engine.Diff(context.Background(), f.id, migration.DesiredSchema{Schema: "public", Tables: []migration.DesiredTable{{Name: "items", Columns: []migration.DesiredColumn{{Name: "id", Type: "bigint", Nullable: false}, {Name: `safe"name`, Type: "text", Nullable: true}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.SQL != `ALTER TABLE "public"."items" ADD COLUMN "safe""name" text;` {
		t.Fatal(result.SQL)
	}
	reviews := 0
	for _, d := range result.Differences {
		if d.RequiresDecision {
			reviews++
		}
	}
	if reviews != 2 {
		t.Fatal("perubahan berisiko ditebak", result)
	}
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='items'`).Scan(&count)
	if count != 2 {
		t.Fatal("diff mengeksekusi SQL otomatis")
	}
}
