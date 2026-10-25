package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/lib/pq"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type Postgres struct {
	DB    *sql.DB
	DSN   string
	Name  string
	Host  string
	Port  int
	admin *sql.DB
	roles []string
}
type Roles struct {
	Executor      *sql.DB
	Audit         *sql.DB
	ExecutorName  string
	AuditName     string
	Password      string
	AuditPassword string
}

func token() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// Helper hanya membuat database baru pada cluster localhost yang secara eksplisit ditandai untuk pengujian.
func NewPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("TEST_PLATFORM_DSN")
	if dsn == "" {
		t.Skip("TEST_PLATFORM_DSN belum diatur ke cluster disposable")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "atlas_test") {
		t.Fatal("DSN test harus menunjuk database atlas_test* di localhost")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("database test tidak dapat dibuka")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if admin.PingContext(ctx) != nil {
		admin.Close()
		t.Fatal("cluster test tidak tersedia")
	}
	name := "atlas_test_" + token()
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)); err != nil {
		admin.Close()
		t.Fatal("database disposable tidak dapat dibuat")
	}
	v := *u
	v.Path = "/" + name
	db, err := sql.Open("postgres", v.String())
	if err != nil {
		t.Fatal("koneksi database disposable gagal")
	}
	p := &Postgres{DB: db, DSN: v.String(), Name: name, Host: u.Hostname(), Port: 5432, admin: admin}
	if u.Port() != "" {
		if _, err = fmt.Sscan(u.Port(), &p.Port); err != nil {
			t.Fatal("port test tidak valid")
		}
	}
	t.Cleanup(func() {
		db.Close()
		_, e := admin.Exec("DROP DATABASE " + pq.QuoteIdentifier(name) + " WITH (FORCE)")
		if e != nil {
			t.Error("database test tidak dapat dibersihkan")
		}
		for _, role := range p.roles {
			if _, e = admin.Exec("DROP ROLE " + pq.QuoteIdentifier(role)); e != nil {
				t.Error("role test tidak dapat dibersihkan")
			}
		}
		admin.Close()
	})
	return p
}
func (p *Postgres) TargetRoles(t *testing.T) Roles {
	t.Helper()
	r := Roles{ExecutorName: "atlas_exec_" + token(), AuditName: "atlas_audit_" + token(), Password: token() + token(), AuditPassword: token() + token()}
	for _, entry := range []struct{ name, password string }{{r.ExecutorName, r.Password}, {r.AuditName, r.AuditPassword}} {
		if _, err := p.admin.Exec("CREATE ROLE " + pq.QuoteIdentifier(entry.name) + " LOGIN PASSWORD " + pq.QuoteLiteral(entry.password)); err != nil {
			t.Fatal("role target tidak dapat dibuat")
		}
		p.roles = append(p.roles, entry.name)
	}
	if _, err := p.DB.Exec("ALTER SCHEMA public OWNER TO " + pq.QuoteIdentifier(r.ExecutorName)); err != nil {
		t.Fatal("schema target tidak dapat disiapkan")
	}
	if _, err := p.DB.Exec("GRANT CREATE ON DATABASE " + pq.QuoteIdentifier(p.Name) + " TO " + pq.QuoteIdentifier(r.AuditName)); err != nil {
		t.Fatal("privilege pencatat tidak dapat disiapkan")
	}
	u, _ := url.Parse(p.DSN)
	u.User = url.UserPassword(r.ExecutorName, r.Password)
	r.Executor, _ = sql.Open("postgres", u.String())
	u.User = url.UserPassword(r.AuditName, r.AuditPassword)
	r.Audit, _ = sql.Open("postgres", u.String())
	t.Cleanup(func() { r.Executor.Close(); r.Audit.Close() })
	return r
}
