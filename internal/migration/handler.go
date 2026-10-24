package migration

import (
	"encoding/json"
	"net/http"
)

type MigrationHandler struct {
	Service *MigrationService
}

func NewMigrationHandler(service *MigrationService) *MigrationHandler {
	return &MigrationHandler{Service: service}
}

// POST /api/migrations/execute
func (h *MigrationHandler) HandleExecuteMigration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MigrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	err := h.Service.ExecuteMigration(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Migrasi berhasil dieksekusi",
	})
}

// GET /api/migrations/history
func (h *MigrationHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userAccess := r.Header.Get("X-User-Access") 

	histories, err := h.Service.GetMigrationHistory(userAccess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(histories)
}