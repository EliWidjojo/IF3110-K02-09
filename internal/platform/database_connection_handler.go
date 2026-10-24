package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

type DatabaseConnectionHandler struct {
	service *DatabaseConnectionService
	csrf    UserCSRFVerifier
}

func NewDatabaseConnectionHandler(service *DatabaseConnectionService, csrf UserCSRFVerifier) *DatabaseConnectionHandler {
	return &DatabaseConnectionHandler{service: service, csrf: csrf}
}

func RegisterDatabaseConnectionRoutes(mux *http.ServeMux, handler *DatabaseConnectionHandler) {
	mux.HandleFunc("/api/admin/databases", handler.HandleDatabases)
	mux.HandleFunc("/api/admin/databases/{id}", handler.HandleDatabase)
	mux.HandleFunc("/api/admin/databases/{id}/check-connection", handler.HandleCheckConnection)
}

// Input password membedakan atribut yang tidak dikirim dari penggantian eksplisit dan menolak null agar maksud pembaruan tidak ambigu.
type databasePasswordInput struct {
	value    string
	provided bool
}

func (p *databasePasswordInput) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalidDatabaseConnection
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrInvalidDatabaseConnection
	}
	p.value, p.provided = value, true
	return nil
}

func (databasePasswordInput) MarshalJSON() ([]byte, error) { return []byte("null"), nil }
func (databasePasswordInput) String() string               { return "[password disembunyikan]" }
func (databasePasswordInput) GoString() string             { return "[password disembunyikan]" }

func (h *DatabaseConnectionHandler) HandleDatabases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeUserMethodError(w, "GET, POST")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		limit, err := userPaginationValue(r, "limit", 20)
		if err != nil {
			writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
			return
		}
		offset, err := userPaginationValue(r, "offset", 0)
		if err != nil {
			writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
			return
		}
		connections, err := h.service.List(r.Context(), limit, offset)
		if err != nil {
			writeDatabaseConnectionError(w, err)
			return
		}
		if connections == nil {
			connections = []DatabaseConnection{}
		}
		writeUserJSON(w, http.StatusOK, connections, nil)
		return
	}
	var body struct {
		CreateDatabaseConnectionParams
		Password databasePasswordInput `json:"password"`
	}
	if !decodeUserJSON(w, r, &body) {
		return
	}
	if !body.Password.provided {
		writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
		return
	}
	params := body.CreateDatabaseConnectionParams
	params.Password = body.Password.value
	connection, err := h.service.Create(r.Context(), params)
	if err != nil {
		writeDatabaseConnectionError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/databases/"+strconv.Itoa(connection.ID))
	writeUserJSON(w, http.StatusCreated, connection, nil)
}

func (h *DatabaseConnectionHandler) HandleDatabase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeUserMethodError(w, "GET, PATCH, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
		return
	}
	switch r.Method {
	case http.MethodGet:
		connection, err := h.service.GetByID(r.Context(), id)
		if err != nil {
			writeDatabaseConnectionError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, connection, nil)
	case http.MethodPatch:
		var body struct {
			UpdateDatabaseConnectionParams
			Password databasePasswordInput `json:"password"`
		}
		if !decodeUserJSON(w, r, &body) {
			return
		}
		params := body.UpdateDatabaseConnectionParams
		if body.Password.provided {
			params.Password = &body.Password.value
		}
		connection, err := h.service.Update(r.Context(), id, params)
		if err != nil {
			writeDatabaseConnectionError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, connection, nil)
	case http.MethodDelete:
		if !emptyDatabaseRequestBody(w, r) {
			return
		}
		if err := h.service.Delete(r.Context(), id); err != nil {
			writeDatabaseConnectionError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, map[string]string{"message": "konfigurasi database dihapus"}, nil)
	}
}

func (h *DatabaseConnectionHandler) HandleCheckConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeUserMethodError(w, "POST")
		return
	}
	if !h.prepareRequest(w, r, true) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
		return
	}
	if !emptyDatabaseRequestBody(w, r) {
		return
	}
	if err := h.service.CheckConnectivity(r.Context(), id); err != nil {
		writeDatabaseConnectionError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, struct {
		DatabaseID int  `json:"database_id"`
		Connected  bool `json:"connected"`
	}{DatabaseID: id, Connected: true}, nil)
}

func (h *DatabaseConnectionHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxUserRequestBytes)
	if r.ContentLength > maxUserRequestBytes {
		writeUserBodyError(w, &http.MaxBytesError{Limit: maxUserRequestBytes})
		return false
	}
	if h == nil {
		writeDatabaseConnectionError(w, ErrDatabaseConnectionDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		writeDatabaseConnectionError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		writeDatabaseConnectionError(w, ErrDatabaseConnectionDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeUserBodyError(w, err)
		return false
	}
	if errors.Is(err, ErrInvalidUserCSRF) {
		writeDatabaseConnectionError(w, ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		writeDatabaseConnectionError(w, ErrDatabaseConnectionDependencyUnavailable)
		return false
	}
	return true
}

func emptyDatabaseRequestBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeUserBodyError(w, err)
		return false
	}
	if len(bytes.TrimSpace(body)) != 0 {
		writeDatabaseConnectionError(w, ErrInvalidDatabaseConnection)
		return false
	}
	return true
}

// Respons tidak meneruskan pesan driver, ciphertext, atau objek error internal yang mungkin menyimpan rahasia.
func writeDatabaseConnectionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &userAPIError{Code: "internal_error", Message: "operasi konfigurasi database gagal"}
	switch {
	case errors.Is(err, ErrUnauthenticated), errors.Is(err, ErrPlatformAdminRequired), errors.Is(err, ErrInvalidUserCSRF):
		writeUserServiceError(w, err)
		return
	case errors.Is(err, ErrDatabaseConnectionNotFound):
		status, apiError = http.StatusNotFound, &userAPIError{Code: "database_connection_not_found", Message: "konfigurasi database tidak ditemukan"}
	case errors.Is(err, ErrInvalidDatabaseConnection):
		status, apiError = http.StatusBadRequest, &userAPIError{Code: "invalid_input", Message: "parameter PostgreSQL, TLS, password, atau pagination tidak valid"}
	case errors.Is(err, ErrDatabaseConnectionNameTaken):
		status, apiError = http.StatusConflict, &userAPIError{Code: "database_connection_name_taken", Message: "nama konfigurasi database sudah dipakai"}
	case errors.Is(err, ErrDatabaseConnectionReferenced):
		status, apiError = http.StatusConflict, &userAPIError{Code: "database_connection_referenced", Message: "konfigurasi database masih dirujuk data lain"}
	case errors.Is(err, ErrDatabaseConnectionDependencyUnavailable), errors.Is(err, ErrDatabaseEncryptionKey),
		errors.Is(err, ErrDatabaseDecryption), errors.Is(err, ErrTargetManagerUnavailable), errors.Is(err, ErrTargetManagerClosed), errors.Is(err, ErrTargetDriverDebug):
		status, apiError = http.StatusServiceUnavailable, &userAPIError{Code: "dependency_unavailable", Message: "komponen atau kredensial koneksi belum dapat digunakan"}
	case errors.Is(err, context.DeadlineExceeded):
		status, apiError = http.StatusGatewayTimeout, &userAPIError{Code: "connection_timeout", Message: "batas waktu operasi database terlampaui"}
	case errors.Is(err, context.Canceled):
		status, apiError = http.StatusRequestTimeout, &userAPIError{Code: "request_canceled", Message: "request dibatalkan"}
	case errors.Is(err, ErrTargetConnectionFailed):
		status, apiError = http.StatusBadGateway, &userAPIError{Code: "target_connection_failed", Message: "koneksi ke database target gagal"}
	}
	writeUserJSON(w, status, nil, apiError)
}
