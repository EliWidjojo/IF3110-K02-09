package access

import (
	"context"
	"errors"
	"sort"

	"IF3110-K02-09/internal/admin"
)

var (
	ErrDatabaseAccessDenied        = errors.New("akses database ditolak")
	ErrInactiveUser                = errors.New("user nonaktif tidak dapat mengakses database")
	ErrInvalidAccessInput          = errors.New("identitas user atau database tidak valid")
	ErrAccessStorage               = errors.New("pembacaan akses database gagal")
	ErrAccessDependencyUnavailable = errors.New("komponen pemeriksaan akses belum tersedia")
)

// AccessibleDatabase hanya membawa identitas konfigurasi dan tingkat akses tanpa kredensial koneksi target.
type AccessibleDatabase struct {
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
