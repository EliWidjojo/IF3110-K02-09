package migration

import (
	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/auth"
	"net/http"
)

type MigrationHandler struct {
	Service    *MigrationService
	DatabaseID string
}

func NewMigrationHandler(service *MigrationService) *MigrationHandler {
	return &MigrationHandler{Service: service, DatabaseID: "default"}
}

func NewDatabaseHandler(service *MigrationService, id string) *MigrationHandler {
	return &MigrationHandler{Service: service, DatabaseID: id}
}
func (h *MigrationHandler) authorize(w http.ResponseWriter, r *http.Request, action access.Action) (access.Principal, bool) {
	p, ok := access.FromContext(r.Context())
	if !ok {
		auth.Error(w, 401, "authentication required")
		return p, false
	}
	if !access.Allowed(p, h.DatabaseID, action) {
		auth.Error(w, 403, "database access denied")
		return p, false
	}
	return p, true
}

func (h *MigrationHandler) HandleExecuteMigration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p, ok := h.authorize(w, r, access.WriteMigration)
	if !ok {
		return
	}
	var req MigrationRequest
	if !auth.Decode(w, r, &req) {
		return
	}
	req.UserID, req.UserAccess = p.ID, string(p.Databases[h.DatabaseID])

	err := h.Service.ExecuteMigrationContext(r.Context(), req)
	if err != nil {
		auth.Error(w, http.StatusBadRequest, "migration failed; check version, SQL, and DBMS transaction limitations")
		return
	}

	auth.JSON(w, http.StatusOK, map[string]any{"message": "Migrasi berhasil dieksekusi", "atomic": !req.NonAtomicDDL})
}

func (h *MigrationHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p, ok := h.authorize(w, r, access.ViewHistory)
	if !ok {
		return
	}

	histories, err := h.Service.GetMigrationHistoryContext(r.Context(), string(p.Databases[h.DatabaseID]))
	if err != nil {
		auth.Error(w, http.StatusServiceUnavailable, "migration history unavailable")
		return
	}

	auth.JSON(w, http.StatusOK, histories)
}
