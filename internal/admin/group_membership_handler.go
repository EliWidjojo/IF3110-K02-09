package admin

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/httpapi"
)

func (h *GroupHandler) HandleGroupMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpapi.WriteMethodError(w, "GET, POST")
		return
	}
	if !h.prepareRequest(w, r, r.Method == http.MethodPost) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		WriteGroupServiceError(w, ErrInvalidGroupInput)
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
		members, err := h.service.ListMembers(r.Context(), groupID, limit, offset)
		if err != nil {
			WriteGroupServiceError(w, err)
			return
		}
		if members == nil {
			members = []User{}
		}
		httpapi.WriteJSON(w, http.StatusOK, members, nil)
		return
	}
	// UserID pada body hanya menunjuk calon anggota dan tidak digunakan sebagai identitas pelaku administrasi.
	var input struct {
		UserID int `json:"user_id"`
	}
	if !httpapi.DecodeJSON(w, r, &input) {
		return
	}
	membership, err := h.service.AddMember(r.Context(), groupID, input.UserID)
	if err != nil {
		WriteGroupServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/groups/"+strconv.Itoa(groupID)+"/members/"+strconv.Itoa(membership.UserID))
	httpapi.WriteJSON(w, http.StatusCreated, membership, nil)
}

func (h *GroupHandler) HandleGroupMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpapi.WriteMethodError(w, "DELETE")
		return
	}
	if !h.prepareRequest(w, r, true) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		WriteGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	userID, err := strconv.Atoi(r.PathValue("user_id"))
	if err != nil || userID <= 0 {
		WriteGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpapi.WriteBodyError(w, err)
		return
	}
	if len(bytes.TrimSpace(body)) != 0 {
		WriteGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	if err := h.service.RemoveMember(r.Context(), groupID, userID); err != nil {
		WriteGroupServiceError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"message": "user dikeluarkan dari group"}, nil)
}
