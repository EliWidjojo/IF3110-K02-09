package migration

import (
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/viewer"
	"errors"
	"net/http"
	"strconv"
)

type Handler struct {
	engine *Engine
	csrf   admin.UserCSRFVerifier
}

func NewHandler(engine *Engine, csrf admin.UserCSRFVerifier) *Handler { return &Handler{engine, csrf} }
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/history", h.History)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/execute", h.Execute)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/diff", h.Diff)
}
func (h *Handler) prepare(w http.ResponseWriter, r *http.Request, mutate bool) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || id <= 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "ID database tidak valid"})
		return 0, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if mutate {
		if h.csrf == nil || h.csrf.ValidateCSRF(r) != nil {
			admin.WriteUserServiceError(w, admin.ErrInvalidUserCSRF)
			return 0, false
		}
	}
	return id, true
}
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, false)
	if !ok {
		return
	}
	limit, e1 := httpapi.PaginationValue(r, "limit", 50)
	offset, e2 := httpapi.PaginationValue(r, "offset", 0)
	if e1 != nil || e2 != nil || limit < 1 || limit > 100 || offset < 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "pagination tidak valid"})
		return
	}
	rows, latest, err := h.engine.History(r.Context(), id, limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"attempts": rows, "latest_version": latest, "limit": limit, "offset": offset}, nil)
}
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	if err := h.engine.access.RequireEdit(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	var req MigrationRequest
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	result, err := h.engine.Run(r.Context(), id, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrVersionConflict):
			httpapi.WriteJSON(w, 409, result, &httpapi.Error{Code: "version_conflict", Message: err.Error()})
		case errors.Is(err, ErrMigrationFailed):
			httpapi.WriteJSON(w, 422, result, &httpapi.Error{Code: "migration_failed", Message: "Migration gagal. Lihat status dan SQLSTATE pada history; periksa SQL serta privilege target."})
		default:
			writeError(w, err)
		}
		return
	}
	httpapi.WriteJSON(w, 200, result, nil)
}
func (h *Handler) Diff(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	var desired DesiredSchema
	if !httpapi.DecodeJSON(w, r, &desired) {
		return
	}
	result, err := h.engine.Diff(r.Context(), id, desired)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, result, nil)
}
func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNeedsReview) || errors.Is(err, ErrLegacyHistory) {
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "history_review_required", Message: err.Error()})
		return
	}
	if errors.Is(err, ErrUnsafeTarget) {
		httpapi.WriteJSON(w, 503, nil, &httpapi.Error{Code: "unsafe_target", Message: ErrUnsafeTarget.Error()})
		return
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		httpapi.WriteJSON(w, 503, nil, &httpapi.Error{Code: "outcome_unknown", Message: ErrOutcomeUnknown.Error()})
		return
	}
	var invalid *InputError
	if errors.As(err, &invalid) {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: invalid.Error()})
		return
	}
	if errors.Is(err, ErrInvalidMigration) {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_migration", Message: err.Error()})
		return
	}
	viewer.WriteError(w, err)
}
