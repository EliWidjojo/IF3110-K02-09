package viewer

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
)

// Seluruh endpoint viewer hanya membaca metadata sehingga tidak memerlukan perubahan state maupun token CSRF.
type ViewerHandler struct {
	service *ViewerService
}

func NewViewerHandler(service *ViewerService) *ViewerHandler {
	return &ViewerHandler{service: service}
}

func RegisterViewerRoutes(mux *http.ServeMux, handler *ViewerHandler) {
	mux.HandleFunc("/api/viewer/databases", handler.HandleDatabases)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas", handler.HandleSchemas)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables", handler.HandleTables)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables/{table}", handler.HandleTableMetadata)
	mux.HandleFunc("/api/viewer/databases/{database_id}/schemas/{schema}/tables/{table}/data", handler.HandleTableData)
}

func (h *ViewerHandler) HandleDatabases(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databases, err := h.service.ListDatabases(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, databases, nil)
}

func (h *ViewerHandler) HandleSchemas(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	schemas, err := h.service.ListSchemas(r.Context(), databaseID)
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, schemas, nil)
}

func (h *ViewerHandler) HandleTables(w http.ResponseWriter, r *http.Request) {
	if !h.prepareRequest(w, r) {
		return
	}
	databaseID, err := DatabaseID(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	// PathValue sudah di-decode oleh router dan nama schema diteruskan sebagai nilai parameter, bukan identifier SQL.
	tables, err := h.service.ListTables(r.Context(), databaseID, r.PathValue("schema"))
	if err != nil {
		WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, tables, nil)
}

func (h *ViewerHandler) prepareRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		httpapi.WriteMethodError(w, "GET")
		return false
	}
	if h == nil || h.service == nil {
		WriteError(w, ErrViewerDependencyUnavailable)
		return false
	}
	return true
}

func DatabaseID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || id <= 0 {
		return 0, ErrInvalidViewerInput
	}
	return id, nil
}

// Respons memakai pesan tetap agar error driver, detail koneksi, dan query tidak diteruskan kepada client.
func WriteError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &httpapi.Error{Code: "internal_error", Message: "operasi viewer gagal"}
	switch {
	case errors.Is(err, database.ErrInternalDatabaseTarget):
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "internal_database_target", Message: database.ErrInternalDatabaseTarget.Error()})
		return
	case errors.Is(err, ErrViewerResultTooLarge):
		httpapi.WriteJSON(w, 413, nil, &httpapi.Error{Code: "result_too_large", Message: ErrViewerResultTooLarge.Error()})
		return
	case errors.Is(err, admin.ErrUnauthenticated), errors.Is(err, admin.ErrUserNotFound):
		status, apiError = http.StatusUnauthorized, &httpapi.Error{Code: "unauthenticated", Message: "authentication diperlukan"}
	case errors.Is(err, access.ErrDatabaseAccessDenied), errors.Is(err, access.ErrInactiveUser):
		status, apiError = http.StatusForbidden, &httpapi.Error{Code: "database_access_denied", Message: "akses database ditolak"}
	case errors.Is(err, ErrInvalidViewerInput), errors.Is(err, access.ErrInvalidAccessInput), errors.Is(err, database.ErrInvalidDatabaseConnection):
		status, apiError = http.StatusBadRequest, &httpapi.Error{Code: "invalid_input", Message: "ID database, nama schema, nama tabel, atau pagination tidak valid"}
	case errors.Is(err, ErrViewerTableNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "table_not_found", Message: "tabel tidak ditemukan atau tidak ditampilkan"}
	case errors.Is(err, ErrViewerSchemaNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "schema_not_found", Message: "schema tidak ditemukan atau tidak ditampilkan"}
	case errors.Is(err, database.ErrDatabaseConnectionNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "database_connection_not_found", Message: "konfigurasi database tidak ditemukan"}
	case errors.Is(err, ErrViewerDependencyUnavailable), errors.Is(err, access.ErrAccessDependencyUnavailable),
		errors.Is(err, database.ErrTargetManagerUnavailable), errors.Is(err, database.ErrTargetManagerClosed), errors.Is(err, database.ErrTargetDriverDebug),
		errors.Is(err, database.ErrDatabaseEncryptionKey), errors.Is(err, database.ErrDatabaseDecryption):
		status, apiError = http.StatusServiceUnavailable, &httpapi.Error{Code: "dependency_unavailable", Message: "komponen viewer atau koneksi belum dapat digunakan"}
	case errors.Is(err, context.DeadlineExceeded):
		status, apiError = http.StatusGatewayTimeout, &httpapi.Error{Code: "viewer_timeout", Message: "batas waktu pembacaan database terlampaui"}
	case errors.Is(err, context.Canceled):
		status, apiError = http.StatusRequestTimeout, &httpapi.Error{Code: "request_canceled", Message: "request dibatalkan"}
	case errors.Is(err, ErrViewerMetadata):
		status, apiError = http.StatusBadGateway, &httpapi.Error{Code: "metadata_query_failed", Message: "metadata database target tidak dapat dibaca"}
	case errors.Is(err, database.ErrTargetConnectionFailed):
		status, apiError = http.StatusBadGateway, &httpapi.Error{Code: "target_connection_failed", Message: "koneksi ke database target gagal"}
	case errors.Is(err, ErrViewerTableData):
		apiError = &httpapi.Error{Code: "table_data_failed", Message: "data tabel tidak dapat disajikan"}
	}
	httpapi.WriteJSON(w, status, nil, apiError)
}
