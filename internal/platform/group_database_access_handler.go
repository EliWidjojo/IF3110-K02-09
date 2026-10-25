package platform

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
)

func (h *GroupHandler) HandleGroupDatabaseAccessList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeUserMethodError(w, "GET")
		return
	}
	if !h.prepareRequest(w, r, false) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
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
	accesses, err := h.service.ListDatabaseAccess(r.Context(), groupID, limit, offset)
	if err != nil {
		writeGroupServiceError(w, err)
		return
	}
	if accesses == nil {
		accesses = []GroupDatabaseAccess{}
	}
	writeUserJSON(w, http.StatusOK, accesses, nil)
}

func (h *GroupHandler) HandleGroupDatabaseAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeUserMethodError(w, "GET, PUT, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	databaseID, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || databaseID <= 0 {
		writeGroupServiceError(w, ErrInvalidGroupInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		access, err := h.service.GetDatabaseAccess(r.Context(), groupID, databaseID)
		if err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, access, nil)
	case http.MethodPut:
		// Body hanya menentukan tingkat akses group target dan tidak menerima pemberian akses langsung kepada user.
		var input struct {
			AccessLevel AccessLevel `json:"access_level"`
		}
		if !decodeUserJSON(w, r, &input) {
			return
		}
		access, err := h.service.SetDatabaseAccess(r.Context(), groupID, databaseID, input.AccessLevel)
		if err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, access, nil)
	case http.MethodDelete:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeUserBodyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			writeGroupServiceError(w, ErrInvalidGroupInput)
			return
		}
		if err := h.service.RevokeDatabaseAccess(r.Context(), groupID, databaseID); err != nil {
			writeGroupServiceError(w, err)
			return
		}
		writeUserJSON(w, http.StatusOK, map[string]string{"message": "akses group terhadap database dicabut"}, nil)
	}
}
