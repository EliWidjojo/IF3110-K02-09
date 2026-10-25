package server

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
)

// Run only on disposable targets: this test creates/drops tables and history rows.
func TestRealDatabaseIntegration(t *testing.T) {
	for _, tc := range []struct {
		dialect     database.Dialect
		env, schema string
	}{{database.Postgres, "TEST_POSTGRES_DSN", "public"}, {database.MySQL, "TEST_MYSQL_DSN", "platform_target"}, {database.MariaDB, "TEST_MARIADB_DSN", "platform_target"}} {
		t.Run(string(tc.dialect), func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skip("set " + tc.env + " to enable real database integration")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			target, err := database.Open(ctx, database.Config{ID: "target", Name: "Integration", Driver: tc.dialect, DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { target.DB.Close() })
			service := migration.NewMigrationServiceWithDialect(target.DB, tc.dialect)
			if err := service.InitMigrationTable(); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("security_test_%d", time.Now().UnixNano())
			quoted := tc.dialect.Quote(name)
			latest, err := service.GetLatestVersion()
			if err != nil {
				t.Fatal(err)
			}
			version := latest + 1
			t.Cleanup(func() {
				target.DB.Exec("DROP TABLE IF EXISTS " + quoted)
				target.DB.Exec("DELETE FROM schema_migrations WHERE query_sql LIKE "+tc.dialect.Bind(1), "%"+name+"%")
			})
			store, _ := auth.OpenStore("")
			id, err := store.CreateUser("alice", password, false)
			if err != nil {
				t.Fatal(err)
			}
			g, _ := store.CreateGroup("editors")
			if err := store.SetMembership(g, id, true); err != nil {
				t.Fatal(err)
			}
			if err := store.SetGrant(g, "target", access.Edit); err != nil {
				t.Fatal(err)
			}
			sessions, _ := auth.NewSessions(auth.DefaultSessionConfig())
			a, err := auth.NewHTTP(store, sessions, "http://localhost:8080", false)
			if err != nil {
				t.Fatal(err)
			}
			handler := New(a, map[string]*database.Target{"target": target})
			b := &browser{t: t, handler: handler}
			b.login("alice")
			ddl := fmt.Sprintf(`{"version":%d,"query_sql":"CREATE TABLE %s (id INT PRIMARY KEY, name VARCHAR(80))","non_atomic_ddl":%t}`, version, name, tc.dialect.IsMySQL())
			status(t, b.request("POST", "/api/databases/target/migrations/execute", ddl, nil), 200)
			body := fmt.Sprintf(`{"version":%d,"query_sql":"INSERT INTO %s VALUES (1, 'first'); INSERT INTO %s VALUES (2, 'second')"}`, version+1, name, name)
			status(t, b.request("POST", "/api/databases/target/migrations/execute", body, nil), 200)
			body = fmt.Sprintf(`{"version":%d,"query_sql":"INSERT INTO %s VALUES (3, 'rollback'); INSERT INTO %s VALUES (1, 'duplicate')"}`, version+2, name, name)
			status(t, b.request("POST", "/api/databases/target/migrations/execute", body, nil), 400)
			var count int
			if err := target.DB.QueryRow("SELECT COUNT(*) FROM " + quoted).Scan(&count); err != nil || count != 2 {
				t.Fatalf("rollback failed: count=%d err=%v", count, err)
			}
			status(t, b.request("GET", "/api/databases/target/schema", "", nil), 200)
			status(t, b.request("GET", "/api/databases/target/data?schema="+tc.schema+"&table="+name, "", nil), 200)
			status(t, b.request("GET", "/api/databases/target/migrations/history", "", nil), 200)
			if err := store.SetGrant(g, "target", access.View); err != nil {
				t.Fatal(err)
			}
			status(t, b.request("POST", "/api/databases/target/migrations/execute", body, nil), 401)
			b.login("alice")
			status(t, b.request("POST", "/api/databases/target/migrations/execute", body, nil), 403)
		})
	}
}
