package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
)

func TestWorkerAdmissionIntegration(t *testing.T) {
	f := fixture(t)
	edit := f.grant(t, 2, "View + Edit")
	view := f.grant(t, 1, "View Only")
	alice := &browser{t: t, handler: f.handler}
	alice.login("alice")
	for _, pair := range [][2]int{{edit, 2}, {view, 1}} {
		status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/leader", pair[0]), fmt.Sprintf(`{"user_id":%d}`, pair[1]), nil), 204)
	}
	base := fmt.Sprintf("/api/databases/%d/migrations/jobs", f.databaseID)
	submit := func(key string) int {
		w := alice.request("POST", base, fmt.Sprintf(`{"version":%d,"query_sql":"CREATE TABLE preflight_%s(id int)"}`, map[string]int{"preflight": 1, "lease": 2, "changed": 3, "revoked": 4}[key], key), map[string]string{"Idempotency-Key": key})
		status(t, w, 202)
		return objectID(t, w)
	}
	approval := func(id, group int) string { return fmt.Sprintf("%s/%d/approvals/%d", base, id, group) }
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	makeWorker := func(name string) *migration.Worker {
		w, err := migration.NewWorker(f.internal.DB, database.NewDatabaseConnectionRepository(f.internal.DB, cipher), name)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	one, two := makeWorker("one"), makeWorker("two")
	ctx := context.Background()
	id := submit("preflight")
	status(t, alice.request("PUT", approval(id, view), "", nil), 403)
	status(t, alice.request("PUT", approval(id, edit), "", nil), 204)
	if worked, err := one.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("missing viewer approval: worked=%v err=%v", worked, err)
	}
	status(t, f.admin.request("PUT", approval(id, view), "", nil), 204)
	var wg sync.WaitGroup
	worked := make(chan bool, 2)
	for _, w := range []*migration.Worker{one, two} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := w.ProcessNext(ctx)
			if err != nil {
				t.Error(err)
			}
			worked <- ok
		}()
	}
	wg.Wait()
	close(worked)
	count := 0
	for ok := range worked {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d workers took one job", count)
	}
	detail := func(id int) migration.Job {
		w := alice.request("GET", fmt.Sprintf("%s/%d", base, id), "", nil)
		status(t, w, 200)
		var p struct{ Data migration.Job }
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p.Data
	}
	if j := detail(id); j.Status != migration.JobSuccess || j.WorkerID == nil || j.HeartbeatAt == nil || j.LeaseUntil != nil {
		t.Fatalf("preflight: %+v", j)
	}
	var absent bool
	if err := f.target.DB.QueryRow("SELECT to_regclass('public.preflight_preflight') IS NULL").Scan(&absent); err != nil || absent {
		t.Fatal("approved migration did not execute", err)
	}
	// Model a target commit followed by a crash before the platform recorded SUCCESS.
	if _, err := f.internal.DB.Exec(`UPDATE platform.migration_jobs SET status='RUNNING', lease_token='expired',
		lease_until=clock_timestamp()-interval '1 second',finished_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := two.ProcessNext(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	var attempts int
	if err := f.roles.Audit.QueryRow("SELECT count(*) FROM platform_migrations.attempts WHERE version=1").Scan(&attempts); err != nil || attempts != 1 || detail(id).Status != migration.JobSuccess {
		t.Fatal("commit was not reconciled", attempts, err)
	}

	// Hold the same physical lock as synchronous migration, observe renewal,
	// then expire the lease to model a lost worker before it records READY.
	id = submit("lease")
	status(t, alice.request("PUT", approval(id, edit), "", nil), 204)
	status(t, f.admin.request("PUT", approval(id, view), "", nil), 204)
	lock, err := f.roles.Audit.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_lock(311009004)`); err != nil {
		t.Fatal(err)
	}
	defer lock.ExecContext(ctx, `SELECT pg_advisory_unlock(311009004)`)
	first := make(chan error, 1)
	go func() { _, err := one.ProcessNext(ctx); first <- err }()
	waitFor := func(condition string) {
		t.Helper()
		deadline := time.Now().Add(11 * time.Second)
		for time.Now().Before(deadline) {
			var ok bool
			if err := f.internal.DB.QueryRow("SELECT COALESCE(("+condition+"),false) FROM platform.migration_jobs WHERE id=$1", id).Scan(&ok); err != nil {
				t.Fatal(err)
			}
			if ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("worker condition timed out", condition)
	}
	waitFor("status='RUNNING' AND heartbeat_at > started_at + interval '1 second'")
	if _, err = f.internal.DB.Exec(`UPDATE platform.migration_jobs SET lease_until=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { _, err := two.ProcessNext(ctx); second <- err }()
	waitFor("worker_id='two'")
	if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_unlock(311009004)`); err != nil {
		t.Fatal(err)
	}
	if err = <-first; !errors.Is(err, migration.ErrLeaseLost) {
		t.Fatalf("expired claim: %v", err)
	}
	if err = <-second; err != nil {
		t.Fatal(err)
	}
	if j := detail(id); j.Status != migration.JobSuccess || j.WorkerID == nil || *j.WorkerID != "two" {
		t.Fatalf("reclaimed: %+v", j)
	}

	id = submit("changed")
	status(t, alice.request("PUT", approval(id, edit), "", nil), 204)
	status(t, f.admin.request("PUT", approval(id, view), "", nil), 204)
	if _, err := f.internal.DB.Exec(`UPDATE platform.database_connections SET database_name='changed' WHERE id=$1`, f.databaseID); err != nil {
		t.Fatal(err)
	}
	if ok, err := one.ProcessNext(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if j := detail(id); j.Status != migration.JobFailed || j.ErrorCode != "target_changed" {
		t.Fatalf("retargeted: %+v", j)
	}

	id = submit("revoked")
	status(t, alice.request("PUT", approval(id, edit), "", nil), 204)
	status(t, f.admin.request("PUT", approval(id, view), "", nil), 204)
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", edit, f.databaseID), `{"access_level":"View Only"}`, nil), 200)
	if ok, err := two.ProcessNext(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	alice.login("alice")
	if j := detail(id); j.Status != migration.JobFailed || j.ErrorCode != "permission_revoked" {
		t.Fatalf("revoked: %+v", j)
	}
}
