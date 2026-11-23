package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"IF3110-K02-09/internal/admin"
)

// Model, kontrak, dan service akses efektif.

var (
	ErrDatabaseAccessDenied        = errors.New("akses database ditolak")
	ErrInactiveUser                = errors.New("user nonaktif tidak dapat mengakses database")
	ErrInvalidAccessInput          = errors.New("identitas user atau database tidak valid")
	ErrAccessStorage               = errors.New("pembacaan akses database gagal")
	ErrAccessDependencyUnavailable = errors.New("komponen pemeriksaan akses belum tersedia")
)

// AccessibleDatabase hanya membawa identitas konfigurasi dan tingkat akses tanpa kredensial koneksi target.
type AccessibleDatabase struct {
	Driver      string      `json:"driver"`
	DatabaseID  int         `json:"database_id"`
	Name        string      `json:"name"`
	AccessLevel AccessLevel `json:"access_level"`
}

// UserAccessSnapshot membawa status user dan pemberian akses per group yang belum digabungkan dari satu pembacaan backend.
type UserAccessSnapshot struct {
	IsActive bool
	Grants   []AccessibleDatabase
}

type EffectiveAccessStore interface {
	GetUserAccess(context.Context, int) (UserAccessSnapshot, error)
}

// DatabasePermissionChecker digunakan viewer dan migration engine sebelum setiap operasi pada database target.
// Identitas pelaku berasal dari context terverifikasi dan setiap error wajib menghentikan operasi yang dilindungi.
type DatabasePermissionChecker interface {
	RequireRead(context.Context, int) error
	RequireEdit(context.Context, int) error
}

type EffectiveAccessService struct {
	store    EffectiveAccessStore
	identity admin.VerifiedUserProvider
}

func NewEffectiveAccessService(store EffectiveAccessStore, identity admin.VerifiedUserProvider) *EffectiveAccessService {
	return &EffectiveAccessService{store: store, identity: identity}
}

// GetEffectiveAccess mengembalikan nilai kosong ketika tidak ada pemberian akses untuk database yang diminta.
func (s *EffectiveAccessService) GetEffectiveAccess(ctx context.Context, databaseID int) (AccessLevel, error) {
	if databaseID <= 0 {
		return "", ErrInvalidAccessInput
	}
	databases, err := s.ListAccessibleDatabases(ctx)
	if err != nil {
		return "", err
	}
	for _, database := range databases {
		if database.DatabaseID == databaseID {
			return database.AccessLevel, nil
		}
	}
	return "", nil
}

func (s *EffectiveAccessService) RequireRead(ctx context.Context, databaseID int) error {
	level, err := s.GetEffectiveAccess(ctx, databaseID)
	if err != nil {
		return err
	}
	if level != AccessViewOnly && level != AccessViewEdit {
		return ErrDatabaseAccessDenied
	}
	return nil
}

func (s *EffectiveAccessService) RequireEdit(ctx context.Context, databaseID int) error {
	level, err := s.GetEffectiveAccess(ctx, databaseID)
	if err != nil {
		return err
	}
	if level != AccessViewEdit {
		return ErrDatabaseAccessDenied
	}
	return nil
}

func (s *EffectiveAccessService) ListAccessibleDatabases(ctx context.Context) ([]AccessibleDatabase, error) {
	if s == nil || s.store == nil || s.identity == nil {
		return nil, ErrAccessDependencyUnavailable
	}
	userID, err := s.identity.VerifiedUserID(ctx)
	if errors.Is(err, admin.ErrUnauthenticated) {
		return nil, admin.ErrUnauthenticated
	}
	if err != nil {
		return nil, ErrAccessDependencyUnavailable
	}
	if userID <= 0 {
		return nil, admin.ErrUnauthenticated
	}
	snapshot, err := s.store.GetUserAccess(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !snapshot.IsActive {
		return nil, ErrInactiveUser
	}

	// Penggabungan hanya memakai pemberian dari group; peran Platform Admin tidak menambah izin database.
	merged := make(map[int]AccessibleDatabase)
	for _, grant := range snapshot.Grants {
		if grant.DatabaseID <= 0 || !validAccessLevel(grant.AccessLevel) {
			return nil, ErrAccessStorage
		}
		current, exists := merged[grant.DatabaseID]
		if !exists || (grant.AccessLevel == AccessViewEdit && current.AccessLevel == AccessViewOnly) {
			merged[grant.DatabaseID] = grant
		}
	}
	databases := make([]AccessibleDatabase, 0, len(merged))
	for _, database := range merged {
		databases = append(databases, database)
	}
	sort.Slice(databases, func(i, j int) bool {
		return databases[i].DatabaseID < databases[j].DatabaseID
	})
	return databases, nil
}

// Repository pembacaan akses efektif.

type EffectiveAccessRepository struct {
	db *sql.DB
}

func NewEffectiveAccessRepository(db *sql.DB) *EffectiveAccessRepository {
	return &EffectiveAccessRepository{db: db}
}

// GetUserAccess membaca status aktif, keanggotaan, dan izin dalam satu query agar tidak mencampur hasil snapshot berbeda.
func (r *EffectiveAccessRepository) GetUserAccess(ctx context.Context, userID int) (UserAccessSnapshot, error) {
	if userID <= 0 {
		return UserAccessSnapshot{}, ErrInvalidAccessInput
	}
	query := `
		SELECT u.is_active, d.id, d.name, a.access_level,d.driver
		FROM platform.users AS u
		LEFT JOIN platform.group_memberships AS m ON m.user_id = u.id
		LEFT JOIN platform.group_database_access AS a ON a.group_id = m.group_id
		LEFT JOIN platform.database_connections AS d ON d.id = a.database_id
		WHERE u.id = $1
		ORDER BY d.id ASC, m.group_id ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
	}
	defer rows.Close()

	snapshot := UserAccessSnapshot{Grants: make([]AccessibleDatabase, 0)}
	found := false
	for rows.Next() {
		var databaseID sql.NullInt64
		var name, level, driver sql.NullString
		if err := rows.Scan(&snapshot.IsActive, &databaseID, &name, &level, &driver); err != nil {
			return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
		}
		found = true
		// LEFT JOIN tetap menghasilkan status user meskipun user tidak memiliki group atau pemberian akses.
		if !databaseID.Valid && !name.Valid && !level.Valid {
			continue
		}
		if !databaseID.Valid || !name.Valid || !level.Valid {
			return UserAccessSnapshot{}, ErrAccessStorage
		}
		snapshot.Grants = append(snapshot.Grants, AccessibleDatabase{
			Driver: driver.String, DatabaseID: int(databaseID.Int64), Name: name.String, AccessLevel: AccessLevel(level.String),
		})
	}
	if err := rows.Err(); err != nil {
		return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
	}
	if !found {
		return UserAccessSnapshot{}, admin.ErrUserNotFound
	}
	return snapshot, nil
}
