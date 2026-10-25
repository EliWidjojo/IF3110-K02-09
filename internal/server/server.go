package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/migration"
)

type Server struct {
	Auth       *auth.HTTP
	Targets    map[string]*database.Target
	migrations map[string]*migration.MigrationHandler
}

func New(a *auth.HTTP, targets map[string]*database.Target) http.Handler {
	s := &Server{Auth: a, Targets: targets, migrations: map[string]*migration.MigrationHandler{}}
	for id, target := range targets {
		s.migrations[id] = migration.NewDatabaseHandler(migration.NewMigrationServiceWithDialect(target.DB, target.Dialect), id)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { auth.JSON(w, 200, map[string]string{"status": "ok"}) })
	mux.Handle("GET /api/databases", a.Require(http.HandlerFunc(s.listDatabases)))
	mux.Handle("GET /api/databases/{database}/schema", a.Require(s.database(access.ViewSchema, s.schema)))
	mux.Handle("GET /api/databases/{database}/data", a.Require(s.database(access.ViewData, s.data)))
	mux.Handle("GET /api/databases/{database}/migrations/history", a.Require(s.database(access.ViewHistory, func(w http.ResponseWriter, r *http.Request, t *database.Target) {
		s.migrations[t.ID].HandleGetHistory(w, r)
	})))
	mux.Handle("POST /api/databases/{database}/migrations/execute", a.Require(s.database(access.WriteMigration, func(w http.ResponseWriter, r *http.Request, t *database.Target) {
		s.migrations[t.ID].HandleExecuteMigration(w, r)
	})))
	mux.Handle("POST /api/admin/users", a.RequireAdmin(http.HandlerFunc(s.createUser)))
	mux.Handle("PUT /api/admin/users/{user}", a.RequireAdmin(http.HandlerFunc(s.updateUser)))
	mux.Handle("POST /api/admin/users/{user}/revoke-sessions", a.RequireAdmin(http.HandlerFunc(s.revokeSessions)))
	mux.Handle("POST /api/admin/groups", a.RequireAdmin(http.HandlerFunc(s.createGroup)))
	mux.Handle("PUT /api/admin/groups/{group}/members/{user}", a.RequireAdmin(http.HandlerFunc(s.membership)))
	mux.Handle("PUT /api/admin/groups/{group}/databases/{database}", a.RequireAdmin(http.HandlerFunc(s.grant)))
	mux.HandleFunc("GET /{$}", loginPage)
	mux.HandleFunc("GET /auth.js", loginScript)
	return auth.Headers(mux)
}

type targetHandler func(http.ResponseWriter, *http.Request, *database.Target)

func (s *Server) database(action access.Action, next targetHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("database")
		p, _ := access.FromContext(r.Context())
		if !access.Allowed(p, id, action) {
			auth.Error(w, 403, "database access denied")
			return
		}
		t, ok := s.Targets[id]
		if !ok {
			auth.Error(w, 404, "database not found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx), t)
	})
}
func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	p, _ := access.FromContext(r.Context())
	targets := []*database.Target{}
	for id, target := range s.Targets {
		if access.Allowed(p, id, access.ViewSchema) {
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	auth.JSON(w, 200, targets)
}
func (s *Server) schema(w http.ResponseWriter, r *http.Request, t *database.Target) {
	result, err := t.Metadata(r.Context())
	if err != nil {
		auth.Error(w, 503, "database metadata unavailable")
		return
	}
	auth.JSON(w, 200, result)
}
func (s *Server) data(w http.ResponseWriter, r *http.Request, t *database.Target) {
	q := r.URL.Query()
	limit, offset := 50, 0
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			auth.Error(w, 400, "invalid limit")
			return
		}
	}
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil {
			auth.Error(w, 400, "invalid offset")
			return
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || q.Get("schema") == "" || q.Get("table") == "" {
		auth.Error(w, 400, "schema, table and valid pagination required")
		return
	}
	result, err := t.Data(r.Context(), q.Get("schema"), q.Get("table"), limit, offset)
	if err != nil {
		auth.Error(w, 400, "table unavailable or query failed")
		return
	}
	auth.JSON(w, 200, result)
}
func pathID(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	id, err := strconv.Atoi(r.PathValue(name))
	if err != nil || id <= 0 {
		auth.Error(w, 400, "invalid identifier")
		return 0, false
	}
	return id, true
}
func mutation(w http.ResponseWriter, err error) {
	if err != nil {
		auth.Error(w, 400, "change rejected; verify identifiers, values and last-admin constraint")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Admin    bool   `json:"platform_admin"`
	}
	if !auth.Decode(w, r, &req) {
		return
	}
	id, err := s.Auth.Store.CreateUser(req.Username, req.Password, req.Admin)
	if err != nil {
		auth.Error(w, 400, "cannot create user; check username uniqueness and password length (12-1024 bytes)")
		return
	}
	auth.JSON(w, 201, map[string]int{"id": id})
}
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "user")
	if !ok {
		return
	}
	var req struct {
		Admin    *bool  `json:"platform_admin"`
		Disabled *bool  `json:"disabled"`
		Password string `json:"password"`
	}
	if !auth.Decode(w, r, &req) {
		return
	}
	if req.Admin == nil || req.Disabled == nil {
		auth.Error(w, 400, "platform_admin and disabled are required")
		return
	}
	mutation(w, s.Auth.Store.UpdateUser(id, *req.Admin, *req.Disabled, req.Password))
}
func (s *Server) revokeSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "user")
	if !ok {
		return
	}
	s.Auth.Sessions.RevokeUser(id)
	w.WriteHeader(204)
}
func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !auth.Decode(w, r, &req) {
		return
	}
	id, err := s.Auth.Store.CreateGroup(req.Name)
	if err != nil {
		auth.Error(w, 400, "cannot create group")
		return
	}
	auth.JSON(w, 201, map[string]int{"id": id})
}
func (s *Server) membership(w http.ResponseWriter, r *http.Request) {
	group, ok := pathID(w, r, "group")
	if !ok {
		return
	}
	user, ok := pathID(w, r, "user")
	if !ok {
		return
	}
	var req struct {
		Member *bool `json:"member"`
	}
	if !auth.Decode(w, r, &req) {
		return
	}
	if req.Member == nil {
		auth.Error(w, 400, "member is required")
		return
	}
	mutation(w, s.Auth.Store.SetMembership(group, user, *req.Member))
}
func (s *Server) grant(w http.ResponseWriter, r *http.Request) {
	group, ok := pathID(w, r, "group")
	if !ok {
		return
	}
	id := r.PathValue("database")
	if _, ok := s.Targets[id]; !ok {
		auth.Error(w, 404, "database not found")
		return
	}
	var req struct {
		Level *access.Level `json:"level"`
	}
	if !auth.Decode(w, r, &req) {
		return
	}
	if req.Level == nil {
		auth.Error(w, 400, "level is required")
		return
	}
	mutation(w, s.Auth.Store.SetGrant(group, id, *req.Level))
}
