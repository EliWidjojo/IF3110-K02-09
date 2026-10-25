package server

import (
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/dashboard"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/platform"
	"net/http"
)

func New(a *auth.HTTP, module *platform.PlatformModule) http.Handler {
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"}, nil)
	})
	protected := http.NewServeMux()
	if module != nil {
		module.RegisterRoutes(protected)
	}
	// Semua API fitur melewati verifikasi session sebelum handler membaca identitas dari context.
	mux.Handle("/api/", a.Require(protected))
	dashboard.RegisterDashboardRoutes(mux)
	mux.HandleFunc("GET /{$}", loginPage)
	mux.HandleFunc("GET /auth.js", loginScript)
	return auth.Headers(mux)
}
