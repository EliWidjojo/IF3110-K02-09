package admin

import (
	"context"
	"database/sql"
	"errors"
)

// AuthenticationRecord hanya ditujukan ke verifier password modul A dan tidak boleh dikirim melalui HTTP atau log.
type AuthenticationRecord struct {
	User         User         `json:"-"`
	PasswordHash PasswordHash `json:"-"`
}

func (AuthenticationRecord) String() string   { return "[record authentication disembunyikan]" }
func (AuthenticationRecord) GoString() string { return "[record authentication disembunyikan]" }

type AuthenticationRepository struct{ db *sql.DB }

func NewAuthenticationRepository(db *sql.DB) *AuthenticationRepository {
	return &AuthenticationRepository{db}
}

// Pencarian menggunakan tabel platform yang sama tanpa mirror atau sinkronisasi ke store JSON milik branch lain.
func (r *AuthenticationRepository) FindByUsername(ctx context.Context, username string) (AuthenticationRecord, error) {
	if r == nil || r.db == nil {
		return AuthenticationRecord{}, ErrUserDependencyUnavailable
	}
	if !validUsername(username) {
		return AuthenticationRecord{}, ErrInvalidUserInput
	}
	var record AuthenticationRecord
	var hash sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,username,is_active,is_platform_admin,password_hash FROM platform.users WHERE username=$1`, username).Scan(&record.User.ID, &record.User.Username, &record.User.IsActive, &record.User.IsPlatformAdmin, &hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!record.User.IsActive || !hash.Valid || hash.String == "")) {
		return AuthenticationRecord{}, ErrUserNotFound
	}
	if err != nil {
		if ctx.Err() != nil {
			return AuthenticationRecord{}, ctx.Err()
		}
		return AuthenticationRecord{}, ErrUserStorage
	}
	record.PasswordHash = PasswordHash(hash.String)
	return record, nil
}
func (r *AuthenticationRepository) CurrentUser(ctx context.Context, id int) (User, error) {
	if r == nil || r.db == nil {
		return User{}, ErrUserDependencyUnavailable
	}
	user, err := NewUserRepository(r.db).GetByID(ctx, id)
	if err == nil && !user.IsActive {
		return User{}, ErrUserNotFound
	}
	return user, err
}
