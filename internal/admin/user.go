package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lib/pq"
)

// Model user.

// User menjadi identitas bersama untuk modul authentication dan administrasi platform.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	IsActive bool   `json:"is_active"`
	// IsPlatformAdmin menandai wewenang pengelolaan platform yang terpisah dari tingkat akses database.
	IsPlatformAdmin bool `json:"is_platform_admin"`
}

// Service dan validasi user.

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

func (s *UserService) RevokeSessions(ctx context.Context, id int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	store, ok := s.store.(interface {
		InvalidateSessions(context.Context, int) error
	})
	if !ok || s.sessions == nil {
		return ErrUserDependencyUnavailable
	}
	if err := store.InvalidateSessions(ctx, id); err != nil {
		return err
	}
	return s.sessions.RevokeUserSessions(ctx, id)
}

// Repository user.

var (
	ErrUserNotFound     = errors.New("user tidak ditemukan")
	ErrUsernameTaken    = errors.New("username sudah dipakai")
	ErrUserStorage      = errors.New("penyimpanan user gagal")
	ErrInvalidUserInput = errors.New("data user tidak valid")
)

// PasswordHash hanya diisi hash hasil komponen authentication yang berwenang, bukan password plaintext.
type PasswordHash string

// CreateUserParams memisahkan input penyimpanan kredensial dari model User yang aman untuk respons publik.
type CreateUserParams struct {
	Username        string       `json:"username"`
	PasswordHash    PasswordHash `json:"-"`
	IsPlatformAdmin bool         `json:"is_platform_admin"`
}

// UpdateUserParams membatasi perubahan pada atribut administrasi tanpa mengubah identitas atau kredensial.
type UpdateUserParams struct {
	Username        *string `json:"username"`
	IsActive        *bool   `json:"is_active"`
	IsPlatformAdmin *bool   `json:"is_platform_admin"`
}

// UserRepository menggunakan koneksi database internal platform dan menyerahkan otorisasi kepada lapisan aplikasi.
type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Create menerima hash yang sudah disiapkan pemanggil dan membuat user aktif tanpa mengembalikan kredensial.
func (r *UserRepository) Create(ctx context.Context, params CreateUserParams) (User, error) {
	if strings.TrimSpace(params.Username) == "" || strings.TrimSpace(string(params.PasswordHash)) == "" {
		return User{}, fmt.Errorf("%w: username dan hash wajib diisi", ErrInvalidUserInput)
	}

	query := `
		INSERT INTO platform.users (username, password_hash, is_platform_admin)
		VALUES ($1, $2, $3)
		RETURNING id, username, is_active, is_platform_admin;
	`
	user, err := scanUser(r.db.QueryRowContext(ctx, query,
		params.Username, string(params.PasswordHash), params.IsPlatformAdmin))
	if err != nil {
		return User{}, userRepositoryError("gagal membuat user", err)
	}
	return user, nil
}

func (r *UserRepository) GetByID(ctx context.Context, id int) (User, error) {
	query := `SELECT id, username, is_active, is_platform_admin FROM platform.users WHERE id = $1;`
	user, err := scanUser(r.db.QueryRowContext(ctx, query, id))
	if err != nil {
		return User{}, userRepositoryError("gagal mengambil user berdasarkan ID", err)
	}
	return user, nil
}

func (r *UserRepository) GetByUsername(ctx context.Context, username string) (User, error) {
	query := `SELECT id, username, is_active, is_platform_admin FROM platform.users WHERE username = $1;`
	user, err := scanUser(r.db.QueryRowContext(ctx, query, username))
	if err != nil {
		return User{}, userRepositoryError("gagal mengambil user berdasarkan username", err)
	}
	return user, nil
}

// List mengurutkan hasil berdasarkan ID dengan limit bawaan 20, maksimum 100, dan offset mulai dari nol.
func (r *UserRepository) List(ctx context.Context, limit, offset int) ([]User, error) {
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("%w: limit harus 0 sampai 100 dan offset tidak boleh negatif", ErrInvalidUserInput)
	}
	if limit == 0 {
		limit = 20
	}

	query := `
		SELECT id, username, is_active, is_platform_admin
		FROM platform.users
		ORDER BY id ASC
		LIMIT $1 OFFSET $2;
	`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, userRepositoryError("gagal menampilkan daftar user", err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, userRepositoryError("gagal membaca data user", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, userRepositoryError("gagal menyelesaikan pembacaan daftar user", err)
	}
	return users, nil
}

func (r *UserRepository) Update(ctx context.Context, id int, params UpdateUserParams) (User, error) {
	if params.Username == nil && params.IsActive == nil && params.IsPlatformAdmin == nil {
		return User{}, fmt.Errorf("%w: atribut perubahan wajib diisi", ErrInvalidUserInput)
	}
	if params.Username != nil && strings.TrimSpace(*params.Username) == "" {
		return User{}, fmt.Errorf("%w: username tidak boleh kosong", ErrInvalidUserInput)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, ErrUserStorage
	}
	defer tx.Rollback()
	if err = guardLastAdmin(ctx, tx, id, params); err != nil {
		return User{}, err
	}
	// Nilai nil mempertahankan atribut lama, sedangkan pointer bool memungkinkan penyimpanan nilai false.
	query := `
		UPDATE platform.users
		SET username = COALESCE($1, username),
			is_active = COALESCE($2, is_active),
			is_platform_admin = COALESCE($3, is_platform_admin)
		WHERE id = $4
		RETURNING id, username, is_active, is_platform_admin;
	`
	user, err := scanUser(tx.QueryRowContext(ctx, query,
		params.Username, params.IsActive, params.IsPlatformAdmin, id))
	if err != nil {
		return User{}, userRepositoryError("gagal memperbarui user", err)
	}
	if tx.Commit() != nil {
		return User{}, ErrUserStorage
	}
	return user, nil
}

// Deactivate tidak menghapus baris user sehingga identitas, keanggotaan, dan rujukan riwayat aktivitas tetap tersedia.
func (r *UserRepository) Deactivate(ctx context.Context, id int) error {
	active := false
	_, err := r.Update(ctx, id, UpdateUserParams{IsActive: &active})
	return err
}

// Perubahan admin diserialkan agar dua request tidak dapat menghilangkan admin aktif terakhir.
func guardLastAdmin(ctx context.Context, tx *sql.Tx, id int, params UpdateUserParams) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE platform.users IN EXCLUSIVE MODE`); err != nil {
		return ErrUserStorage
	}
	var active, admin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_active,is_platform_admin FROM platform.users WHERE id=$1`, id).Scan(&active, &admin); err != nil {
		return userRepositoryError("membaca user", err)
	}
	if active && admin && ((params.IsActive != nil && !*params.IsActive) || (params.IsPlatformAdmin != nil && !*params.IsPlatformAdmin)) {
		var count int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.users WHERE is_active AND is_platform_admin`).Scan(&count) != nil {
			return ErrUserStorage
		}
		if count <= 1 {
			return fmt.Errorf("%w: admin aktif terakhir harus dipertahankan", ErrInvalidUserInput)
		}
	}
	return nil
}
func (r *UserRepository) InvalidateSessions(ctx context.Context, id int) error {
	result, err := r.db.ExecContext(ctx, `UPDATE platform.users SET security_version=security_version+1 WHERE id=$1`, id)
	if err != nil {
		return ErrUserStorage
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrUserNotFound
	}
	return nil
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.Username, &user.IsActive, &user.IsPlatformAdmin)
	return user, err
}

// Error domain dapat diperiksa dengan errors.Is tanpa bergantung pada pesan error driver PostgreSQL.
func userRepositoryError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrUserNotFound)
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "users_username_key" {
		return fmt.Errorf("%s: %w", operation, ErrUsernameTaken)
	}
	return fmt.Errorf("%s: %w: %w", operation, ErrUserStorage, err)
}
