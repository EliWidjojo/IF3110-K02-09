package testutil

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"github.com/go-sql-driver/mysql"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

type MySQL struct {
	DB                                                        *sql.DB
	Executor                                                  *sql.DB
	Audit                                                     *sql.DB
	Name, Host                                                string
	Port                                                      int
	ExecutorName, AuditName, Password, AuditPassword, History string
	AccountHost                                               string
}

func NewMySQL(t *testing.T) *MySQL {
	t.Helper()
	raw := os.Getenv("TEST_MARIADB_ADMIN_DSN")
	if raw == "" {
		t.Skip("TEST_MARIADB_ADMIN_DSN belum diatur ke MariaDB disposable")
	}
	cfg, err := mysql.ParseDSN(raw)
	if err != nil || cfg.Net != "tcp" || !strings.HasPrefix(cfg.DBName, "atlas_test") {
		t.Fatal("DSN MariaDB test harus menuju atlas_test* di localhost")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		t.Fatal("host MariaDB test harus localhost")
	}
	admin, err := sql.Open("mysql", raw)
	if err != nil {
		t.Fatal("MariaDB test tidak tersedia")
	}
	if admin.Ping() != nil {
		admin.Close()
		t.Fatal("MariaDB test tidak dapat dihubungi")
	}
	p := &MySQL{Name: "atlas_test_" + token(), Host: host, ExecutorName: "ae_" + token(), AuditName: "aa_" + token(), Password: token() + token(), AuditPassword: token() + token()}
	// Docker NAT can make a localhost DSN arrive from a bridge address.
	if err := admin.QueryRow(`SELECT SUBSTRING_INDEX(USER(),'@',-1)`).Scan(&p.AccountHost); err != nil || p.AccountHost == "" {
		admin.Close()
		t.Fatal("alamat client MariaDB test tidak dapat ditentukan")
	}
	p.Port, _ = strconv.Atoi(port)
	sum := sha256.Sum256([]byte(p.Name))
	p.History = "atlas_history_" + hex.EncodeToString(sum[:8])
	quote := func(x string) string { return "`" + strings.ReplaceAll(x, "`", "``") + "`" }
	literal := func(x string) string { return "'" + strings.ReplaceAll(x, "'", "''") + "'" }
	account := func(name string) string { return literal(name) + "@" + literal(p.AccountHost) }
	t.Cleanup(func() {
		if p.Executor != nil {
			p.Executor.Close()
		}
		if p.Audit != nil {
			p.Audit.Close()
		}
		if p.DB != nil {
			p.DB.Close()
		}
		for _, q := range []string{"DROP DATABASE IF EXISTS " + quote(p.Name), "DROP DATABASE IF EXISTS " + quote(p.History), "DROP USER IF EXISTS " + account(p.ExecutorName), "DROP USER IF EXISTS " + account(p.AuditName)} {
			if _, e := admin.Exec(q); e != nil {
				t.Error("pembersihan MariaDB test gagal")
			}
		}
		admin.Close()
	})
	queries := []string{"CREATE DATABASE " + quote(p.Name), "CREATE DATABASE " + quote(p.History), "CREATE USER " + account(p.ExecutorName) + " IDENTIFIED BY " + literal(p.Password), "CREATE USER " + account(p.AuditName) + " IDENTIFIED BY " + literal(p.AuditPassword), "GRANT ALL ON " + quote(p.Name) + ".* TO " + account(p.ExecutorName), "GRANT ALL ON " + quote(p.History) + ".* TO " + account(p.AuditName) + " WITH GRANT OPTION"}
	for _, q := range queries {
		if _, err = admin.Exec(q); err != nil {
			t.Fatal("setup privilege MariaDB test gagal")
		}
	}
	cfg.DBName = p.Name
	cfg.ParseTime = true
	p.DB, _ = sql.Open("mysql", cfg.FormatDSN())
	cfg.User = p.ExecutorName
	cfg.Passwd = p.Password
	p.Executor, _ = sql.Open("mysql", cfg.FormatDSN())
	cfg.User = p.AuditName
	cfg.Passwd = p.AuditPassword
	cfg.DBName = p.History
	p.Audit, _ = sql.Open("mysql", cfg.FormatDSN())
	return p
}
