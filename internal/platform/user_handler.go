package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
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
}

func (h *UserHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
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
			writeUserServiceError(w, ErrInvalidUserInput)
			return
		}
		offset, err := userPaginationValue(r, "offset", 0)
		if err != nil {
			writeUserServiceError(w, ErrInvalidUserInput)
			return
		}
		users, err := h.service.List(r.Context(), limit, offset)
		if err != nil {
			writeUserServiceError(w, err)
			return
		}
		if users == nil {
			users = []User{}
		}
		writeUserJSON(w, http.StatusOK, users, nil)
		return
	}
	// Penanda admin pada body hanya mengatur user target setelah service memverifikasi kewenangan pelaku.
	var body struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		IsPlatformAdmin bool   `json:"is_platform_admin"`
	}
	if !decodeUserJSON(w, r, &body) {
		return
	}
	user, err := h.service.Create(r.Context(), CreateUserInput{
		Username: body.Username, Password: body.Password, IsPlatformAdmin: body.IsPlatformAdmin,
	})
	if err != nil {
		writeUserServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/users/"+strconv.Itoa(user.ID))
	writeUserJSON(w, http.StatusCreated, user, nil)
}

func (h *UserHandler) HandleUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		writeUserMethodError(w, "GET, PATCH")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPatch) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeUserServiceError(w, ErrInvalidUserInput)
		return
	}
	if r.Method == http.MethodGet {
		user, err := h.service.GetByID(r.Context(), id)
		if err != nil {
			writeUserServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, user, nil)
		return
	}
	var input UpdateUserParams
	if !decodeUserJSON(w, r, &input) {
		return
	}
	user, err := h.service.Update(r.Context(), id, input)
	if err != nil {
		writeUserServiceError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, user, nil)
}

func (h *UserHandler) HandleDeactivateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeUserMethodError(w, "POST")
		return
	}
	if !h.prepareRequest(w, r, true) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeUserServiceError(w, ErrInvalidUserInput)
		return
	}
	// Endpoint penonaktifan tidak menerima atribut tambahan yang dapat disalahartikan sebagai identitas pelaku.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeUserBodyError(w, err)
		return
	}
	if len(bytes.TrimSpace(body)) != 0 {
		writeUserServiceError(w, ErrInvalidUserInput)
		return
	}
	if err := h.service.Deactivate(r.Context(), id); err != nil {
		writeUserServiceError(w, err)
		return
	}
	writeUserJSON(w, http.StatusOK, map[string]string{"message": "user dinonaktifkan"}, nil)
}

// Body dibatasi sebelum diproses modul A dan kewenangan diperiksa kembali oleh service sebelum operasi penyimpanan.
func (h *UserHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxUserRequestBytes)
	if r.ContentLength > maxUserRequestBytes {
		writeUserBodyError(w, &http.MaxBytesError{Limit: maxUserRequestBytes})
		return false
	}
	if h == nil {
		writeUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		writeUserServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		writeUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeUserBodyError(w, err)
		return false
	}
	if errors.Is(err, ErrInvalidUserCSRF) {
		writeUserServiceError(w, ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		writeUserServiceError(w, ErrUserDependencyUnavailable)
		return false
	}
	return true
}

const maxUserRequestBytes = 16 * 1024

func decodeUserJSON(w http.ResponseWriter, r *http.Request, input any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeUserJSON(w, http.StatusUnsupportedMediaType, nil, &userAPIError{
			Code: "unsupported_media_type", Message: "Content-Type harus application/json",
		})
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		writeUserBodyError(w, err)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeUserBodyError(w, err)
		return false
	}
	return true
}

func userPaginationValue(r *http.Request, name string, fallback int) (int, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, ErrInvalidUserInput
	}
	return strconv.Atoi(values[0])
}

type userAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeUserJSON(w http.ResponseWriter, status int, data any, apiError *userAPIError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Data  any           `json:"data,omitempty"`
		Error *userAPIError `json:"error,omitempty"`
	}{Data: data, Error: apiError})
}

func writeUserMethodError(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeUserJSON(w, http.StatusMethodNotAllowed, nil, &userAPIError{
		Code: "method_not_allowed", Message: "metode HTTP tidak diizinkan",
	})
}

func writeUserBodyError(w http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeUserJSON(w, http.StatusRequestEntityTooLarge, nil, &userAPIError{
			Code: "request_too_large", Message: "ukuran body maksimum 16 KiB",
		})
		return
	}
	writeUserJSON(w, http.StatusBadRequest, nil, &userAPIError{
		Code: "invalid_request", Message: "body request tidak valid",
	})
}

// Pesan driver dan dependency tidak diteruskan agar detail penyimpanan maupun kredensial tidak bocor ke client.
func writeUserServiceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	apiError := &userAPIError{Code: "internal_error", Message: "operasi user gagal"}
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, apiError = http.StatusUnauthorized, &userAPIError{Code: "unauthenticated", Message: "authentication diperlukan"}
	case errors.Is(err, ErrPlatformAdminRequired):
		status, apiError = http.StatusForbidden, &userAPIError{Code: "forbidden", Message: "akses Platform Admin diperlukan"}
	case errors.Is(err, ErrInvalidUserCSRF):
		status, apiError = http.StatusForbidden, &userAPIError{Code: "invalid_csrf", Message: "validasi CSRF gagal"}
	case errors.Is(err, ErrInvalidUserInput):
		status, apiError = http.StatusBadRequest, &userAPIError{Code: "invalid_input", Message: "data user atau pagination tidak valid"}
	case errors.Is(err, ErrUserNotFound):
		status, apiError = http.StatusNotFound, &userAPIError{Code: "user_not_found", Message: "user tidak ditemukan"}
	case errors.Is(err, ErrUsernameTaken):
		status, apiError = http.StatusConflict, &userAPIError{Code: "username_taken", Message: "username sudah dipakai"}
	case errors.Is(err, ErrUserDependencyUnavailable):
		status, apiError = http.StatusServiceUnavailable, &userAPIError{Code: "dependency_unavailable", Message: "komponen pengelolaan user belum tersedia"}
	}
	writeUserJSON(w, status, nil, apiError)
}
