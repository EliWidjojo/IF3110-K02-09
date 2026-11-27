package server

import (
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/observability"
	"IF3110-K02-09/internal/platform"
	"IF3110-K02-09/web"
	"bytes"
	"io/fs"
	"net/http"
	"strings"
	"time"
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
	// URL dashboard Milestone 1 diarahkan ke SPA.
	mux.Handle("GET /platform", http.RedirectHandler("/", http.StatusMovedPermanently))
	mux.Handle("GET /platform/", http.RedirectHandler("/", http.StatusMovedPermanently))
	mux.Handle("/", spaHandler(web.Dist()))
	return auth.Headers(mux)
}

// spaHandler menyajikan aset hasil build Vite dan mengembalikan index.html untuk route client-side
// (/login, /databases/..., /admin/...). Halaman tidak berisi data; seluruh data diambil lewat API
// yang tetap memeriksa session dan hak akses di backend.
func spaHandler(files fs.FS) http.Handler {
	static := http.FileServerFS(files)
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		index = []byte("<!doctype html><title>Atlas</title><p>Frontend belum di-build. Jalankan npm --prefix web run build.</p>")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpapi.WriteMethodError(w, "GET, HEAD")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(name, "assets/") || name == "favicon.svg" {
			if info, err := fs.Stat(files, name); err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				// Nama file aset memuat hash konten sehingga aman di-cache lama.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			static.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}
