package platform

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

type ViewerHandler struct {
	service *ViewerService
}

func NewViewerHandler(service *ViewerService) *ViewerHandler {
	return &ViewerHandler{service: service}
}

// Seluruh endpoint viewer hanya membaca metadata sehingga tidak memerlukan perubahan state maupun token CSRF.
func RegisterViewerRoutes(mux *http.ServeMux, handler *ViewerHandler) {
	mux.HandleFunc("/api/viewer/databases", handler.HandleDatabases)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas", handler.HandleSchemas)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables", handler.HandleTables)
}

func (h *ViewerHandler) HandleDatabases(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databases, err := h.service.ListDatabases(r.Context())
	if err != nil {
		writeViewerError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, databases, nil)
}

func (h *ViewerHandler) HandleSchemas(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := viewerDatabaseID(r)
	if err != nil {
		writeViewerError(w, err)
		return
	}
	schemas, err := h.service.ListSchemas(r.Context(), databaseID)
	if err != nil {
		writeViewerError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, schemas, nil)
}

func (h *ViewerHandler) HandleTables(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := viewerDatabaseID(r)
	if err != nil {
		writeViewerError(w, err)
		return
	}
	// PathValue sudah di-decode oleh router dan nama schema diteruskan sebagai nilai parameter, bukan identifier SQL.
	tables, err := h.service.ListTables(r.Context(), databaseID, r.PathValue("schema"))
	if err != nil {
		writeViewerError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, tables, nil)
}

func (h *ViewerHandler) prepareRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeUserMethodError(w, "GET")
		return false
	}
	if h == nil || h.service == nil {
		writeViewerError(w, ErrViewerDependencyUnavailable)
		return false
	}
	return true
}

func viewerDatabaseID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || id <= 0 {
		return 0, ErrInvalidViewerInput
	}
	return id, nil
}

// Respons memakai pesan tetap agar error driver, detail koneksi, dan query tidak diteruskan kepada client.
func writeViewerError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &userAPIError{Code: "internal_error", Message: "operasi viewer gagal"}
	switch {
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, ErrUserNotFound):
		status, apiError = http.StatusUnauthorized, &userAPIError{Code: "unauthenticated", Message: "authentication diperlukan"}
	case errors.Is(err, ErrDatabaseAccessDenied), errors.Is(err, ErrInactiveUser):
		status, apiError = http.StatusForbidden, &userAPIError{Code: "database_access_denied", Message: "akses database ditolak"}
	case errors.Is(err, ErrInvalidViewerInput), errors.Is(err, ErrInvalidAccessInput), errors.Is(err, ErrInvalidDatabaseConnection):
		status, apiError = http.StatusBadRequest, &userAPIError{Code: "invalid_input", Message: "ID database atau nama schema tidak valid"}
	case errors.Is(err, ErrViewerSchemaNotFound):
		status, apiError = http.StatusNotFound, &userAPIError{Code: "schema_not_found", Message: "schema tidak ditemukan atau tidak ditampilkan"}
	case errors.Is(err, ErrDatabaseConnectionNotFound):
		status, apiError = http.StatusNotFound, &userAPIError{Code: "database_connection_not_found", Message: "konfigurasi database tidak ditemukan"}
	case errors.Is(err, ErrViewerDependencyUnavailable), errors.Is(err, ErrAccessDependencyUnavailable),
		errors.Is(err, ErrTargetManagerUnavailable), errors.Is(err, ErrTargetManagerClosed), errors.Is(err, ErrTargetDriverDebug),
		errors.Is(err, ErrDatabaseEncryptionKey), errors.Is(err, ErrDatabaseDecryption):
		status, apiError = http.StatusServiceUnavailable, &userAPIError{Code: "dependency_unavailable", Message: "komponen viewer atau koneksi belum dapat digunakan"}
	case errors.Is(err, context.DeadlineExceeded):
		status, apiError = http.StatusGatewayTimeout, &userAPIError{Code: "viewer_timeout", Message: "batas waktu pembacaan database terlampaui"}
	case errors.Is(err, context.Canceled):
		status, apiError = http.StatusRequestTimeout, &userAPIError{Code: "request_canceled", Message: "request dibatalkan"}
	case errors.Is(err, ErrViewerMetadata):
		status, apiError = http.StatusBadGateway, &userAPIError{Code: "metadata_query_failed", Message: "metadata database target tidak dapat dibaca"}
	case errors.Is(err, ErrTargetConnectionFailed):
		status, apiError = http.StatusBadGateway, &userAPIError{Code: "target_connection_failed", Message: "koneksi ke database target gagal"}
	}
	writeUserJSON(w, status, nil, apiError)
}
