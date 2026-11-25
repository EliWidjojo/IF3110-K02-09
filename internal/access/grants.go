package access

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"

	"github.com/lib/pq"
)

// Model izin database.

// AccessLevel menyatakan izin pada database tertentu dan terpisah dari peran Platform Admin.
type AccessLevel = Level

const (
	AccessViewOnly AccessLevel = View
	AccessViewEdit AccessLevel = Edit
)

// GroupDatabaseAccess mengikat izin ke pasangan group dan database, bukan langsung ke user.
type GroupDatabaseAccess struct {
	GroupID     int         `json:"group_id"`
	DatabaseID  int         `json:"database_id"`
	AccessLevel AccessLevel `json:"access_level"`
}

// Handler HTTP pemberian dan pencabutan izin.

func (h *GrantHandler) HandleGroupDatabaseAccessList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteMethodError(w, "GET")
		return
	}
	if !h.prepareRequest(w, r, false) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	limit, err := httpapi.PaginationValue(r, "limit", 20)
	if err != nil {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	offset, err := httpapi.PaginationValue(r, "offset", 0)
	if err != nil {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	accesses, err := h.service.ListDatabaseAccess(r.Context(), groupID, limit, offset)
	if err != nil {
		writeGrantServiceError(w, err)
		return
	}
	if accesses == nil {
		accesses = []GroupDatabaseAccess{}
	}
	httpapi.WriteJSON(w, http.StatusOK, accesses, nil)
}

func (h *GrantHandler) HandleGroupDatabaseAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		httpapi.WriteMethodError(w, "GET, PUT, DELETE")
		return
	}
	if !h.prepareRequest(w, r, r.Method != http.MethodGet) {
		return
	}
	groupID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || groupID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	databaseID, err := strconv.Atoi(r.PathValue("database_id"))
	if err != nil || databaseID <= 0 {
		writeGrantServiceError(w, admin.ErrInvalidGroupInput)
		return
	}
	switch r.Method {
	case http.MethodGet:
		access, err := h.service.GetDatabaseAccess(r.Context(), groupID, databaseID)
		if err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, access, nil)
	case http.MethodPut:
		// Body hanya menentukan tingkat akses group target dan tidak menerima pemberian akses langsung kepada user.
		var input struct {
			AccessLevel AccessLevel `json:"access_level"`
		}
		if !httpapi.DecodeJSON(w, r, &input) {
			return
		}
		access, err := h.service.SetDatabaseAccess(r.Context(), groupID, databaseID, input.AccessLevel)
		if err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, access, nil)
	case http.MethodDelete:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httpapi.WriteBodyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			writeGrantServiceError(w, admin.ErrInvalidGroupInput)
			return
		}
		if err := h.service.RevokeDatabaseAccess(r.Context(), groupID, databaseID); err != nil {
			writeGrantServiceError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"message": "akses group terhadap database dicabut"}, nil)
	}
}

type GrantHandler struct {
	service *GrantService
	csrf    admin.UserCSRFVerifier
}

func NewGrantHandler(service *GrantService, csrf admin.UserCSRFVerifier) *GrantHandler {
	return &GrantHandler{service: service, csrf: csrf}
}
func RegisterGrantRoutes(mux *http.ServeMux, handler *GrantHandler) {
	mux.HandleFunc("/api/admin/groups/{id}/database-access", handler.HandleGroupDatabaseAccessList)
	mux.HandleFunc("/api/admin/groups/{id}/database-access/{database_id}", handler.HandleGroupDatabaseAccess)
}

// Batas body diterapkan sebelum validasi CSRF dan service mengulang otorisasi sebelum menjalankan operasi group.
func (h *GrantHandler) prepareRequest(w http.ResponseWriter, r *http.Request, changesState bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	if r.ContentLength > httpapi.MaxRequestBytes {
		httpapi.WriteBodyError(w, &http.MaxBytesError{Limit: httpapi.MaxRequestBytes})
		return false
	}
	if h == nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	if err := h.service.requireAdmin(r.Context()); err != nil {
		writeGrantServiceError(w, err)
		return false
	}
	if !changesState {
		return true
	}
	if h.csrf == nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	err := h.csrf.ValidateCSRF(r)
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		httpapi.WriteBodyError(w, err)
		return false
	}
	if errors.Is(err, admin.ErrInvalidUserCSRF) {
		writeGrantServiceError(w, admin.ErrInvalidUserCSRF)
		return false
	}
	if err != nil {
		writeGrantServiceError(w, admin.ErrGroupDependencyUnavailable)
		return false
	}
	return true
}

func writeGrantServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidAccessLevel):
		httpapi.WriteJSON(w, http.StatusBadRequest, nil, &httpapi.Error{
			Code: "invalid_access_level", Message: "tingkat akses harus View Only atau View + Edit",
		})
	case errors.Is(err, database.ErrDatabaseConnectionNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "database_connection_not_found", Message: "konfigurasi database target tidak ditemukan",
		})
	case errors.Is(err, ErrGroupDatabaseAccessNotFound):
		httpapi.WriteJSON(w, http.StatusNotFound, nil, &httpapi.Error{
			Code: "group_database_access_not_found", Message: "akses group terhadap database tidak ditemukan",
		})
	default:
		admin.WriteGroupServiceError(w, err)
	}
}

// Service dan validasi izin.

// GrantStore memisahkan penyimpanan izin dari CRUD group dan keanggotaan yang dimiliki package admin.
type GrantStore interface {
	SetDatabaseAccess(context.Context, int, int, AccessLevel) (GroupDatabaseAccess, error)
	GetDatabaseAccess(context.Context, int, int) (GroupDatabaseAccess, error)
	ListDatabaseAccess(context.Context, int, int, int) ([]GroupDatabaseAccess, error)
	RevokeDatabaseAccess(context.Context, int, int) error
}
type GrantService struct {
	store GrantStore
	admin admin.PlatformAdminAuthorizer
}

func NewGrantService(store GrantStore, admin admin.PlatformAdminAuthorizer) *GrantService {
	return &GrantService{store: store, admin: admin}
}
func (s *GrantService) requireAdmin(ctx context.Context) error {
	if s == nil || s.store == nil || s.admin == nil {
		return admin.ErrGroupDependencyUnavailable
	}
	err := s.admin.RequirePlatformAdmin(ctx)
	if errors.Is(err, admin.ErrUserDependencyUnavailable) {
		return admin.ErrGroupDependencyUnavailable
	}
	return err
}

func (s *GrantService) SetDatabaseAccess(ctx context.Context, groupID, databaseID int, level AccessLevel) (GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return GroupDatabaseAccess{}, err
	}
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	if !validAccessLevel(level) {
		return GroupDatabaseAccess{}, ErrInvalidAccessLevel
	}
	return s.store.SetDatabaseAccess(ctx, groupID, databaseID, level)
}

func (s *GrantService) GetDatabaseAccess(ctx context.Context, groupID, databaseID int) (GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return GroupDatabaseAccess{}, err
	}
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	return s.store.GetDatabaseAccess(ctx, groupID, databaseID)
}

func (s *GrantService) ListDatabaseAccess(ctx context.Context, groupID, limit, offset int) ([]GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, admin.ErrInvalidGroupInput
	}
	return s.store.ListDatabaseAccess(ctx, groupID, limit, offset)
}

func (s *GrantService) RevokeDatabaseAccess(ctx context.Context, groupID, databaseID int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if groupID <= 0 || databaseID <= 0 {
		return admin.ErrInvalidGroupInput
	}
	return s.store.RevokeDatabaseAccess(ctx, groupID, databaseID)
}

// Repository izin group terhadap database.

var (
	ErrGroupDatabaseAccessNotFound = errors.New("akses group terhadap database tidak ditemukan")
	ErrInvalidAccessLevel          = errors.New("tingkat akses harus View Only atau View + Edit")
)

type GrantRepository struct{ db *sql.DB }

func NewGrantRepository(db *sql.DB) *GrantRepository { return &GrantRepository{db: db} }

// SetDatabaseAccess memakai upsert pada primary key gabungan agar pemberian dan perubahan akses tidak menghasilkan duplikasi.
func (r *GrantRepository) SetDatabaseAccess(ctx context.Context, groupID, databaseID int, level AccessLevel) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	if !validAccessLevel(level) {
		return GroupDatabaseAccess{}, ErrInvalidAccessLevel
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memulai pemberian akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return GroupDatabaseAccess{}, err
	}
	query := `
		INSERT INTO platform.group_database_access (group_id, database_id, access_level)
		VALUES ($1, $2, $3)
		ON CONFLICT (group_id, database_id)
		DO UPDATE SET access_level = EXCLUDED.access_level
		RETURNING group_id, database_id, access_level;
	`
	access, err := scanGroupDatabaseAccess(tx.QueryRowContext(ctx, query, groupID, databaseID, string(level)))
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memberikan akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal menyimpan akses database", err)
	}
	return access, nil
}

// GetDatabaseAccess selalu membaca tabel izin terkini agar pencabutan yang sudah di-commit berlaku pada pembacaan berikutnya.
func (r *GrantRepository) GetDatabaseAccess(ctx context.Context, groupID, databaseID int) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memulai pembacaan akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return GroupDatabaseAccess{}, err
	}
	query := `
		SELECT group_id, database_id, access_level FROM platform.group_database_access
		WHERE group_id = $1 AND database_id = $2;
	`
	access, err := scanGroupDatabaseAccess(tx.QueryRowContext(ctx, query, groupID, databaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return GroupDatabaseAccess{}, ErrGroupDatabaseAccessNotFound
	}
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal membaca akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal menyelesaikan pembacaan akses", err)
	}
	return access, nil
}

func (r *GrantRepository) ListDatabaseAccess(ctx context.Context, groupID, limit, offset int) ([]GroupDatabaseAccess, error) {
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, admin.ErrInvalidGroupInput
	}
	if limit == 0 {
		limit = 20
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, grantRepositoryError("gagal memulai daftar akses database", err)
	}
	defer tx.Rollback()

	if err := lockGrantGroup(ctx, tx, groupID); err != nil {
		return nil, err
	}
	query := `
		SELECT group_id, database_id, access_level FROM platform.group_database_access
		WHERE group_id = $1 ORDER BY database_id ASC LIMIT $2 OFFSET $3;
	`
	rows, err := tx.QueryContext(ctx, query, groupID, limit, offset)
	if err != nil {
		return nil, grantRepositoryError("gagal mengambil daftar akses database", err)
	}
	defer rows.Close()

	accesses := make([]GroupDatabaseAccess, 0)
	for rows.Next() {
		access, err := scanGroupDatabaseAccess(rows)
		if err != nil {
			return nil, grantRepositoryError("gagal membaca daftar akses database", err)
		}
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, grantRepositoryError("gagal menyelesaikan daftar akses database", err)
	}
	if err := rows.Close(); err != nil {
		return nil, grantRepositoryError("gagal menutup daftar akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, grantRepositoryError("gagal menyelesaikan transaksi daftar akses", err)
	}
	return accesses, nil
}

// RevokeDatabaseAccess menghapus satu pemberian akses tanpa mengubah keanggotaan group atau konfigurasi database.
func (r *GrantRepository) RevokeDatabaseAccess(ctx context.Context, groupID, databaseID int) error {
	if groupID <= 0 || databaseID <= 0 {
		return admin.ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return grantRepositoryError("gagal memulai pencabutan akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM platform.group_database_access WHERE group_id = $1 AND database_id = $2;`, groupID, databaseID)
	if err != nil {
		return grantRepositoryError("gagal mencabut akses database", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return grantRepositoryError("gagal memeriksa pencabutan akses", err)
	}
	if count == 0 {
		return ErrGroupDatabaseAccessNotFound
	}
	if err := tx.Commit(); err != nil {
		return grantRepositoryError("gagal menyimpan pencabutan akses", err)
	}
	return nil
}

// Pemeriksaan hanya membaca ID konfigurasi yang sudah ada tanpa mengambil kredensial atau membuka koneksi target.
func lockDatabaseAccessParents(ctx context.Context, tx *sql.Tx, groupID, databaseID int) error {
	if err := lockGrantGroup(ctx, tx, groupID); err != nil {
		return err
	}
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.database_connections WHERE id = $1 FOR KEY SHARE;`, databaseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return database.ErrDatabaseConnectionNotFound
	}
	if err != nil {
		return grantRepositoryError("gagal memeriksa konfigurasi database target", err)
	}
	return nil
}

func scanGroupDatabaseAccess(row interface{ Scan(...any) error }) (GroupDatabaseAccess, error) {
	var access GroupDatabaseAccess
	err := row.Scan(&access.GroupID, &access.DatabaseID, &access.AccessLevel)
	return access, err
}

func validAccessLevel(level AccessLevel) bool {
	return level == AccessViewOnly || level == AccessViewEdit
}

// Kunci group menjaga keberadaan induk selama transaksi pemberian atau pembacaan akses database.
func lockGrantGroup(ctx context.Context, tx *sql.Tx, groupID int) error {
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.groups WHERE id = $1 FOR KEY SHARE;`, groupID).Scan(&id)
	if err != nil {
		return grantRepositoryError("gagal memeriksa group", err)
	}
	return nil
}

// Error domain dapat diperiksa dengan errors.Is menggunakan pola yang sama dengan repository user.
func grantRepositoryError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, admin.ErrGroupNotFound)
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "groups_name_key" {
		return fmt.Errorf("%s: %w", operation, admin.ErrGroupNameTaken)
	}
	return fmt.Errorf("%s: %w: %w", operation, admin.ErrGroupStorage, err)
}
