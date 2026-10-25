package admin

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/httpapi"
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
		httpapi.WriteMethodError(w, "GET, POST")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		limit, err := httpapi.PaginationValue(r, "limit", 20)
		if err != nil {
			WriteGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		offset, err := httpapi.PaginationValue(r, "offset", 0)
		if err != nil {
			WriteGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		groups, err := h.service.List(r.Context(), limit, offset)
		if err != nil {
			WriteGroupServiceError(w, err)
			return
		}
		if groups == nil {
			groups = []Group{}
		}
		httpapi.WriteJSON(w, http.StatusOK, groups, nil)
		return
	}
	var input CreateGroupParams
	if !httpapi.DecodeJSON(w, r, &input) {
		return
	}
	group, err := h.service.Create(r.Context(), input)
	if err != nil {
		WriteGroupServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/groups/"+strconv.Itoa(group.ID))
	httpapi.WriteJSON(w, http.StatusCreated, group, nil)
}

func (h *GroupHandler) HandleGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		httpapi.WriteMethodError(w, "GET, PATCH, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		WriteGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		group, err := h.service.GetByID(r.Context(), id)
		if err != nil {
			WriteGroupServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, group, nil)
	case http.MethodPatch:
		var input UpdateGroupParams
		if !httpapi.DecodeJSON(w, r, &input) {
			return
		}
		group, err := h.service.Update(r.Context(), id, input)
		if err != nil {
			WriteGroupServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, group, nil)
	case http.MethodDelete:
		// Penghapusan hanya memakai ID target pada route dan tidak menerima atribut atau identitas tambahan pada body.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httpapi.WriteBodyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			WriteGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		if err := h.service.Delete(r.Context(), id); err != nil {
			WriteGroupServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"message": "group dihapus"}, nil)
	}
}

// Batas body diterapkan sebelum validasi CSRF dan service mengulang otorisasi sebelum menjalankan operasi group.
func (h *GroupHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if r.ContentLength > httpapi.MaxRequestBytes {
		httpapi.WriteBodyError(w, &http.MaxBytesError{Limit: httpapi.MaxRequestBytes})
		return false
	}
	if h == nil {
		WriteGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		WriteGroupServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		WriteGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		httpapi.WriteBodyError(w, err)
		return false
	}
	if errors.Is(err, ErrInvalidUserCSRF) {
		WriteGroupServiceError(w, ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		WriteGroupServiceError(w, ErrGroupDependencyUnavailable)
		return false
	}
	return true
}

// Format JSON dan error keamanan dipakai bersama dengan handler user tanpa meneruskan detail error penyimpanan.
func WriteGroupServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidGroupInput):
		httpapi.WriteJSON(w, http.StatusBadRequest, nil, &httpapi.Error{
			Code: "invalid_input", Message: "data group, user anggota, database, atau pagination tidak valid",
		})
	case errors.Is(err, ErrGroupMembershipExists):
		httpapi.WriteJSON(w, http.StatusConflict, nil, &httpapi.Error{
			Code: "group_membership_exists", Message: "user sudah menjadi anggota group",
		})
	case errors.Is(err, ErrGroupMembershipNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "group_membership_not_found", Message: "keanggotaan group tidak ditemukan",
		})
	case errors.Is(err, ErrGroupNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "group_not_found", Message: "group tidak ditemukan",
		})
	case errors.Is(err, ErrGroupNameTaken):
		httpapi.WriteJSON(w, http.StatusConflict, nil, &httpapi.Error{
			Code: "group_name_taken", Message: "nama group sudah dipakai",
		})
	case errors.Is(err, ErrGroupDependencyUnavailable):
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, nil, &httpapi.Error{
			Code: "dependency_unavailable", Message: "komponen pengelolaan group belum tersedia",
		})
	case errors.Is(err, ErrGroupStorage):
		httpapi.WriteJSON(w, http.StatusInternalServerError, nil, &httpapi.Error{
			Code: "internal_error", Message: "operasi group gagal",
		})
	default:
		WriteUserServiceError(w, err)
	}
}
