package access

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
)

func (h *GrantHandler) HandleGroupDatabaseAccessList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteMethodError(w, "GET")
		return
	}
	if !h.prepareRequest(w, r, false) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	limit, err := httpapi.PaginationValue(r, "limit", 20)
	if err != nil {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	offset, err := httpapi.PaginationValue(r, "offset", 0)
	if err != nil {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	accesses, err := h.service.ListDatabaseAccess(r.Context(), groupID, limit, offset)
	if err != nil {
		writeGrantServiceError(w, err)
		return
	}
	if accesses == nil {
		accesses = []GroupDatabaseAccess{}
	}
	httpapi.WriteJSON(w, http.StatusOK, accesses, nil)
}

func (h *GrantHandler) HandleGroupDatabaseAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		httpapi.WriteMethodError(w, "GET, PUT, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	databaseID, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || databaseID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		access, err := h.service.GetDatabaseAccess(r.Context(), groupID, databaseID)
		if err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, access, nil)
	case http.MethodPut:
		// Body hanya menentukan tingkat akses group target dan tidak menerima pemberian akses langsung kepada user.
		var input struct {
			AccessLevel AccessLevel `json:"access_level"`
		}
		if !httpapi.DecodeJSON(w, r, &input) {
			return
		}
		access, err := h.service.SetDatabaseAccess(r.Context(), groupID, databaseID, input.AccessLevel)
		if err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, access, nil)
	case http.MethodDelete:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httpapi.WriteBodyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			writeGrantServiceError(w, admin.ErrInvalidGroupInput)
			return
		}
		if err := h.service.RevokeDatabaseAccess(r.Context(), groupID, databaseID); err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"message": "akses group terhadap database dicabut"}, nil)
	}
}

type GrantHandler struct {
	service *GrantService
	csrf    admin.UserCSRFVerifier
}

func NewGrantHandler(service *GrantService, csrf admin.UserCSRFVerifier) *GrantHandler {
	return &GrantHandler{service: service, csrf: csrf}
}
func RegisterGrantRoutes(mux *http.ServeMux, handler *GrantHandler) {
	mux.HandleFunc("/api/admin/groups/{id}/database-access", handler.HandleGroupDatabaseAccessList)
	mux.HandleFunc("/api/admin/groups/{id}/database-access/{database_id}", handler.HandleGroupDatabaseAccess)
}

// Batas body diterapkan sebelum validasi CSRF dan service mengulang otorisasi sebelum menjalankan operasi group.
func (h *GrantHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if r.ContentLength > httpapi.MaxRequestBytes {
		httpapi.WriteBodyError(w, &http.MaxBytesError{Limit: httpapi.MaxRequestBytes})
		return false
	}
	if h == nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		writeGrantServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		httpapi.WriteBodyError(w, err)
		return false
	}
	if errors.Is(err, admin.ErrInvalidUserCSRF) {
		writeGrantServiceError(w, admin.ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	return true
}

func writeGrantServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidAccessLevel):
		httpapi.WriteJSON(w, http.StatusBadRequest, nil, &httpapi.Error{
			Code: "invalid_access_level", Message: "tingkat akses harus View Only atau View + Edit",
		})
	case errors.Is(err, database.ErrDatabaseConnectionNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "database_connection_not_found", Message: "konfigurasi database target tidak ditemukan",
		})
	case errors.Is(err, ErrGroupDatabaseAccessNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "group_database_access_not_found", Message: "akses group terhadap database tidak ditemukan",
		})
	default:
		admin.WriteGroupServiceError(w, err)
	}
}
