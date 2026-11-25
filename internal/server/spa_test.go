package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Route client-side SPA dilayani index.html; aset yang tidak ada tidak boleh jatuh ke index.html.
func TestSPAHandlerServesIndexAndAssets(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       {Data: []byte(`<!doctype html><div id="root"></div>`)},
		"assets/app-1.js":  {Data: []byte(`console.log(1)`)},
		"favicon.svg":      {Data: []byte(`<svg/>`)},
		"assets/style.css": {Data: []byte(`body{}`)},
	}
	h := spaHandler(files)
	for _, path := range []string{"/", "/login", "/databases/3?schema=public", "/admin/users", "/migrations/1/jobs/9"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app-1.js", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %v", w.Code, w.Header())
	}
	for _, missing := range []string{"/assets/missing.js", "/assets/"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, missing, nil))
		if w.Code != 404 {
			t.Fatalf("%s harus 404, dapat %d", missing, w.Code)
		}
	}
}

func TestSPARoutesThroughServerKeepSecurityHeaders(t *testing.T) {
	h := New(nil, nil)
	for _, path := range []string{"/", "/login", "/admin/connections"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("%s: %d %v", path, w.Code, w.Header())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/platform/", nil))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/" {
		t.Fatalf("redirect dashboard lama: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST ke halaman SPA harus ditolak, dapat %d", w.Code)
	}
}

// Endpoint read-only untuk SPA: daftar job, approval per group, leader group, dan group yang dipimpin.
func TestSPAReadEndpointsEnforceAccess(t *testing.T) {
	f := fixture(t)
	edit := f.grant(t, 2, "View + Edit")
	status(t, f.admin.request("PUT", fmt.Sprintf("/api/admin/groups/%d/leader", edit), `{"user_id":2}`, nil), 204)
	alice := &browser{t: t, handler: f.handler}
	alice.login("alice")

	var me struct {
		Data struct {
			Username     string `json:"username"`
			LeaderGroups []struct {
				ID int `json:"id"`
			} `json:"leader_groups"`
		} `json:"data"`
	}
	w := alice.request("GET", "/api/platform/me", "", nil)
	status(t, w, 200)
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || me.Data.Username != "alice" || len(me.Data.LeaderGroups) != 1 || me.Data.LeaderGroups[0].ID != edit {
		t.Fatalf("leader_groups: %s", w.Body.String())
	}

	base := fmt.Sprintf("/api/databases/%d/migrations/jobs", f.databaseID)
	w = alice.request("POST", base, `{"version":1,"query_sql":"CREATE TABLE public.spa_list(id int)"}`, map[string]string{"Idempotency-Key": "spa-list"})
	status(t, w, 202)
	id := objectID(t, w)

	w = alice.request("GET", base+"?limit=5", "", nil)
	status(t, w, 200)
	var list struct {
		Data []struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != id || list.Data[0].Status != "QUEUED" {
		t.Fatalf("list job: %s", w.Body.String())
	}
	status(t, alice.request("GET", base+"?limit=0", "", nil), 400)

	w = alice.request("GET", fmt.Sprintf("%s/%d/approvals", base, id), "", nil)
	status(t, w, 200)
	var approvals struct {
		Data []struct {
			GroupID        int     `json:"group_id"`
			LeaderUsername *string `json:"leader_username"`
			Approved       bool    `json:"approved"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &approvals) != nil || len(approvals.Data) != 1 || approvals.Data[0].GroupID != edit ||
		approvals.Data[0].LeaderUsername == nil || *approvals.Data[0].LeaderUsername != "alice" || approvals.Data[0].Approved {
		t.Fatalf("approval sebelum disetujui: %s", w.Body.String())
	}
	status(t, alice.request("PUT", fmt.Sprintf("%s/%d/approvals/%d", base, id, edit), "", nil), 204)
	w = alice.request("GET", fmt.Sprintf("%s/%d/approvals", base, id), "", nil)
	if json.Unmarshal(w.Body.Bytes(), &approvals) != nil || !approvals.Data[0].Approved {
		t.Fatalf("approval setelah disetujui: %s", w.Body.String())
	}
	status(t, alice.request("GET", fmt.Sprintf("%s/%d/approvals", base, 99999), "", nil), 404)

	w = f.admin.request("GET", fmt.Sprintf("/api/admin/groups/%d/leader", edit), "", nil)
	status(t, w, 200)
	if !strings.Contains(w.Body.String(), `"user_id":2`) {
		t.Fatalf("leader: %s", w.Body.String())
	}
	status(t, alice.request("GET", fmt.Sprintf("/api/admin/groups/%d/leader", edit), "", nil), 403)

	// User tanpa grant ke database ditolak; request tanpa session ditolak 401.
	status(t, f.admin.request("POST", "/api/admin/users", `{"username":"bob","password":"`+password+`","is_platform_admin":false}`, nil), 201)
	bob := &browser{t: t, handler: f.handler}
	bob.login("bob")
	status(t, bob.request("GET", base, "", nil), 403)
	status(t, bob.request("GET", fmt.Sprintf("%s/%d/approvals", base, id), "", nil), 403)
	anonymous := &browser{t: t, handler: f.handler}
	status(t, anonymous.request("GET", base, "", nil), 401)
}
