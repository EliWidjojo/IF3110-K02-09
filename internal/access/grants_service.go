package access

import (
	"context"
	"errors"

	"IF3110-K02-09/internal/admin"
)

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
