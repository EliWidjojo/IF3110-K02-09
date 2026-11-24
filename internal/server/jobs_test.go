package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/platform"
)

func TestJobSubmissionAndDetailIntegration(t *testing.T) {
	f := fixture(t)
	path := fmt.Sprintf("/api/databases/%d/migrations/jobs", f.databaseID)
	body := `{"version":1,"query_sql":"CREATE TABLE job_must_not_execute(id INT)"}`
	headers := map[string]string{"Idempotency-Key": "submission-1"}
	anon := &browser{t: t, handler: f.handler}
	status(t, anon.request("POST", path, body, headers), 401)
	status(t, f.admin.request("POST", path, body, headers), 403)
	group := f.grant(t, 2, "View Only")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	status(t, b.request("POST", path, body, headers), 403)
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), `{"access_level":"View + Edit"}`, nil), 200)
	w := b.request("POST", path, body, headers)
	status(t, w, 202)
	var result struct {
		Data migration.Job `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	j := result.Data
	if j.ID <= 0 || j.Status != migration.JobQueued || j.SubmittedBy != 2 || j.WorkerID != nil || j.StartedAt != nil || j.FinishedAt != nil || j.CreatedAt.IsZero() || j.StatusURL != w.Header().Get("Location") {
		t.Fatalf("invalid job: %+v", j)
	}
	w = b.request("POST", path, body, headers)
	status(t, w, 202)
	if objectID(t, w) != int(j.ID) {
		t.Fatal("retry created another job")
	}
	status(t, b.request("POST", path, strings.Replace(body, `"version":1`, `"version":2`, 1), headers), 409)
	status(t, b.request("POST", path, body, map[string]string{"Idempotency-Key": "a", "X-CSRF-Token": "wrong"}), 403)
	status(t, b.request("POST", path, `{"version":1,"query_sql":"BEGIN"}`, map[string]string{"Idempotency-Key": "invalid"}), 400)
	status(t, b.request("GET", j.StatusURL, "", nil), 200)
	status(t, b.request("GET", path+"/999999", "", nil), 404)
	status(t, f.admin.request("GET", j.StatusURL, "", nil), 403)
	status(t, anon.request("GET", j.StatusURL, "", nil), 401)
	var absent bool
	if err := f.target.DB.QueryRow(`SELECT to_regclass('public.job_must_not_execute') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatal("submission executed target SQL", err)
	}
	var attempts int
	if err := f.roles.Audit.QueryRow(`SELECT count(*) FROM platform_migrations.attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("submission created target attempt", err)
	}
	// Restart/schema bootstrap harus mempertahankan job dan idempotency key.
	if err := platform.EnsureSchema(context.Background(), f.internal.DB); err != nil {
		t.Fatal(err)
	}
	restarted := migration.NewJobService(f.internal.DB, f.module.Access, f.module.Pools, f.auth)
	mux := http.NewServeMux()
	migration.RegisterJobRoutes(mux, migration.NewJobHandler(restarted, f.auth))
	other := &browser{t: t, handler: f.auth.Require(mux), cookie: b.cookie, csrf: b.csrf}
	w = other.request("GET", j.StatusURL, "", nil)
	status(t, w, 200)
	if objectID(t, w) != int(j.ID) {
		t.Fatal("job lost after restart")
	}
	status(t, other.request("POST", path, body, headers), 202)
	if _, err := f.internal.DB.Exec(`UPDATE platform.migration_jobs SET query_sql='DROP TABLE changed' WHERE id=$1`, j.ID); err == nil {
		t.Fatal("snapshot is mutable")
	}
	// Detail membaca perkembangan yang kelak ditulis worker, bukan status hardcoded.
	if _, err := f.internal.DB.Exec(`UPDATE platform.migration_jobs SET status='RUNNING',worker_id='worker-1',started_at=now() WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	w = b.request("GET", j.StatusURL, "", nil)
	status(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Status != migration.JobRunning || result.Data.WorkerID == nil || *result.Data.WorkerID != "worker-1" || result.Data.StartedAt == nil {
		t.Fatal("worker state missing")
	}
	// ID valid pada database lain tidak boleh membuka detail job ini.
	var secondID int
	if err := f.internal.DB.QueryRow(`INSERT INTO platform.database_connections(name,host,port,database_name,username,ssl_mode,encrypted_credentials)
	 SELECT 'second-target',host,port,database_name,username,ssl_mode,encrypted_credentials FROM platform.database_connections WHERE id=$1 RETURNING id`, f.databaseID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.internal.DB.Exec(`INSERT INTO platform.group_database_access(group_id,database_id,access_level) VALUES ($1,$2,'View Only')`, group, secondID); err != nil {
		t.Fatal(err)
	}
	status(t, b.request("GET", fmt.Sprintf("/api/databases/%d/migrations/jobs/%d", secondID, j.ID), "", nil), 404)
	// Penurunan izin mengharuskan login ulang; View Only tetap bisa membaca detail.
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), `{"access_level":"View Only"}`, nil), 200)
	b.login("alice")
	status(t, b.request("GET", j.StatusURL, "", nil), 200)
	status(t, b.request("POST", path, body, headers), 403)
	status(t, f.admin.request("DELETE", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), "", nil), 200)
	b.login("alice")
	status(t, b.request("GET", j.StatusURL, "", nil), 403)
}

func TestConcurrentJobIdempotencyIntegration(t *testing.T) {
	f := fixture(t)
	f.grant(t, 2, "View + Edit")
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	path := fmt.Sprintf("/api/databases/%d/migrations/jobs", f.databaseID)
	const count = 8
	results := make(chan *httptest.ResponseRecorder, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", path, strings.NewReader(`{"version":1,"query_sql":"CREATE TABLE parallel_job(id INT)"}`))
			r.RemoteAddr = "127.0.0.1:50000"
			r.AddCookie(b.cookie)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", b.csrf)
			r.Header.Set("Idempotency-Key", "parallel")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			results <- w
		}()
	}
	wg.Wait()
	close(results)
	id := 0
	for w := range results {
		status(t, w, 202)
		got := objectID(t, w)
		if id != 0 && got != id {
			t.Fatal("parallel retry created duplicate")
		}
		id = got
	}
	var stored int
	if err := f.internal.DB.QueryRow(`SELECT count(*) FROM platform.migration_jobs`).Scan(&stored); err != nil || stored != 1 {
		t.Fatal("unexpected stored jobs", stored, err)
	}
}
