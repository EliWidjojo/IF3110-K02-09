package dashboard

import (
	"bytes"
	"embed"
	"net/http"
	"time"

	"IF3110-K02-09/internal/httpapi"
)

//go:embed web/*.html web/*.css web/*.js
var dashboardFiles embed.FS

// Halaman publik hanya memuat shell tanpa data dan seluruh akses API tetap memerlukan identitas backend.
func RegisterDashboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/platform", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpapi.WriteMethodError(w, "GET")
			return
		}
		http.Redirect(w, r, "/platform/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/platform/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpapi.WriteMethodError(w, "GET")
			return
		}
		name := ""
		contentType := ""
		switch r.URL.Path {
		case "/platform/":
			name = "dashboard.html"
			contentType = "text/html; charset=utf-8"
		case "/platform/assets/dashboard.css":
			name = "dashboard.css"
			contentType = "text/css; charset=utf-8"
		case "/platform/assets/core.js":
			name = "core.js"
			contentType = "text/javascript; charset=utf-8"
		case "/platform/assets/admin.js":
			name = "admin.js"
			contentType = "text/javascript; charset=utf-8"
		case "/platform/assets/viewer.js":
			name = "viewer.js"
			contentType = "text/javascript; charset=utf-8"
		case "/platform/assets/migrations.js":
			name = "migrations.js"
			contentType = "text/javascript; charset=utf-8"
		case "/platform/assets/analysis.js":
			name = "analysis.js"
			contentType = "text/javascript; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		data, err := dashboardFiles.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}
