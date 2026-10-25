package auth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/httpapi"
)

type HTTP struct {
	Store     IdentityStore
	Sessions  *Sessions
	Origin    string
	Secure    bool
	dummyHash string
	mu        sync.Mutex
	attempts  map[string]attempt
}
type attempt struct {
	count int
	start time.Time
}

func NewHTTP(store IdentityStore, sessions *Sessions, origin string, production bool) (*HTTP, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("APP_ORIGIN must be an HTTP(S) origin without a trailing slash")
	}
	if production && u.Scheme != "https" {
		return nil, errors.New("production requires an HTTPS APP_ORIGIN")
	}
	hash, err := HashPassword("dummy-password-for-timing-only")
	if err != nil {
		return nil, err
	}
	return &HTTP{Store: store, Sessions: sessions, Origin: origin, Secure: production || u.Scheme == "https", dummyHash: hash, attempts: map[string]attempt{}}, nil
}
func (h *HTTP) cookieName() string {
	if h.Secure {
		return "__Host-platform_session"
	}
	return "platform_session"
}
func (h *HTTP) token(r *http.Request) string {
	c, err := r.Cookie(h.cookieName())
	if err != nil {
		return ""
	}
	return c.Value
}
func (h *HTTP) cookie(w http.ResponseWriter, token string, p Session) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode, Expires: p.CreatedAt.Add(h.Sessions.cfg.Absolute)})
}
func (h *HTTP) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Path: "/", HttpOnly: true, Secure: h.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Error(w http.ResponseWriter, status int, message string) {
	httpapi.WriteJSON(w, status, nil, &httpapi.Error{Code: strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_")), Message: message})
}
func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		Error(w, 400, "invalid request body")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		Error(w, 400, "invalid request body")
		return false
	}
	return true
}
func (h *HTTP) csrf(r *http.Request, p Session) bool {
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.Origin {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRF)) == 1
}
func safe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func (h *HTTP) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := h.token(r)
		session, err := h.Sessions.Get(token)
		if err != nil || session.UserID == 0 {
			h.clearCookie(w)
			Error(w, 401, "authentication required")
			return
		}
		u, p, err := h.Store.Snapshot(session.UserID)
		if err != nil || u.SecurityVersion != session.SecurityVersion {
			h.Sessions.Revoke(token)
			h.clearCookie(w)
			Error(w, 401, "session revoked")
			return
		}
		if !safe(r.Method) && !h.csrf(r, session) {
			Error(w, 403, "invalid CSRF token or origin")
			return
		}
		session, current, err := h.Sessions.Use(token, u)
		if err != nil {
			h.clearCookie(w)
			Error(w, 401, "session expired")
			return
		}
		if current != token {
			h.cookie(w, current, session)
		}
		next.ServeHTTP(w, r.WithContext(access.WithPrincipal(r.Context(), p)))
	})
}
func (h *HTTP) RequireAdmin(next http.Handler) http.Handler {
	return h.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := access.FromContext(r.Context())
		if !access.Allowed(p, "", access.ManagePlatform) {
			Error(w, 403, "platform admin required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func (h *HTTP) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/csrf", h.CSRF)
	mux.HandleFunc("POST /api/auth/login", h.Login)
	mux.Handle("POST /api/auth/logout", h.Require(http.HandlerFunc(h.Logout)))
	mux.Handle("GET /api/auth/me", h.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p, _ := access.FromContext(r.Context()); JSON(w, 200, p) })))
}
func (h *HTTP) CSRF(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); (origin != "" && origin != h.Origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		Error(w, 403, "invalid origin")
		return
	}
	token := h.token(r)
	p, err := h.Sessions.Get(token)
	if err == nil && p.UserID != 0 {
		u, _, e := h.Store.Snapshot(p.UserID)
		if e != nil {
			h.Sessions.Revoke(token)
			err = ErrSession
		} else {
			p, token, err = h.Sessions.Use(token, u)
		}
	}
	if err != nil {
		p, token, err = h.Sessions.New(User{}, token)
	}
	if err != nil {
		Error(w, 503, "session unavailable")
		return
	}
	h.cookie(w, token, p)
	JSON(w, 200, map[string]string{"csrf_token": p.CSRF})
}
func (h *HTTP) allowLogin(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for key, a := range h.attempts {
		if now.Sub(a.start) >= time.Minute {
			delete(h.attempts, key)
		}
	}
	a := h.attempts[ip]
	if a.count == 0 {
		if len(h.attempts) >= 10000 {
			return false
		}
		a.start = now
	}
	a.count++
	h.attempts[ip] = a
	return a.count <= 10
}
func (h *HTTP) Login(w http.ResponseWriter, r *http.Request) {
	token := h.token(r)
	p, err := h.Sessions.Get(token)
	if err != nil || !h.csrf(r, p) {
		Error(w, 403, "fetch a CSRF token before login")
		return
	}
	if !h.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		Error(w, 429, "too many login attempts")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !Decode(w, r, &req) {
		return
	}
	u, found := h.Store.FindUsername(strings.TrimSpace(req.Username))
	hash := h.dummyHash
	if found {
		hash = u.PasswordHash
	}
	valid := VerifyPassword(hash, req.Password)
	if !valid || !found || u.Disabled {
		Error(w, 401, "invalid username or password")
		return
	}
	// Recheck after expensive password verification to catch concurrent changes.
	latest, principal, err := h.Store.Snapshot(u.ID)
	if err != nil || latest.SecurityVersion != u.SecurityVersion {
		Error(w, 401, "invalid username or password")
		return
	}
	p, token, err = h.Sessions.New(latest, token)
	if err != nil {
		Error(w, 503, "session unavailable")
		return
	}
	h.cookie(w, token, p)
	JSON(w, 200, map[string]any{"user": principal, "csrf_token": p.CSRF})
}
func (h *HTTP) Logout(w http.ResponseWriter, r *http.Request) {
	h.Sessions.Revoke(h.token(r))
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Headers must wrap the whole router, including error and anonymous responses.
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
