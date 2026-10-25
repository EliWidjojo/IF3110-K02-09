package database

import (
	"IF3110-K02-09/internal/testutil"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestTypedConnectorUsesConfiguredTarget(t *testing.T) {
	p := testutil.NewPostgres(t)
	r := p.TargetRoles(t)
	c, err := newTargetPostgresConnector(DatabaseConnectionCredentials{Configuration: DatabaseConnection{ID: 1, Host: p.Host, Port: p.Port, DatabaseName: p.Name, Username: r.ExecutorName, SSLMode: "disable", Driver: Postgres}, Password: r.Password}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer db.Close()
	if err = db.PingContext(context.Background()); err != nil {
		for {
			wrapped, ok := err.(*TargetConnectionError)
			if !ok {
				break
			}
			err = wrapped.cause
		}
		message := strings.ReplaceAll(err.Error(), r.Password, "[redacted]")
		message = strings.ReplaceAll(message, r.AuditPassword, "[redacted]")
		t.Fatal(message)
	}
}
