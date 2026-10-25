package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnauthenticated           = errors.New("authentication diperlukan")
	ErrPlatformAdminRequired     = errors.New("akses Platform Admin diperlukan")
	ErrUserDependencyUnavailable = errors.New("komponen pengelolaan user belum tersedia")
)

// UserStore membatasi kebutuhan penyimpanan service pada operasi administrasi user.
type UserStore interface {
	Create(context.Context, CreateUserParams) (User, error)
	GetByID(context.Context, int) (User, error)
	List(context.Context, int, int) ([]User, error)
	Update(context.Context, int, UpdateUserParams) (User, error)
	Deactivate(context.Context, int) error
}

// VerifiedUserProvider membaca identitas dari session backend yang telah diverifikasi modul A, bukan dari data client.
// Modul A wajib menolak session user nonaktif dan mengembalikan ErrUnauthenticated untuk session tidak sah.
type VerifiedUserProvider interface {
	VerifiedUserID(context.Context) (int, error)
}

// UserPasswordHasher disediakan modul A untuk menghasilkan hash sesuai kontrak authentication yang sama.
// Penolakan kebijakan password dikembalikan sebagai ErrInvalidUserInput tanpa menyertakan password pada error.
type UserPasswordHasher interface {
	HashPassword(context.Context, string) (PasswordHash, error)
}

// UserSessionRevoker mencabut seluruh identifier session user, termasuk identifier lama dalam grace period.
// Modul A harus mencegah penerbitan session untuk user nonaktif dan memastikan session tercabut tidak dapat dipakai kembali.
type UserSessionRevoker interface {
	RevokeUserSessions(context.Context, int) error
}

// CreateUserInput hanya membawa password menuju hasher dan tidak menyertakannya saat diserialisasi ke JSON.
type CreateUserInput struct {
	Username        string `json:"username"`
	Password        string `json:"-"`
	IsPlatformAdmin bool   `json:"is_platform_admin"`
}

type UserService struct {
	store    UserStore
	identity VerifiedUserProvider
	hasher   UserPasswordHasher
	sessions UserSessionRevoker
}

func NewUserService(store UserStore, identity VerifiedUserProvider, hasher UserPasswordHasher, sessions UserSessionRevoker) *UserService {
	return &UserService{store: store, identity: identity, hasher: hasher, sessions: sessions}
}

// RequirePlatformAdmin menyediakan pemeriksaan identitas dan peran yang sama untuk service administrasi lain.
func (s *UserService) RequirePlatformAdmin(ctx context.Context) error {
	return s.requireAdmin(ctx)
}

func (s *UserService) requireAdmin(ctx context.Context) error {
	if s == nil || s.store == nil || s.identity == nil {
		return ErrUserDependencyUnavailable
	}
	id, err := s.identity.VerifiedUserID(ctx)
	if errors.Is(err, ErrUnauthenticated) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("%w: verifikasi identitas gagal", ErrUserDependencyUnavailable)
	}
	if id <= 0 {
		return ErrUnauthenticated
	}

	// Peran dan status dibaca ulang agar perubahan hak admin tidak bergantung pada klaim session yang sudah usang.
	actor, err := s.store.GetByID(ctx, id)
	if errors.Is(err, ErrUserNotFound) {
		return ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if !actor.IsActive {
		return ErrUnauthenticated
	}
	if !actor.IsPlatformAdmin {
		return ErrPlatformAdminRequired
	}
	return nil
}

func (s *UserService) Create(ctx context.Context, input CreateUserInput) (User, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return User{}, err
	}
	if s.hasher == nil {
		return User{}, ErrUserDependencyUnavailable
	}
	if !validUsername(input.Username) || !utf8.ValidString(input.Password) || len(input.Password) < 8 || len(input.Password) > 1024 {
		return User{}, ErrInvalidUserInput
	}
	hash, err := s.hasher.HashPassword(ctx, input.Password)
	if errors.Is(err, ErrInvalidUserInput) {
		return User{}, ErrInvalidUserInput
	}
	if err != nil || strings.TrimSpace(string(hash)) == "" {
		return User{}, fmt.Errorf("%w: hashing password gagal", ErrUserDependencyUnavailable)
	}
	return s.store.Create(ctx, CreateUserParams{
		Username: input.Username, PasswordHash: hash, IsPlatformAdmin: input.IsPlatformAdmin,
	})
}

func (s *UserService) GetByID(ctx context.Context, id int) (User, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return User{}, err
	}
	if id <= 0 {
		return User{}, ErrInvalidUserInput
	}
	return s.store.GetByID(ctx, id)
}

func (s *UserService) List(ctx context.Context, limit, offset int) ([]User, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidUserInput
	}
	return s.store.List(ctx, limit, offset)
}

func (s *UserService) Update(ctx context.Context, id int, input UpdateUserParams) (User, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return User{}, err
	}
	if id <= 0 || (input.Username == nil && input.IsActive == nil && input.IsPlatformAdmin == nil) {
		return User{}, ErrInvalidUserInput
	}
	if input.Username != nil && !validUsername(*input.Username) {
		return User{}, ErrInvalidUserInput
	}
	needsRevocation := input.IsActive != nil || input.IsPlatformAdmin != nil
	if needsRevocation && s.sessions == nil {
		return User{}, ErrUserDependencyUnavailable
	}
	user, err := s.store.Update(ctx, id, input)
	if err != nil {
		return User{}, err
	}
	if needsRevocation {
		if err := s.sessions.RevokeUserSessions(ctx, id); err != nil {
			return User{}, fmt.Errorf("%w: session gagal dicabut setelah atribut user diperbarui", ErrUserDependencyUnavailable)
		}
	}
	return user, nil
}

func (s *UserService) Deactivate(ctx context.Context, id int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if id <= 0 {
		return ErrInvalidUserInput
	}
	if s.sessions == nil {
		return ErrUserDependencyUnavailable
	}
	// Status disimpan lebih dahulu agar modul A menolak login dan session user ini selama pencabutan berlangsung.
	if err := s.store.Deactivate(ctx, id); err != nil {
		return err
	}
	if err := s.sessions.RevokeUserSessions(ctx, id); err != nil {
		return fmt.Errorf("%w: user telah nonaktif tetapi pencabutan session perlu diulang", ErrUserDependencyUnavailable)
	}
	return nil
}

// Username dibatasi 128 karakter tanpa spasi tepi atau karakter kontrol agar identitas tetap konsisten.
func validUsername(username string) bool {
	return username != "" && utf8.ValidString(username) && utf8.RuneCountInString(username) <= 128 &&
		strings.TrimSpace(username) == username && strings.IndexFunc(username, unicode.IsControl) == -1
}
