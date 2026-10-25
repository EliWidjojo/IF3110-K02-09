package server

import (
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/platform"
	"IF3110-K02-09/internal/testutil"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const password = "test-password-long-enough"

type browser struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func (b *browser) request(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	b.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	if b.cookie != nil {
		r.AddCookie(b.cookie)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", b.csrf)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			b.cookie = nil
		} else {
			b.cookie = c
		}
	}
	return w
}
func (b *browser) login(username string) {
	b.t.Helper()
	w := b.request("GET", "/api/auth/csrf", "", nil)
	status(b.t, w, 200)
	var p struct {
		CSRF string `json:"csrf_token"`
	}
	if json.Unmarshal(w.Body.Bytes(), &p) != nil {
		b.t.Fatal("CSRF tidak valid")
	}
	b.csrf = p.CSRF
	old := b.cookie.Value
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	w = b.request("POST", "/api/auth/login", string(body), nil)
	status(b.t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &p)
	b.csrf = p.CSRF
	if b.cookie == nil || b.cookie.Value == old || !b.cookie.HttpOnly || b.cookie.SameSite != http.SameSiteLaxMode {
		b.t.Fatal("cookie atau rotasi login tidak sesuai")
	}
	if strings.Contains(w.Body.String(), b.cookie.Value) || strings.Contains(w.Body.String(), password) {
		b.t.Fatal("response membocorkan rahasia")
	}
}
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
	}
}
func objectID(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	var p struct {
		Data struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Data.ID <= 0 {
		t.Fatal("ID response tidak valid")
	}
	return p.Data.ID
}

type setup struct {
	internal, target *testutil.Postgres
	roles            testutil.Roles
	store            *auth.PostgresStore
	auth             *auth.HTTP
	module           *platform.PlatformModule
	handler          http.Handler
	admin            *browser
	databaseID       int
}

func fixture(t *testing.T) *setup {
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
	if store.Bootstrap(ctx, "admin", password) != nil {
		t.Fatal("bootstrap gagal")
	}
	sessions, _ := auth.NewSessions(auth.DefaultSessionConfig())
	a, err := auth.NewHTTP(store, sessions, "http://localhost:8080", false)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	t.Setenv(database.DatabaseCredentialsKeyEnv, base64.StdEncoding.EncodeToString(key))
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	module, err := platform.NewPlatformModule(internal.DB, cipher, platform.SecurityDependencies{Identity: a, CSRF: a, Sessions: a, PasswordHasher: platform.PasswordHasherFunc(func(_ context.Context, p string) (admin.PasswordHash, error) {
		hash, e := auth.HashPassword(p)
		if e != nil {
			return "", admin.ErrInvalidUserInput
		}
		return admin.PasswordHash(hash), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Close() })
	handler := New(a, module)
	b := &browser{t: t, handler: handler}
	b.login("admin")
	w := b.request("POST", "/api/admin/users", `{"username":"alice","password":"`+password+`","is_platform_admin":false}`, nil)
	status(t, w, 201)
	config := map[string]any{"name": "target", "driver": "postgres", "host": target.Host, "port": target.Port, "database_name": target.Name, "username": roles.ExecutorName, "password": roles.Password, "audit_username": roles.AuditName, "audit_password": roles.AuditPassword, "ssl_mode": "disable"}
	body, _ := json.Marshal(config)
	w = b.request("POST", "/api/admin/databases", string(body), nil)
	status(t, w, 201)
	if strings.Contains(w.Body.String(), roles.Password) || strings.Contains(w.Body.String(), roles.AuditPassword) {
		t.Fatal("response koneksi membocorkan rahasia")
	}
	return &setup{internal, target, roles, store, a, module, handler, b, objectID(t, w)}
}
func (f *setup) grant(t *testing.T, user int, level string) int {
	t.Helper()
	w := f.admin.request("POST", "/api/admin/groups", fmt.Sprintf(`{"name":"group-%d-%s"}`, user, level), nil)
	status(t, w, 201)
	id := objectID(t, w)
	status(t, f.admin.request("POST", fmt.Sprintf("/api/admin/groups/%d/members", id), fmt.Sprintf(`{"user_id":%d}`, user), nil), 201)
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", id, f.databaseID), fmt.Sprintf(`{"access_level":%q}`, level), nil), 200)
	return id
}
func (f *setup) execute(b *browser, version int, query string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"version": version, "query_sql": query})
	return b.request("POST", fmt.Sprintf("/api/databases/%d/migrations/execute", f.databaseID), string(body), nil)
}
func TestAuthenticationCSRFAndLogoutIntegration(t *testing.T) {
	f := fixture(t)
	b := &browser{t: t, handler: f.handler}
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	status(t, b.request("POST", "/api/auth/login", `{"username":"admin","password":"`+password+`"}`, nil), 403)
	b.login("admin")
	if b.cookie.Secure {
		t.Fatal("cookie development tidak dapat digunakan")
	}
	w := b.request("GET", "/api/auth/me", "", nil)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), "Password") || strings.Contains(w.Body.String(), "Hash") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("serialisasi rahasia/cache")
	}
	for _, headers := range []map[string]string{{"X-CSRF-Token": ""}, {"X-CSRF-Token": "wrong"}, {"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		status(t, b.request("POST", "/api/auth/logout", "", headers), 403)
	}
	saved := b.cookie
	status(t, b.request("POST", "/api/auth/logout", "", nil), 204)
	b.cookie = saved
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
}
func TestGroupAccessAndMigrationIntegration(t *testing.T) {
	f := fixture(t)
	history := fmt.Sprintf("/api/databases/%d/migrations/history", f.databaseID)
	status(t, f.admin.request("GET", history, "", nil), 403)
	group := f.grant(t, 2, "View Only")
	user := &browser{t: t, handler: f.handler}
	user.login("alice")
	status(t, user.request("POST", "/api/admin/groups", `{"name":"intruders"}`, nil), 403)
	status(t, user.request("GET", "/api/viewer/databases/999/schemas", "", nil), 403)
	status(t, user.request("POST", fmt.Sprintf("/api/databases/%d/migrations/execute", f.databaseID), `{"version":1,"query_sql":"CREATE TABLE x(id INT)","UserAccess":"View + Edit","UserID":1}`, map[string]string{"X-User-Access": "View + Edit"}), 403)
	status(t, user.request("GET", history, "", nil), 200)
	old := *user.cookie
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), `{"access_level":"View + Edit"}`, nil), 200)
	status(t, user.request("GET", "/api/auth/me", "", nil), 200)
	if old.Value == user.cookie.Value {
		t.Fatal("peningkatan izin tidak merotasi session")
	}
	status(t, user.request("POST", fmt.Sprintf("/api/databases/%d/migrations/execute", f.databaseID), `{"version":1,"query_sql":"CREATE TABLE x(id INT)","UserID":1}`, nil), 400)
	status(t, f.execute(user, 1, "CREATE TABLE x(id INT)"), 200)
	var actor int
	if f.roles.Audit.QueryRow(`SELECT executed_by FROM platform_migrations.attempts WHERE status='SUCCESS'`).Scan(&actor) != nil || actor != 2 {
		t.Fatal("pelaksana bukan identitas session")
	}
	status(t, f.admin.request("DELETE", fmt.Sprintf("/api/admin/groups/%d/database-access/%d", group, f.databaseID), "", nil), 200)
	status(t, user.request("GET", "/api/auth/me", "", nil), 401)
	_, p, _ := f.store.Snapshot(2)
	if len(p.Databases) != 0 {
		t.Fatal("pencabutan izin tidak berlaku")
	}
}
func TestLoginFailuresProductionCookiesAndRevocation(t *testing.T) {
	f := fixture(t)
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	for _, name := range []string{"alice", "missing-user"} {
		w := b.request("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":"wrong"}`, name), nil)
		status(t, w, 401)
		if !strings.Contains(w.Body.String(), "invalid username or password") {
			t.Fatal("pesan dapat membedakan akun")
		}
	}
	status(t, f.admin.request("POST", "/api/admin/users/2/deactivate", "", nil), 200)
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	if _, err := auth.NewHTTP(f.store, f.auth.Sessions, "http://localhost:8080", true); err == nil {
		t.Fatal("production menerima origin HTTP")
	}
	secure, err := auth.NewHTTP(f.store, f.auth.Sessions, "https://platform.example", true)
	if err != nil {
		t.Fatal(err)
	}
	b = &browser{t: t, handler: New(secure, nil)}
	b.login("admin")
	if !b.cookie.Secure || b.cookie.Name != "__Host-platform_session" || b.cookie.Domain != "" {
		t.Fatal("cookie production tidak aman")
	}
}
func TestLoginCSRFCrossOriginAndInvalidPayload(t *testing.T) {
	f := fixture(t)
	b := &browser{t: t, handler: f.handler}
	status(t, b.request("GET", "/api/auth/csrf", "", map[string]string{"Origin": "https://evil.example"}), 403)
	b.login("admin")
	status(t, b.request("POST", "/api/auth/login", `{"username":"admin","password":"`+password+`"}`, map[string]string{"Origin": "https://evil.example"}), 403)
	status(t, b.request("POST", "/api/auth/login", "username=admin", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}), 415)
	status(t, b.request("POST", "/api/admin/groups", `{"name":"a"} {"name":"b"}`, nil), 400)
}
func TestOldRevokedSessionDoesNotInvalidateNewLogin(t *testing.T) {
	f := fixture(t)
	b := &browser{t: t, handler: f.handler}
	b.login("alice")
	old := *b.cookie
	status(t, f.admin.request("POST", "/api/admin/users/2/revoke-sessions", "", nil), 200)
	b = &browser{t: t, handler: f.handler}
	b.login("alice")
	fresh := *b.cookie
	b.cookie = &old
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	b.cookie = &fresh
	status(t, b.request("GET", "/api/auth/me", "", nil), 200)
}
func TestDashboardRoutesAndAdministrationContracts(t *testing.T) {
	f := fixture(t)
	for _, path := range []string{"/platform/", "/platform/assets/core.js", "/platform/assets/admin.js", "/platform/assets/viewer.js", "/platform/assets/migrations.js", "/platform/assets/analysis.js", "/api/platform/me", "/api/admin/users", "/api/admin/groups", "/api/admin/databases"} {
		status(t, f.admin.request("GET", path, "", nil), 200)
	}
	status(t, f.admin.request("PATCH", "/api/admin/users/2", `{"username":"alice-new","is_active":true,"is_platform_admin":false}`, nil), 200)
	status(t, f.admin.request("POST", fmt.Sprintf("/api/admin/databases/%d/check-connection", f.databaseID), "", nil), 200)
	status(t, f.admin.request("PATCH", fmt.Sprintf("/api/admin/databases/%d", f.databaseID), `{"name":"renamed"}`, nil), 200)
	status(t, f.admin.request("PATCH", "/api/admin/users/1", `{"is_active":false}`, nil), 400)
	b := &browser{t: t, handler: f.handler}
	b.login("alice-new")
	for _, path := range []string{"/api/admin/users", "/api/admin/groups", "/api/admin/databases"} {
		status(t, b.request("GET", path, "", nil), 403)
	}
}
