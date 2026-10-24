package platform

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
)

type GroupHandler struct {
	service *GroupService
	csrf    UserCSRFVerifier
}

// NewGroupHandler memakai kontrak CSRF modul A yang sama dengan endpoint administrasi user.
func NewGroupHandler(service *GroupService, csrf UserCSRFVerifier) *GroupHandler {
	return &GroupHandler{service: service, csrf: csrf}
}

func RegisterGroupRoutes(mux *http.ServeMux, handler *GroupHandler) {
	mux.HandleFunc("/api/admin/groups", handler.HandleGroups)
	mux.HandleFunc("/api/admin/groups/{id}", handler.HandleGroup)
	mux.HandleFunc("/api/admin/groups/{id}/members", handler.HandleGroupMembers)
	mux.HandleFunc("/api/admin/groups/{id}/members/{user_id}", handler.HandleGroupMember)
}

func (h *GroupHandler) HandleGroups(w http.ResponseWriter, r *http.Request) {
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
			writeGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		offset, err := userPaginationValue(r, "offset", 0)
		if err != nil {
			writeGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		groups, err := h.service.List(r.Context(), limit, offset)
		if err != nil {
			writeGroupServiceError(w, err)
			return
		}
		if groups == nil {
			groups = []Group{}
		}
		writeUserJSON(w, http.StatusOK, groups, nil)
		return
	}
	var input CreateGroupParams
	if !decodeUserJSON(w, r, &input) {
		return
	}
	group, err := h.service.Create(r.Context(), input)
	if err != nil {
		writeGroupServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/groups/"+strconv.Itoa(group.ID))
	writeUserJSON(w, http.StatusCreated, group, nil)
}

func (h *GroupHandler) HandleGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeUserMethodError(w, "GET, PATCH, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		group, err := h.service.GetByID(r.Context(), id)
		if err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, group, nil)
	case http.MethodPatch:
		var input UpdateGroupParams
		if !decodeUserJSON(w, r, &input) {
			return
		}
		group, err := h.service.Update(r.Context(), id, input)
		if err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, group, nil)
	case http.MethodDelete:
		// Penghapusan hanya memakai ID target pada route dan tidak menerima atribut atau identitas tambahan pada body.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeUserBodyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			writeGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		if err := h.service.Delete(r.Context(), id); err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, map[string]string{"message": "group dihapus"}, nil)
	}
}

// Batas body diterapkan sebelum validasi CSRF dan service mengulang otorisasi sebelum menjalankan operasi group.
func (h *GroupHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxUserRequestBytes)
	if r.ContentLength > maxUserRequestBytes {
		writeUserBodyError(w, &http.MaxBytesError{Limit: maxUserRequestBytes})
		return false
	}
	if h == nil {
		writeGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		writeGroupServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		writeGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeUserBodyError(w, err)
		return false
	}
	if errors.Is(err, ErrInvalidUserCSRF) {
		writeGroupServiceError(w, ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		writeGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	return true
}

// Format JSON dan error keamanan dipakai bersama dengan handler user tanpa meneruskan detail error penyimpanan.
func writeGroupServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidGroupInput):
		writeUserJSON(w, http.StatusBadRequest, nil, &userAPIError{
			Code: "invalid_input", Message: "data group, user anggota, atau pagination tidak valid",
		})
	case errors.Is(err, ErrGroupMembershipExists):
		writeUserJSON(w, http.StatusConflict, nil, &userAPIError{
			Code: "group_membership_exists", Message: "user sudah menjadi anggota group",
		})
	case errors.Is(err, ErrGroupMembershipNotFound):
		writeUserJSON(w, http.StatusNotFound, nil, &userAPIError{
			Code: "group_membership_not_found", Message: "keanggotaan group tidak ditemukan",
		})
	case errors.Is(err, ErrGroupNotFound):
		writeUserJSON(w, http.StatusNotFound, nil, &userAPIError{
			Code: "group_not_found", Message: "group tidak ditemukan",
		})
	case errors.Is(err, ErrGroupNameTaken):
		writeUserJSON(w, http.StatusConflict, nil, &userAPIError{
			Code: "group_name_taken", Message: "nama group sudah dipakai",
		})
	case errors.Is(err, ErrGroupDependencyUnavailable):
		writeUserJSON(w, http.StatusServiceUnavailable, nil, &userAPIError{
			Code: "dependency_unavailable", Message: "komponen pengelolaan group belum tersedia",
		})
	case errors.Is(err, ErrGroupStorage):
		writeUserJSON(w, http.StatusInternalServerError, nil, &userAPIError{
			Code: "internal_error", Message: "operasi group gagal",
		})
	default:
		writeUserServiceError(w, err)
	}
}
