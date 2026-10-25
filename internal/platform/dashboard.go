package platform

import (
	"net/http"

	"IF3110-K02-09/internal/dashboard"
)

// Registrasi dipertahankan agar pemanggil komposisi platform tidak perlu mengikuti lokasi aset.
func RegisterDashboardRoutes(mux *http.ServeMux) { dashboard.RegisterDashboardRoutes(mux) }
