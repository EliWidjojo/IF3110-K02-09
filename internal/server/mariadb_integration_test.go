package server

import (
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/testutil"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMariaDBCompleteFlow(t *testing.T) {
	f := fixture(t)
	m := testutil.NewMySQL(t)
	if err := migration.SetupMySQLHistory(context.Background(), m.Audit, m.Name, m.ExecutorName+"@localhost"); err != nil {
		t.Fatal("setup history MariaDB", err)
	}
	config := map[string]any{"name": "mariadb", "driver": "mariadb", "host": m.Host, "port": m.Port, "database_name": m.Name, "username": m.ExecutorName, "password": m.Password, "audit_username": m.AuditName, "audit_password": m.AuditPassword, "ssl_mode": "disable"}
	body, _ := json.Marshal(config)
	w := f.admin.request("POST", "/api/admin/databases", string(body), nil)
	status(t, w, 201)
	f.databaseID = objectID(t, w)
	group := f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	execute := func(version int, sql string, ddl bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"version": version, "query_sql": sql, "non_atomic_ddl": ddl})
		return b.request("POST", fmt.Sprintf("/api/databases/%d/migrations/execute", f.databaseID), string(body), nil)
	}
	status(t, execute(1, `CREATE TABLE items(id BIGINT PRIMARY KEY,n DECIMAL(30,3),b BLOB,note TEXT) ENGINE=InnoDB`, false), 400)
	status(t, execute(1, `CREATE TABLE items(id BIGINT PRIMARY KEY,n DECIMAL(30,3),b BLOB,note TEXT) ENGINE=InnoDB`, true), 200)
	status(t, execute(2, `INSERT INTO items VALUES(9007199254740993,12345678901234567890.123,UNHEX('ff00'),NULL)`, false), 200)
	base := fmt.Sprintf("/api/viewer/databases/%d/schemas/%s/tables/items", f.databaseID, m.Name)
	for _, path := range []string{fmt.Sprintf("/api/viewer/databases/%d/schemas", f.databaseID), fmt.Sprintf("/api/viewer/databases/%d/schemas/%s/tables", f.databaseID, m.Name), base} {
		status(t, b.request("GET", path, "", nil), 200)
	}
	w = b.request("GET", base+"/data", "", nil)
	status(t, w, 200)
	for _, want := range []string{"9007199254740993", "12345678901234567890.123", "/wA="} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatal("serialisasi MariaDB berubah")
		}
	}
	status(t, execute(3, `INSERT INTO items(id) VALUES(1); INSERT INTO items(id) VALUES(1)`, false), 422)
	var count int
	m.Executor.QueryRow(`SELECT count(*) FROM items`).Scan(&count)
	if count != 1 {
		t.Fatal("rollback MariaDB gagal")
	}
	schema := "`" + m.History + "`"
	status(t, execute(3, "DELETE FROM "+schema+".attempts", false), 422)
	history := b.request("GET", fmt.Sprintf("/api/databases/%d/migrations/history", f.databaseID), "", nil)
	status(t, history, 200)
	if !strings.Contains(history.Body.String(), "FAILED") {
		t.Fatal("history kegagalan MariaDB hilang")
	}
	status(t, execute(3, `CREATE TABLE non_atomic(id INT) ENGINE=MyISAM`, true), 200)
	status(t, execute(4, `INSERT INTO non_atomic VALUES(1)`, false), 422)
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), `{"access_level":"View Only"}`, nil), 200)
	b.login("alice")
	status(t, execute(4, "DELETE FROM items", false), 403)
	status(t, b.request("GET", base+"/data", "", nil), 200)
}
