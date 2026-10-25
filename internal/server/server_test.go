package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"github.com/DATA-DOG/go-sqlmock"
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
	for name, value := range headers {
		r.Header.Set(name, value)
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
	var payload struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		b.t.Fatal(err)
	}
	b.csrf = payload.CSRF
	old := b.cookie.Value
	w = b.request("POST", "/api/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, nil)
	status(b.t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		b.t.Fatal(err)
	}
	b.csrf = payload.CSRF
	if b.cookie.Value == old {
		b.t.Fatal("login did not rotate cookie")
	}
	if !b.cookie.HttpOnly || b.cookie.SameSite != http.SameSiteLaxMode {
		b.t.Fatal("missing cookie protections")
	}
	if strings.Contains(w.Body.String(), b.cookie.Value) || strings.Contains(w.Body.String(), password) {
		b.t.Fatal("response leaked credentials")
	}
}
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
	}
}
func fixture(t *testing.T) (*auth.Store, *auth.HTTP, http.Handler, sqlmock.Sqlmock) {
	t.Helper()
	store, err := auth.OpenStore(filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUser("admin", password, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUser("alice", password, false); err != nil {
		t.Fatal(err)
	}
	sessions, _ := auth.NewSessions(auth.DefaultSessionConfig())
	a, err := auth.NewHTTP(store, sessions, "http://localhost:8080", false)
	if err != nil {
		t.Fatal(err)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	targets := map[string]*database.Target{"one": {ID: "one", Name: "One", Dialect: database.Postgres, DB: db}, "two": {ID: "two", Name: "Two", Dialect: database.MySQL, DB: db}}
	return store, a, New(a, targets), mock
}
func TestAuthenticationCSRFAndLogoutIntegration(t *testing.T) {
	_, _, handler, _ := fixture(t)
	b := &browser{t: t, handler: handler}
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	status(t, b.request("POST", "/api/auth/login", `{"username":"admin","password":"`+password+`"}`, nil), 403)
	b.login("admin")
	if b.cookie.Secure {
		t.Fatal("HTTP development cookie must work on localhost")
	}
	w := b.request("GET", "/api/auth/me", "", nil)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), "Password") || strings.Contains(w.Body.String(), "Hash") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret serialization/cache regression")
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
	store, _, handler, mock := fixture(t)
	admin := &browser{t: t, handler: handler}
	admin.login("admin")
	status(t, admin.request("GET", "/api/databases/one/migrations/history", "", nil), 403)
	w := admin.request("POST", "/api/admin/groups", `{"name":"readers"}`, nil)
	status(t, w, 201)
	status(t, admin.request("PUT", "/api/admin/groups/1/members/2", `{"member":true}`, nil), 204)
	status(t, admin.request("PUT", "/api/admin/groups/1/databases/one", `{"level":"View Only"}`, nil), 204)
	user := &browser{t: t, handler: handler}
	user.login("alice")
	status(t, user.request("POST", "/api/admin/groups", `{"name":"intruders"}`, nil), 403)
	status(t, user.request("GET", "/api/databases/two/schema", "", nil), 403)
	status(t, user.request("POST", "/api/databases/one/migrations/execute", `{"version":1,"query_sql":"CREATE TABLE x (id INT)","UserAccess":"View + Edit","UserID":1}`, map[string]string{"X-User-Access": "View + Edit"}), 403)
	mock.ExpectQuery("SELECT id, version").WillReturnRows(sqlmock.NewRows([]string{"id", "version", "executed_by", "executed_at", "status", "query_sql"}).AddRow(1, 1, 2, time.Now(), "SUCCESS", "CREATE TABLE x (id INT)"))
	status(t, user.request("GET", "/api/databases/one/migrations/history", "", nil), 200)
	old := *user.cookie
	status(t, admin.request("PUT", "/api/admin/groups/1/databases/one", `{"level":"View + Edit"}`, nil), 204)
	status(t, user.request("GET", "/api/auth/me", "", nil), 200)
	if old.Value == user.cookie.Value {
		t.Fatal("privilege increase failed to rotate")
	}
	status(t, user.request("POST", "/api/databases/one/migrations/execute", `{"version":2,"query_sql":"CREATE TABLE y (id INT)","UserID":1}`, nil), 400)
	mock.ExpectQuery(`SELECT MAX\(version\)`).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE y").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO schema_migrations").WithArgs(2, 2, sqlmock.AnyArg(), "SUCCESS", "CREATE TABLE y (id INT)").WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	status(t, user.request("POST", "/api/databases/one/migrations/execute", `{"version":2,"query_sql":"CREATE TABLE y (id INT)"}`, map[string]string{"X-User-Access": "View Only"}), 200)
	status(t, admin.request("PUT", "/api/admin/groups/1/databases/one", `{"level":""}`, nil), 204)
	status(t, user.request("GET", "/api/auth/me", "", nil), 401)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_, principal, _ := store.Snapshot(2)
	if access.Allowed(principal, "one", access.ViewData) {
		t.Fatal("revocation ineffective")
	}
}
func TestLoginFailuresProductionCookiesAndRevocation(t *testing.T) {
	store, a, handler, _ := fixture(t)
	b := &browser{t: t, handler: handler}
	b.login("alice")
	for _, name := range []string{"alice", "missing-user"} {
		w := b.request("POST", "/api/auth/login", `{"username":"`+name+`","password":"wrong"}`, nil)
		status(t, w, 401)
		if !strings.Contains(w.Body.String(), "invalid username or password") {
			t.Fatal("account enumeration error")
		}
	}
	if err := store.UpdateUser(2, false, true, ""); err != nil {
		t.Fatal(err)
	}
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	if _, err := auth.NewHTTP(store, a.Sessions, "http://localhost:8080", true); err == nil {
		t.Fatal("production HTTP accepted")
	}
	secure, err := auth.NewHTTP(store, a.Sessions, "https://platform.example", true)
	if err != nil {
		t.Fatal(err)
	}
	b = &browser{t: t, handler: New(secure, nil)}
	b.login("admin")
	if !b.cookie.Secure || b.cookie.Name != "__Host-platform_session" || b.cookie.Domain != "" {
		t.Fatal("production cookie insecure")
	}
}
func TestLoginCSRFCrossOriginAndInvalidPayload(t *testing.T) {
	_, _, handler, _ := fixture(t)
	b := &browser{t: t, handler: handler}
	status(t, b.request("GET", "/api/auth/csrf", "", map[string]string{"Origin": "https://evil.example"}), 403)
	b.login("admin")
	status(t, b.request("POST", "/api/auth/login", `{"username":"admin","password":"`+password+`"}`, map[string]string{"Origin": "https://evil.example"}), 403)
	status(t, b.request("POST", "/api/auth/login", "username=admin", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}), 415)
	status(t, b.request("POST", "/api/admin/groups", `{"name":"a"} {"name":"b"}`, nil), 400)
}

func TestOldRevokedSessionDoesNotInvalidateNewLogin(t *testing.T) {
	store, _, handler, _ := fixture(t)
	b := &browser{t: t, handler: handler}
	b.login("alice")
	old := *b.cookie
	if err := store.UpdateUser(2, false, false, password); err != nil {
		t.Fatal(err)
	}
	// A different browser logs in, leaving the old session record to be checked.
	b = &browser{t: t, handler: handler}
	b.login("alice")
	fresh := *b.cookie
	b.cookie = &old
	status(t, b.request("GET", "/api/auth/me", "", nil), 401)
	b.cookie = &fresh
	status(t, b.request("GET", "/api/auth/me", "", nil), 200)
}
