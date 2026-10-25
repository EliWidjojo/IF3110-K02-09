package admin

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrGroupDependencyUnavailable = errors.New("komponen pengelolaan group belum tersedia")

// PlatformAdminAuthorizer menggunakan pemeriksaan backend yang sudah disediakan UserService tanpa menerima klaim peran dari client.
type PlatformAdminAuthorizer interface {
	RequirePlatformAdmin(context.Context) error
}

type GroupStore interface {
	Create(context.Context, CreateGroupParams) (Group, error)
	GetByID(context.Context, int) (Group, error)
	List(context.Context, int, int) ([]Group, error)
	Update(context.Context, int, UpdateGroupParams) (Group, error)
	Delete(context.Context, int) error
	AddMember(context.Context, int, int) (GroupMembership, error)
	RemoveMember(context.Context, int, int) error
	ListMembers(context.Context, int, int, int) ([]User, error)
}

type GroupService struct {
	store GroupStore
	admin PlatformAdminAuthorizer
}

func NewGroupService(store GroupStore, admin PlatformAdminAuthorizer) *GroupService {
	return &GroupService{store: store, admin: admin}
}

func (s *GroupService) requireAdmin(ctx context.Context) error {
	if s == nil || s.store == nil || s.admin == nil {
		return ErrGroupDependencyUnavailable
	}
	err := s.admin.RequirePlatformAdmin(ctx)
	if errors.Is(err, ErrUserDependencyUnavailable) {
		return ErrGroupDependencyUnavailable
	}
	return err
}

func (s *GroupService) Create(ctx context.Context, input CreateGroupParams) (Group, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return Group{}, err
	}
	if !validGroupName(input.Name) {
		return Group{}, ErrInvalidGroupInput
	}
	return s.store.Create(ctx, input)
}

func (s *GroupService) GetByID(ctx context.Context, id int) (Group, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return Group{}, err
	}
	if id <= 0 {
		return Group{}, ErrInvalidGroupInput
	}
	return s.store.GetByID(ctx, id)
}

func (s *GroupService) List(ctx context.Context, limit, offset int) ([]Group, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidGroupInput
	}
	return s.store.List(ctx, limit, offset)
}

func (s *GroupService) Update(ctx context.Context, id int, input UpdateGroupParams) (Group, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return Group{}, err
	}
	if id <= 0 || input.Name == nil || !validGroupName(*input.Name) {
		return Group{}, ErrInvalidGroupInput
	}
	return s.store.Update(ctx, id, input)
}

func (s *GroupService) Delete(ctx context.Context, id int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if id <= 0 {
		return ErrInvalidGroupInput
	}
	return s.store.Delete(ctx, id)
}

func (s *GroupService) AddMember(ctx context.Context, groupID, userID int) (GroupMembership, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return GroupMembership{}, err
	}
	if groupID <= 0 || userID <= 0 {
		return GroupMembership{}, ErrInvalidGroupInput
	}
	return s.store.AddMember(ctx, groupID, userID)
}

func (s *GroupService) RemoveMember(ctx context.Context, groupID, userID int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if groupID <= 0 || userID <= 0 {
		return ErrInvalidGroupInput
	}
	return s.store.RemoveMember(ctx, groupID, userID)
}

func (s *GroupService) ListMembers(ctx context.Context, groupID, limit, offset int) ([]User, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidGroupInput
	}
	return s.store.ListMembers(ctx, groupID, limit, offset)
}

// Nama group dibatasi 128 karakter tanpa spasi tepi atau karakter kontrol agar identitas group mudah dibedakan.
func validGroupName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 128 &&
		strings.TrimSpace(name) == name && strings.IndexFunc(name, unicode.IsControl) == -1
}
