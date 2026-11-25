package migration

import (
	"errors"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/viewer"
)

type Handler struct {
	engine *Engine
	csrf   admin.UserCSRFVerifier
}

func NewHandler(engine *Engine, csrf admin.UserCSRFVerifier) *Handler { return &Handler{engine, csrf} }

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	// Existing endpoints
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/history", h.History)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/diff", h.Diff)

	// Workflow & Approval endpoints
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/proposals", h.SubmitProposal)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/proposals", h.ListProposals)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/proposals/{proposal_id}", h.GetProposal)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/proposals/{proposal_id}/approve", h.ApproveProposal)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/proposals/{proposal_id}/reject", h.RejectProposal)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/proposals/{proposal_id}/run", h.RunProposal)

	// Rollback endpoint
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/rollback", h.Rollback)
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

func (h *Handler) SubmitProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	var req MigrationRequest
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	prop, err := h.engine.SubmitProposal(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, prop, nil)
}

func (h *Handler) ListProposals(w http.ResponseWriter, r *http.Request) {
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
	status := r.URL.Query().Get("status")
	items, err := h.engine.ListProposals(r.Context(), id, status, limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"proposals": items, "limit": limit, "offset": offset}, nil)
}

func (h *Handler) GetProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, false)
	if !ok {
		return
	}
	propID, err := strconv.ParseInt(r.PathValue("proposal_id"), 10, 64)
	if err != nil || propID <= 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "ID proposal tidak valid"})
		return
	}
	prop, err := h.engine.GetProposal(r.Context(), id, propID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, prop, nil)
}

func (h *Handler) ApproveProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	propID, err := strconv.ParseInt(r.PathValue("proposal_id"), 10, 64)
	if err != nil || propID <= 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "ID proposal tidak valid"})
		return
	}
	prop, err := h.engine.ApproveProposal(r.Context(), id, propID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, prop, nil)
}

func (h *Handler) RejectProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	propID, err := strconv.ParseInt(r.PathValue("proposal_id"), 10, 64)
	if err != nil || propID <= 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "ID proposal tidak valid"})
		return
	}
	var req ApprovalActionRequest
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	prop, err := h.engine.RejectProposal(r.Context(), id, propID, req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, prop, nil)
}

func (h *Handler) RunProposal(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	propID, err := strconv.ParseInt(r.PathValue("proposal_id"), 10, 64)
	if err != nil || propID <= 0 {
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: "ID proposal tidak valid"})
		return
	}
	prop, err := h.engine.QueueApprovedProposal(r.Context(), id, propID)
	if err != nil {
		writeError(w, err)
		return
	}
	statusCode := 200
	if prop.Status == StatusQueued {
		statusCode = 202
	}
	httpapi.WriteJSON(w, statusCode, prop, nil)
}

func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	id, ok := h.prepare(w, r, true)
	if !ok {
		return
	}
	var req RollbackRequest
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	res, err := h.engine.Rollback(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, res, nil)
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
	switch {
	case errors.Is(err, ErrVersionConflict):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "version_conflict", Message: err.Error()})
	case errors.Is(err, ErrMigrationFailed):
		httpapi.WriteJSON(w, 422, nil, &httpapi.Error{Code: "migration_failed", Message: "Migration gagal. Lihat status dan SQLSTATE pada history; periksa SQL serta privilege target."})
	case errors.Is(err, ErrNeedsReview) || errors.Is(err, ErrLegacyHistory):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "history_review_required", Message: err.Error()})
	case errors.Is(err, ErrInvalidState) || errors.Is(err, ErrNotLatestVersion) || errors.Is(err, ErrCannotRollbackState):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "invalid_state", Message: err.Error()})
	case errors.Is(err, ErrSelfApprovalBlocked):
		httpapi.WriteJSON(w, 403, nil, &httpapi.Error{Code: "self_approval_blocked", Message: err.Error()})
	case errors.Is(err, ErrProposalNotFound) || errors.Is(err, ErrAttemptNotFound):
		httpapi.WriteJSON(w, 404, nil, &httpapi.Error{Code: "not_found", Message: err.Error()})
	case errors.Is(err, ErrChecksumMismatch):
		httpapi.WriteJSON(w, 422, nil, &httpapi.Error{Code: "checksum_mismatch", Message: err.Error()})
	case errors.Is(err, ErrMissingRollbackSQL):
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "missing_rollback_sql", Message: err.Error()})
	case errors.Is(err, ErrUnsafeTarget):
		httpapi.WriteJSON(w, 503, nil, &httpapi.Error{Code: "unsafe_target", Message: ErrUnsafeTarget.Error()})
	case errors.Is(err, ErrOutcomeUnknown):
		httpapi.WriteJSON(w, 503, nil, &httpapi.Error{Code: "outcome_unknown", Message: ErrOutcomeUnknown.Error()})
	case errors.Is(err, ErrInvalidMigration):
		httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_migration", Message: err.Error()})
	default:
		var invalid *InputError
		if errors.As(err, &invalid) {
			httpapi.WriteJSON(w, 400, nil, &httpapi.Error{Code: "invalid_input", Message: invalid.Error()})
			return
		}
		viewer.WriteError(w, err)
	}
}