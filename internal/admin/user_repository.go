package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

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
