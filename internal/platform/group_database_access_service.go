package platform

import "context"

func (s *GroupService) SetDatabaseAccess(ctx context.Context, groupID, databaseID int, level AccessLevel) (GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return GroupDatabaseAccess{}, err
	}
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, ErrInvalidGroupInput
	}
	if !validAccessLevel(level) {
		return GroupDatabaseAccess{}, ErrInvalidAccessLevel
	}
	return s.store.SetDatabaseAccess(ctx, groupID, databaseID, level)
}

func (s *GroupService) GetDatabaseAccess(ctx context.Context, groupID, databaseID int) (GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return GroupDatabaseAccess{}, err
	}
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, ErrInvalidGroupInput
	}
	return s.store.GetDatabaseAccess(ctx, groupID, databaseID)
}

func (s *GroupService) ListDatabaseAccess(ctx context.Context, groupID, limit, offset int) ([]GroupDatabaseAccess, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidGroupInput
	}
	return s.store.ListDatabaseAccess(ctx, groupID, limit, offset)
}

func (s *GroupService) RevokeDatabaseAccess(ctx context.Context, groupID, databaseID int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if groupID <= 0 || databaseID <= 0 {
		return ErrInvalidGroupInput
	}
	return s.store.RevokeDatabaseAccess(ctx, groupID, databaseID)
}
