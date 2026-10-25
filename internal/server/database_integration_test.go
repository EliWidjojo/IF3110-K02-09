package server

import (
	"encoding/json"
 "net/url"
	"fmt"
	"strings"
	"testing"
)

func TestRealDatabaseIntegration(t *testing.T) {
	f := fixture(t)
	f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	status(t, f.execute(b, 1, `CREATE TABLE public.values_demo(id BIGINT PRIMARY KEY,amount NUMERIC,bin BYTEA,note TEXT,flag BOOLEAN,stamp TIMESTAMPTZ,document JSONB,CONSTRAINT amount_check CHECK(amount>0)); CREATE INDEX note_index ON public.values_demo(note)`), 200)
	status(t, f.execute(b, 2, `INSERT INTO public.values_demo VALUES(9007199254740993,12345678901234567890.123,decode('ff00','hex'),NULL,true,'2026-10-25 10:00:00+07','{"large":9007199254740993,"null":null}')`), 200)
	base := fmt.Sprintf("/api/viewer/databases/%d/schemas/public/tables/values_demo", f.databaseID)
	meta := b.request("GET", base, "", nil)
	status(t, meta, 200)
	if !strings.Contains(meta.Body.String(), "primary_key") || !strings.Contains(meta.Body.String(), "note_index") || !strings.Contains(meta.Body.String(), "amount_check") {
		t.Fatal("metadata constraint/index tidak lengkap")
	}
	w := b.request("GET", base+"/data?limit=1&offset=0", "", nil)
	status(t, w, 200)
	var data struct {
		Data struct {
			Rows   [][]any `json:"rows"`
			Stable bool    `json:"stable_order"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Data.Rows) != 1 || !data.Data.Stable {
		t.Fatal("data/pagination tidak valid")
	}
	row := data.Data.Rows[0]
	want := map[int]string{0: "9007199254740993", 1: "12345678901234567890.123", 2: "/wA="}
	for i, value := range want {
		typed, ok := row[i].(map[string]any)
		if !ok || typed["value"] != value {
			t.Fatal("nilai typed berubah", i)
		}
	}
	if row[3] != nil || row[4] != true {
		t.Fatal("NULL atau boolean berubah")
	}
	if !strings.Contains(w.Body.String(), `\"large\": 9007199254740993`) {
		t.Fatal("JSON berpresisi tinggi berubah")
	}
	status(t, b.request("GET", base+"/data?limit=-1", "", nil), 400)
	status(t, b.request("GET", base+"/data?limit=101", "", nil), 400)
	status(t, b.request("GET", base+"/data?offset=1", "", nil), 200)
	status(t, b.request("GET", fmt.Sprintf("/api/viewer/databases/%d/schemas/public/tables/missing/data", f.databaseID), "", nil), 404)
	status(t, f.execute(b, 3, `INSERT INTO values_demo(id) VALUES(1); INSERT INTO values_demo(id) VALUES(1)`), 422)
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM values_demo`).Scan(&count)
	if count != 1 {
		t.Fatal("rollback SQL gagal")
	}
	history := b.request("GET", fmt.Sprintf("/api/databases/%d/migrations/history", f.databaseID), "", nil)
	status(t, history, 200)
	if !strings.Contains(history.Body.String(), "FAILED") || !strings.Contains(history.Body.String(), "23505") {
		t.Fatal("history gagal tidak lengkap")
	}
	status(t, f.execute(b, 3, `UPDATE platform_migrations.history SET executed_by=999`), 422)
	status(t, f.execute(b, 3, `INSERT INTO values_demo(id,bin) VALUES(2,decode(repeat('ff',2097153),'hex'))`), 200)
	status(t, b.request("GET", base+"/data", "", nil), 413)
}
func TestQueryAnalysisPermissionsValidationAndPlans(t *testing.T) {
	f := fixture(t)
	group := f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	status(t, f.execute(b, 1, `CREATE TABLE public.analysis_items(id INT PRIMARY KEY,note TEXT); INSERT INTO public.analysis_items VALUES(1,'safe')`), 200)
	endpoint := fmt.Sprintf("/api/viewer/databases/%d/explain", f.databaseID)
	for _, analyze := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"query": "SELECT * FROM public.analysis_items WHERE id=1", "analyze": analyze})
		w := b.request("POST", endpoint, string(body), nil)
		status(t, w, 200)
		if !strings.Contains(w.Body.String(), "node_type") || !strings.Contains(w.Body.String(), "total_cost") {
			t.Fatal("plan tidak ditampilkan")
		}
		if analyze && !strings.Contains(w.Body.String(), "actual_rows") {
			t.Fatal("actual plan tidak tersedia")
		}
	}
	for _, tc := range []struct {
		query   string
		analyze bool
		code    int
	}{{"SELECT 1;SELECT 2", false, 400}, {"CREATE TABLE no(id INT)", false, 422}, {"SELECT pg_sleep(1)", true, 422}, {"DELETE FROM public.analysis_items", true, 422}, {"INSERT INTO public.analysis_items VALUES(2,'plan only')", false, 200}, {"SELECT * FROM public.missing", false, 422}} {
		body, _ := json.Marshal(map[string]any{"query": tc.query, "analyze": tc.analyze})
		status(t, b.request("POST", endpoint, string(body), nil), tc.code)
	}
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM analysis_items`).Scan(&count)
	if count != 1 {
		t.Fatal("analisis mengubah data")
	}
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), `{"access_level":"View Only"}`, nil), 200)
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	b.login("alice")
	status(t, b.request("POST", endpoint, `{"query":"SELECT 1","analyze":false}`, nil), 403)
}
func TestConnectionFailureInvalidationAndGroupLifecycle(t *testing.T) {
	f := fixture(t)
	group := f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	schemas := fmt.Sprintf("/api/viewer/databases/%d/schemas", f.databaseID)
	status(t, b.request("GET", schemas, "", nil), 200)
	path := fmt.Sprintf("/api/admin/databases/%d", f.databaseID)
	status(t, f.admin.request("PATCH", path, `{"port":1}`, nil), 200)
	status(t, b.request("GET", schemas, "", nil), 502)
	status(t, f.admin.request("PATCH", path, fmt.Sprintf(`{"port":%d}`, f.target.Port), nil), 200)
	status(t, b.request("GET", schemas, "", nil), 200)
	status(t, f.admin.request("GET", fmt.Sprintf("/api/admin/groups/%d/members", group), "", nil), 200)
	status(t, f.admin.request("GET", fmt.Sprintf("/api/admin/groups/%d/database-access", group), "", nil), 200)
	status(t, f.admin.request("PATCH", fmt.Sprintf("/api/admin/groups/%d", group), `{"name":"renamed-group"}`, nil), 200)
	status(t, f.admin.request("DELETE", fmt.Sprintf("/api/admin/groups/%d/members/2", group), "", nil), 200)
	status(t, b.request("GET", schemas, "", nil), 401)
	b.login("alice")
	status(t, b.request("GET", schemas, "", nil), 403)
	status(t, f.admin.request("DELETE", fmt.Sprintf("/api/admin/groups/%d", group), "", nil), 200)
	status(t, f.admin.request("DELETE", path, "", nil), 200)
	status(t, f.admin.request("GET", path, "", nil), 404)
}
func TestInternalDatabaseCannotBeManagedTarget(t *testing.T){
 f:=fixture(t);source,_:=url.Parse(f.internal.DSN);secret,_:=source.User.Password()
 body,_:=json.Marshal(map[string]any{"name":"internal-forbidden","driver":"postgres","host":f.internal.Host,"port":f.internal.Port,"database_name":f.internal.Name,"username":source.User.Username(),"password":secret,"audit_username":"not-used","audit_password":"","ssl_mode":"disable"})
 w:=f.admin.request("POST","/api/admin/databases",string(body),nil);status(t,w,201);f.databaseID=objectID(t,w);f.grant(t,2,"View Only");b:=&browser{t:t,handler:f.handler};b.login("alice");status(t,b.request("GET",fmt.Sprintf("/api/viewer/databases/%d/schemas",f.databaseID),"",nil),400)
}
