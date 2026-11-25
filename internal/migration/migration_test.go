package migration_test

import (
	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/platform"
	"IF3110-K02-09/internal/testutil"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"
	"time"
)

type fixture struct {
	engine           *migration.Engine
	pools            *database.TargetConnectionManager
	repo             *database.DatabaseConnectionRepository
	internal, target *testutil.Postgres
	roles            testutil.Roles
	id               int
	config           database.CreateDatabaseConnectionParams
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	internal, target := testutil.NewPostgres(t), testutil.NewPostgres(t)
	if err := platform.EnsureSchema(ctx, internal.DB); err != nil {
		t.Fatal(err)
	}
	roles := target.TargetRoles(t)
	if err := migration.SetupPostgresHistory(ctx, roles.Audit, roles.ExecutorName); err != nil {
		t.Fatal("setup history", err)
	}
	store := auth.NewPostgresStore(internal.DB)
	if store.Bootstrap(ctx, "admin", "test-password-long-enough") != nil {
		t.Fatal("bootstrap gagal")
	}
	key := make([]byte, 32)
	rand.Read(key)
	t.Setenv(database.DatabaseCredentialsKeyEnv, base64.StdEncoding.EncodeToString(key))
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	repo := database.NewDatabaseConnectionRepository(internal.DB, cipher)
	pools, err := database.NewTargetConnectionManager(repo, database.TargetPoolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	config := database.CreateDatabaseConnectionParams{Name: "target", Driver: database.Postgres, Host: target.Host, Port: target.Port, DatabaseName: target.Name, Username: roles.ExecutorName, Password: roles.Password, AuditUsername: roles.AuditName, AuditPassword: roles.AuditPassword, SSLMode: "disable"}
	c, err := repo.Create(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = internal.DB.Exec(`INSERT INTO platform.groups(name) VALUES('editors'); INSERT INTO platform.group_memberships(user_id,group_id) VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = internal.DB.Exec(`INSERT INTO platform.group_database_access(group_id,database_id,access_level) VALUES(1,$1,'View + Edit')`, c.ID); err != nil {
		t.Fatal(err)
	}
	identity := platform.VerifiedIdentityFunc(func(context.Context) (int, error) { return 1, nil })
	a := access.NewEffectiveAccessService(access.NewEffectiveAccessRepository(internal.DB), identity)
	return &fixture{migration.NewEngine(a, pools, identity), pools, repo, internal, target, roles, c.ID, config}
}
func (f *fixture) run(version int, sql string) (migration.ExecutionResult, error) {
	return f.engine.Run(context.Background(), f.id, migration.MigrationRequest{Version: version, QuerySQL: sql})
}
func (f *fixture) history(t *testing.T) []migration.Attempt {
	t.Helper()
	rows, _, err := f.engine.History(context.Background(), f.id, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func TestExecuteMigration_Success(t *testing.T) {
	f := newFixture(t)
	result, err := f.run(1, "CREATE TABLE public.items(id INT PRIMARY KEY, name TEXT)")
	if err != nil || result.Status != "SUCCESS" || !result.Atomic {
		t.Fatal(result, err)
	}
	h := f.history(t)
	if len(h) != 1 || h[0].Version != 1 || h[0].ExecutedBy != 1 || h[0].QuerySQL != "CREATE TABLE public.items(id INT PRIMARY KEY, name TEXT)" || h[0].FinishedAt == nil {
		t.Fatal("metadata history tidak lengkap")
	}
}
func TestExecuteMigration_SQLError_Rollback(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(1, "CREATE TABLE items(id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	r, err := f.run(2, "INSERT INTO items VALUES(1); INSERT INTO missing_table VALUES(1)")
	if !errors.Is(err, migration.ErrMigrationFailed) || r.Status != "FAILED" {
		t.Fatal(r, err)
	}
	var count int
	if f.roles.Executor.QueryRow("SELECT count(*) FROM items").Scan(&count) != nil || count != 0 {
		t.Fatal("perubahan gagal tidak di-rollback")
	}
	if len(f.history(t)) != 2 || f.history(t)[0].Status != "FAILED" || f.history(t)[0].SQLState != "42P01" {
		t.Fatal("kegagalan tidak dicatat")
	}
	if _, err = f.run(2, "INSERT INTO items VALUES(1)"); err != nil {
		t.Fatal("versi gagal tidak dapat dipakai ulang", err)
	}
}
func TestExecuteMigration_AccessDenied(t *testing.T) {
	f := newFixture(t)
	f.internal.DB.Exec(`UPDATE platform.group_database_access SET access_level='View Only'`)
	if _, err := f.run(1, "CREATE TABLE forbidden(id INT)"); !errors.Is(err, access.ErrDatabaseAccessDenied) {
		t.Fatal("view only dapat menulis", err)
	}
	if len(f.history(t)) != 0 {
		t.Fatal("request tanpa izin menyentuh history")
	}
}
func TestGetMigrationHistory_ViewOnly_Success(t *testing.T) {
	f := newFixture(t)
	f.run(1, "CREATE TABLE items(id INT)")
	f.internal.DB.Exec(`UPDATE platform.group_database_access SET access_level='View Only'`)
	if len(f.history(t)) != 1 {
		t.Fatal("history view only gagal")
	}
}
func TestProtectedHistoryAndForgedCapability(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(1, "CREATE TABLE items(id INT)"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DELETE FROM platform_migrations.attempts", "UPDATE platform_migrations.history SET executed_by=999", "DROP SCHEMA platform_migrations CASCADE", "ALTER TABLE platform_migrations.attempts ADD COLUMN forged INT"} {
		if _, err := f.run(2, q); !errors.Is(err, migration.ErrMigrationFailed) {
			t.Fatalf("history dapat dimodifikasi: %v", err)
		}
	}
	if _, err := f.roles.Executor.Exec(`SELECT platform_migrations.complete_attempt('forged')`); err == nil {
		t.Fatal("capability palsu diterima")
	}
	h := f.history(t)
	success := 0
	for _, a := range h {
		if a.Status == "SUCCESS" {
			success++
			if a.ExecutedBy != 1 || a.Version != 1 {
				t.Fatal("history sukses berubah")
			}
		}
	}
	if success != 1 {
		t.Fatal("history sukses hilang")
	}
	if _, err := f.roles.Executor.Exec(`SELECT capability_hash FROM platform_migrations.attempts`); err == nil {
		t.Fatal("executor dapat membaca capability")
	}
}
func TestHistoryFailureRollsBackTarget(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(1, "CREATE TABLE items(id INT)"); err != nil {
		t.Fatal(err)
	}
	_, err := f.roles.Audit.Exec(`CREATE OR REPLACE FUNCTION platform_migrations.complete_attempt(capability TEXT) RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.run(2, "INSERT INTO items VALUES(1)"); !errors.Is(err, migration.ErrMigrationFailed) {
		t.Fatal(err)
	}
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM items`).Scan(&count)
	if count != 0 {
		t.Fatal("pencatatan gagal meninggalkan perubahan")
	}
	if f.history(t)[0].ErrorCode != "history_failed" {
		t.Fatal("kegagalan history tidak tercatat")
	}
}
func TestPhysicalDatabaseLockAcrossAliases(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(1, "CREATE TABLE items(id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	cfg := f.config
	cfg.Name = "alias"
	alias, err := f.repo.Create(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.internal.DB.Exec(`INSERT INTO platform.group_database_access(group_id,database_id,access_level) VALUES(1,$1,'View + Edit')`, alias.ID); err != nil {
		t.Fatal(err)
	}
	identity := platform.VerifiedIdentityFunc(func(context.Context) (int, error) { return 1, nil })
	a := access.NewEffectiveAccessService(access.NewEffectiveAccessRepository(f.internal.DB), identity)
	secondPools, err := database.NewTargetConnectionManager(database.NewDatabaseConnectionRepository(f.internal.DB, mustCipher(t)), database.TargetPoolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer secondPools.Close()
	second := migration.NewEngine(a, secondPools, identity)
	done := make(chan error, 1)
	go func() { _, e := f.run(2, "INSERT INTO items SELECT 2 FROM pg_sleep(0.5)"); done <- e }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var running int
		f.roles.Audit.QueryRow(`SELECT count(*) FROM platform_migrations.attempts WHERE status='RUNNING'`).Scan(&running)
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("migration lambat tidak mulai")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = second.Run(context.Background(), alias.ID, migration.MigrationRequest{Version: 3, QuerySQL: "INSERT INTO items VALUES(3)"}); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	rows := f.history(t)
	if rows[0].Version != 3 || rows[1].Version != 2 || rows[0].ExecutedAt.Before(rows[1].ExecutedAt) || rows[0].FinishedAt.Before(*rows[1].FinishedAt) {
		t.Fatal("urutan fisik database terbalik")
	}
}
func mustCipher(t *testing.T) *database.DatabasePasswordCipher {
	t.Helper()
	c, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestUnsafeSuperuserExecutorRejected(t *testing.T) {
	f := newFixture(t)
	u := f.config
	u.Name = "unsafe"
	source, _ := url.Parse(f.target.DSN)
	u.Username = source.User.Username()
	u.Password, _ = source.User.Password()
	c, err := f.repo.Create(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	f.internal.DB.Exec(`INSERT INTO platform.group_database_access(group_id,database_id,access_level) VALUES(1,$1,'View + Edit')`, c.ID)
	if _, err = f.engine.Run(context.Background(), c.ID, migration.MigrationRequest{Version: 1, QuerySQL: "CREATE TABLE forbidden(id INT)"}); !errors.Is(err, migration.ErrUnsafeTarget) {
		t.Fatal("akun istimewa diterima", err)
	}
}
func TestLegacyHistoryImportAndRetry(t *testing.T) {
	f := newFixture(t)
	if _, err := f.roles.Executor.Exec(`CREATE TABLE public.schema_migrations(id INT PRIMARY KEY,version INT,executed_by INT,executed_at TIMESTAMP,status TEXT,query_sql TEXT); INSERT INTO public.schema_migrations VALUES(1,5,42,'2026-10-25 10:00:00','SUCCESS','CREATE TABLE historical(id INT)')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(6, "CREATE TABLE next(id INT)"); !errors.Is(err, migration.ErrLegacyHistory) {
		t.Fatal("history lama diabaikan", err)
	}
	zone, _ := time.LoadLocation("Asia/Jakarta")
	for i := 0; i < 2; i++ {
		if err := migration.ImportHistory(context.Background(), f.roles.Audit, f.roles.Executor, database.Postgres, f.target.Name, map[int]int{42: 1}, zone); err != nil {
			t.Fatal(err)
		}
	}
	h := f.history(t)
	if len(h) != 1 || h[0].Version != 5 || h[0].ExecutedBy != 1 || !h[0].ExecutedAt.Equal(time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)) {
		t.Fatal("history lama atau zona waktunya berubah")
	}
	if _, err := f.run(6, "CREATE TABLE next(id INT)"); err != nil {
		t.Fatal(err)
	}
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM public.schema_migrations`).Scan(&count)
	if count != 1 {
		t.Fatal("sumber history lama diubah")
	}
}
func TestCancellationRollsBackAndRecordsFailure(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(1, "CREATE TABLE items(id INT)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := f.engine.Run(ctx, f.id, migration.MigrationRequest{Version: 2, QuerySQL: "INSERT INTO items SELECT 1 FROM pg_sleep(1)"}); err == nil {
		t.Fatal("timeout diabaikan")
	}
	var count int
	f.roles.Executor.QueryRow(`SELECT count(*) FROM items`).Scan(&count)
	if count != 0 {
		t.Fatal("request dibatalkan meninggalkan perubahan")
	}
	h := f.history(t)
	if len(h) != 2 || h[0].Status != "FAILED" {
		t.Fatal("pembatalan tidak tercatat")
	}
}
func TestLostCoordinatorFencesOldTransaction(t *testing.T){
 f:=newFixture(t);if _,err:=f.run(1,"CREATE TABLE items(id INT PRIMARY KEY)");err!=nil{t.Fatal(err)}
 done:=make(chan error,1);go func(){_,err:=f.run(2,"INSERT INTO items SELECT 2 FROM pg_sleep(0.6)");done<-err}()
 deadline:=time.Now().Add(3*time.Second);var pid int
 for {err:=f.target.DB.QueryRow(`SELECT pid FROM pg_locks WHERE locktype='advisory' AND objid=311009004 AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&pid);if err==nil{var running int;f.roles.Audit.QueryRow(`SELECT count(*) FROM platform_migrations.attempts WHERE status='RUNNING'`).Scan(&running);if running==1{break}};if time.Now().After(deadline){t.Fatal("pengunci tidak tersedia")};time.Sleep(10*time.Millisecond)}
 var terminated bool;if f.target.DB.QueryRow(`SELECT pg_terminate_backend($1)`,pid).Scan(&terminated)!=nil || !terminated{t.Fatal("koneksi pengunci test tidak dapat diputus")}
 if _,err:=f.run(3,"INSERT INTO items VALUES(3)");err!=nil{t.Fatal(err)};if err:=<-done;err==nil{t.Fatal("proses tanpa kunci tetap berhasil")}
 var old,new int;f.roles.Executor.QueryRow(`SELECT count(*) FROM items WHERE id=2`).Scan(&old);f.roles.Executor.QueryRow(`SELECT count(*) FROM items WHERE id=3`).Scan(&new);if old!=0 || new!=1{t.Fatal("transaction lama melewati pagar")}
 h:=f.history(t);if h[0].Version!=3 || h[0].Status!="SUCCESS" || h[1].Status!="INTERRUPTED"{t.Fatal("history pemulihan tidak konsisten")}
}

type mockDispatcher struct {
	called     int
	databaseID int
	proposalID int64
	direction  migration.MigrationDirection
}

func (m *mockDispatcher) EnqueueMigration(_ context.Context, dbID int, propID int64, dir migration.MigrationDirection) error {
	m.called++
	m.databaseID = dbID
	m.proposalID = propID
	m.direction = dir
	return nil
}

func (f *fixture) engineAsUser(userID int) *migration.Engine {
	identity := platform.VerifiedIdentityFunc(func(context.Context) (int, error) { return userID, nil })
	a := access.NewEffectiveAccessService(access.NewEffectiveAccessRepository(f.internal.DB), identity)
	return migration.NewEngine(a, f.pools, identity)
}

func TestProposalWorkflow_ApprovalAndSelfApprovalBlocked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Tambahkan user kedua (user_id = 2) ke grup editors agar punya hak View + Edit
	if _, err := f.internal.DB.Exec(`INSERT INTO platform.users(username,password_hash) VALUES('reviewer','hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.internal.DB.Exec(`INSERT INTO platform.group_memberships(user_id,group_id) VALUES(2,1)`); err != nil {
		t.Fatal(err)
	}

	// 1. User 1 mengajukan proposal
	prop, err := f.engine.SubmitProposal(ctx, f.id, migration.MigrationRequest{
		Version:     1,
		Description: "Tambah tabel orders",
		QuerySQL:    "CREATE TABLE public.orders(id INT PRIMARY KEY)",
		RollbackSQL: "DROP TABLE public.orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prop.Status != migration.StatusPendingApproval || prop.CreatedBy != 1 {
		t.Fatalf("status awal tidak sesuai: %+v", prop)
	}

	// 2. User 1 mencoba approve pengajuannya sendiri -> harus ditolak
	if _, err := f.engine.ApproveProposal(ctx, f.id, prop.ID); !errors.Is(err, migration.ErrSelfApprovalBlocked) {
		t.Fatalf("self-approval seharusnya ditolak, dapat: %v", err)
	}

	// 3. User 2 menyetujui proposal -> harus berhasil
	reviewerEngine := f.engineAsUser(2)
	approved, err := reviewerEngine.ApproveProposal(ctx, f.id, prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != migration.StatusApproved || approved.ReviewedBy == nil || *approved.ReviewedBy != 2 {
		t.Fatalf("approval gagal tercatat: %+v", approved)
	}

	// 4. Proposal yang sudah APPROVED tidak bisa di-approve ulang atau di-reject
	if _, err := reviewerEngine.ApproveProposal(ctx, f.id, prop.ID); !errors.Is(err, migration.ErrInvalidState) {
		t.Fatalf("double approval seharusnya gagal: %v", err)
	}
	if _, err := reviewerEngine.RejectProposal(ctx, f.id, prop.ID, "terlambat"); !errors.Is(err, migration.ErrInvalidState) {
		t.Fatalf("reject setelah approve seharusnya gagal: %v", err)
	}
}

func TestProposalWorkflow_RejectAndAsyncQueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.internal.DB.Exec(`INSERT INTO platform.users(username,password_hash) VALUES('reviewer','hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.internal.DB.Exec(`INSERT INTO platform.group_memberships(user_id,group_id) VALUES(2,1)`); err != nil {
		t.Fatal(err)
	}
	reviewerEngine := f.engineAsUser(2)

	// Skenario Reject
	p1, err := f.engine.SubmitProposal(ctx, f.id, migration.MigrationRequest{
		Version:     1,
		Description: "Proposal ditolak",
		QuerySQL:    "CREATE TABLE public.temp(id INT)",
		RollbackSQL: "DROP TABLE public.temp",
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := reviewerEngine.RejectProposal(ctx, f.id, p1.ID, "Nama tabel kurang deskriptif")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != migration.StatusRejected || rejected.RejectionReason != "Nama tabel kurang deskriptif" {
		t.Fatalf("reject tidak tercatat dengan benar: %+v", rejected)
	}

	// Skenario Queue ke Async Worker (Nate)
	p2, err := f.engine.SubmitProposal(ctx, f.id, migration.MigrationRequest{
		Version:     1,
		Description: "Proposal valid",
		QuerySQL:    "CREATE TABLE public.customers(id INT PRIMARY KEY)",
		RollbackSQL: "DROP TABLE public.customers",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewerEngine.ApproveProposal(ctx, f.id, p2.ID); err != nil {
		t.Fatal(err)
	}

	dispatcher := &mockDispatcher{}
	f.engine.SetDispatcher(dispatcher)

	queued, err := f.engine.QueueApprovedProposal(ctx, f.id, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != migration.StatusQueued || dispatcher.called != 1 || dispatcher.proposalID != p2.ID || dispatcher.direction != migration.DirectionForward {
		t.Fatalf("dispatch ke worker tidak sesuai: status=%s, dispatcher=%+v", queued.Status, dispatcher)
	}
}

func TestForwardAndRollbackLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.internal.DB.Exec(`INSERT INTO platform.users(username,password_hash) VALUES('reviewer','hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.internal.DB.Exec(`INSERT INTO platform.group_memberships(user_id,group_id) VALUES(2,1)`); err != nil {
		t.Fatal(err)
	}
	reviewerEngine := f.engineAsUser(2)

	// Eksekusi v1 dan v2 melalui proposal workflow (tanpa dispatcher = eksekusi langsung)
	p1, err := f.engine.SubmitProposal(ctx, f.id, migration.MigrationRequest{
		Version:     1,
		Description: "Create users",
		QuerySQL:    "CREATE TABLE public.app_users(id INT PRIMARY KEY)",
		RollbackSQL: "DROP TABLE public.app_users",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewerEngine.ApproveProposal(ctx, f.id, p1.ID); err != nil {
		t.Fatal(err)
	}
	exec1, err := f.engine.QueueApprovedProposal(ctx, f.id, p1.ID)
	if err != nil || exec1.Status != migration.StatusExecuted || exec1.AttemptID == nil {
		t.Fatalf("eksekusi v1 gagal: %+v, err=%v", exec1, err)
	}

	p2, err := f.engine.SubmitProposal(ctx, f.id, migration.MigrationRequest{
		Version:     2,
		Description: "Add email column",
		QuerySQL:    "ALTER TABLE public.app_users ADD COLUMN email TEXT",
		RollbackSQL: "ALTER TABLE public.app_users DROP COLUMN email",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewerEngine.ApproveProposal(ctx, f.id, p2.ID); err != nil {
		t.Fatal(err)
	}
	exec2, err := f.engine.QueueApprovedProposal(ctx, f.id, p2.ID)
	if err != nil || exec2.Status != migration.StatusExecuted || exec2.AttemptID == nil {
		t.Fatalf("eksekusi v2 gagal: %+v, err=%v", exec2, err)
	}

	// 1. Mencoba rollback v1 padahal v2 masih aktif -> harus ditolak (ErrNotLatestVersion)
	if _, err := f.engine.Rollback(ctx, f.id, migration.RollbackRequest{AttemptID: *exec1.AttemptID}); !errors.Is(err, migration.ErrNotLatestVersion) {
		t.Fatalf("rollback loncat versi seharusnya ditolak, dapat: %v", err)
	}

	// 2. Rollback v2 (versi terakhir) -> harus berhasil dan otomatis mengambil rollback_sql dari proposal
	rbRes, err := f.engine.Rollback(ctx, f.id, migration.RollbackRequest{AttemptID: *exec2.AttemptID})
	if err != nil || rbRes.Status != "ROLLED_BACK" {
		t.Fatalf("rollback v2 gagal: %+v, err=%v", rbRes, err)
	}

	// Pastikan kolom email benar-benar terhapus di target database
	var colCount int
	if err := f.roles.Executor.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='app_users' AND column_name='email'`).Scan(&colCount); err != nil || colCount != 0 {
		t.Fatalf("kolom email masih ada setelah rollback: count=%d, err=%v", colCount, err)
	}

	// Pastikan latest_version di history mundur kembali ke 1
	_, latest, err := f.engine.History(ctx, f.id, 10, 0)
	if err != nil || latest != 1 {
		t.Fatalf("latest_version seharusnya mundur ke 1, dapat: %d (err=%v)", latest, err)
	}
}