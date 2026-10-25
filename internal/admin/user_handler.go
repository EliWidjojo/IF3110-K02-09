package admin

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/httpapi"
)

var ErrInvalidUserCSRF = errors.New("validasi CSRF gagal")

// UserCSRFVerifier menggunakan perlindungan modul A yang terikat session dan mengembalikan ErrInvalidUserCSRF untuk request tidak sah.
type UserCSRFVerifier interface {
	ValidateCSRF(*http.Request) error
}

type UserHandler struct {
	service *UserService
	csrf    UserCSRFVerifier
}

func NewUserHandler(service *UserService, csrf UserCSRFVerifier) *UserHandler {
	return &UserHandler{service: service, csrf: csrf}
}

// RegisterUserRoutes memasang route pada mux milik aplikasi tanpa membuat server atau mekanisme authentication baru.
func RegisterUserRoutes(mux *http.ServeMux, handler *UserHandler) {
	mux.HandleFunc("/api/admin/users", handler.HandleUsers)
	mux.HandleFunc("/api/admin/users/{id}", handler.HandleUser)
	mux.HandleFunc("/api/admin/users/{id}/deactivate", handler.HandleDeactivateUser)
	mux.HandleFunc("/api/admin/users/{id}/revoke-sessions", handler.HandleRevokeSessions)
}

func (h *UserHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpapi.WriteMethodError(w, "GET, POST")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		limit, err := httpapi.PaginationValue(r, "limit", 20)
		if err != nil {
			WriteUserServiceError(w, ErrInvalidUserInput)
			return
		}
		offset, err := httpapi.PaginationValue(r, "offset", 0)
		if err != nil {
			WriteUserServiceError(w, ErrInvalidUserInput)
			return
		}
		users, err := h.service.List(r.Context(), limit, offset)
		if err != nil {
			WriteUserServiceError(w, err)
			return
		}
		if users == nil {
			users = []User{}
		}
		httpapi.WriteJSON(w, http.StatusOK, users, nil)
		return
	}
	// Penanda admin pada body hanya mengatur user target setelah service memverifikasi kewenangan pelaku.
	var body struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		IsPlatformAdmin bool   `json:"is_platform_admin"`
	}
	if !httpapi.DecodeJSON(w, r, &body) {
		return
	}
	user, err := h.service.Create(r.Context(), CreateUserInput{
		Username: body.Username, Password: body.Password, IsPlatformAdmin: body.IsPlatformAdmin,
	})
	if err != nil {
		WriteUserServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/users/"+strconv.Itoa(user.ID))
	httpapi.WriteJSON(w, http.StatusCreated, user, nil)
}

func (h *UserHandler) HandleUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		httpapi.WriteMethodError(w, "GET, PATCH")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPatch) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		WriteUserServiceError(w, ErrInvalidUserInput)
		return
	}
	if r.Method == http.MethodGet {
		user, err := h.service.GetByID(r.Context(), id)
		if err != nil {
			WriteUserServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, user, nil)
		return
	}
	var input UpdateUserParams
	if !httpapi.DecodeJSON(w, r, &input) {
		return
	}
	user, err := h.service.Update(r.Context(), id, input)
	if err != nil {
		WriteUserServiceError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, user, nil)
}

func (h *UserHandler) HandleDeactivateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteMethodError(w, "POST")
		return
	}
	if !h.prepareRequest(w, r, true) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		WriteUserServiceError(w, ErrInvalidUserInput)
		return
	}
	// Endpoint penonaktifan tidak menerima atribut tambahan yang dapat disalahartikan sebagai identitas pelaku.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpapi.WriteBodyError(w, err)
		return
	}
	if len(bytes.TrimSpace(body)) != 0 {
		WriteUserServiceError(w, ErrInvalidUserInput)
		return
	}
	if err := h.service.Deactivate(r.Context(), id); err != nil {
		WriteUserServiceError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"message": "user dinonaktifkan"}, nil)
}

// Body dibatasi sebelum diproses modul A dan kewenangan diperiksa kembali oleh service sebelum operasi penyimpanan.
func (h *UserHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if r.ContentLength > httpapi.MaxRequestBytes {
		httpapi.WriteBodyError(w, &http.MaxBytesError{Limit: httpapi.MaxRequestBytes})
		return false
	}
	if h == nil {
		WriteUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		WriteUserServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		WriteUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		httpapi.WriteBodyError(w, err)
		return false
	}
	if errors.Is(err, ErrInvalidUserCSRF) {
		WriteUserServiceError(w, ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		WriteUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	return true
}

// Pesan driver dan dependency tidak diteruskan agar detail penyimpanan maupun kredensial tidak bocor ke client.
func WriteUserServiceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &httpapi.Error{Code: "internal_error", Message: "operasi user gagal"}
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, apiError = http.StatusUnauthorized, &httpapi.Error{Code: "unauthenticated", Message: "authentication diperlukan"}
	case errors.Is(err, ErrPlatformAdminRequired):
		status, apiError = http.StatusForbidden, &httpapi.Error{Code: "forbidden", Message: "akses Platform Admin diperlukan"}
	case errors.Is(err, ErrInvalidUserCSRF):
		status, apiError = http.StatusForbidden, &httpapi.Error{Code: "invalid_csrf", Message: "validasi CSRF gagal"}
	case errors.Is(err, ErrInvalidUserInput):
		status, apiError = http.StatusBadRequest, &httpapi.Error{Code: "invalid_input", Message: "data user atau pagination tidak valid"}
	case errors.Is(err, ErrUserNotFound):
		status, apiError = http.StatusNotFound, &httpapi.Error{Code: "user_not_found", Message: "user tidak ditemukan"}
	case errors.Is(err, ErrUsernameTaken):
		status, apiError = http.StatusConflict, &httpapi.Error{Code: "username_taken", Message: "username sudah dipakai"}
	case errors.Is(err, ErrUserDependencyUnavailable):
		status, apiError = http.StatusServiceUnavailable, &httpapi.Error{Code: "dependency_unavailable", Message: "komponen pengelolaan user belum tersedia"}
	}
	httpapi.WriteJSON(w, status, nil, apiError)
}

func (h *UserHandler) HandleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpapi.WriteMethodError(w, "POST")
		return
	}
	if !h.prepareRequest(w, r, true) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		WriteUserServiceError(w, ErrInvalidUserInput)
		return
	}
	if err = h.service.RevokeSessions(r.Context(), id); err != nil {
		WriteUserServiceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"message": "session dicabut"}, nil)
}
