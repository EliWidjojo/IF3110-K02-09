package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

var (
	ErrGroupNotFound     = errors.New("group tidak ditemukan")
	ErrGroupNameTaken    = errors.New("nama group sudah dipakai")
	ErrGroupStorage      = errors.New("penyimpanan group gagal")
	ErrInvalidGroupInput = errors.New("data group tidak valid")
)

type CreateGroupParams struct {
	Name string `json:"name"`
}

// UpdateGroupParams membatasi perubahan pada nama tanpa mengubah identitas group yang dirujuk relasi lain.
type UpdateGroupParams struct {
	Name *string `json:"name"`
}

// GroupRepository menggunakan database internal platform dan menyerahkan otorisasi kepada lapisan aplikasi.
type GroupRepository struct {
	db *sql.DB
}

func NewGroupRepository(db *sql.DB) *GroupRepository {
	return &GroupRepository{db: db}
}

func (r *GroupRepository) Create(ctx context.Context, params CreateGroupParams) (Group, error) {
	if strings.TrimSpace(params.Name) == "" {
		return Group{}, fmt.Errorf("%w: nama group wajib diisi", ErrInvalidGroupInput)
	}

	query := `INSERT INTO platform.groups (name) VALUES ($1) RETURNING id, name;`
	group, err := scanGroup(r.db.QueryRowContext(ctx, query, params.Name))
	if err != nil {
		return Group{}, groupRepositoryError("gagal membuat group", err)
	}
	return group, nil
}

func (r *GroupRepository) GetByID(ctx context.Context, id int) (Group, error) {
	query := `SELECT id, name FROM platform.groups WHERE id = $1;`
	group, err := scanGroup(r.db.QueryRowContext(ctx, query, id))
	if err != nil {
		return Group{}, groupRepositoryError("gagal mengambil group berdasarkan ID", err)
	}
	return group, nil
}

// List mengurutkan hasil berdasarkan ID dengan limit bawaan 20, maksimum 100, dan offset mulai dari nol.
func (r *GroupRepository) List(ctx context.Context, limit, offset int) ([]Group, error) {
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("%w: limit harus 0 sampai 100 dan offset tidak boleh negatif", ErrInvalidGroupInput)
	}
	if limit == 0 {
		limit = 20
	}

	query := `SELECT id, name FROM platform.groups ORDER BY id ASC LIMIT $1 OFFSET $2;`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, groupRepositoryError("gagal menampilkan daftar group", err)
	}
	defer rows.Close()

	groups := make([]Group, 0)
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, groupRepositoryError("gagal membaca data group", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, groupRepositoryError("gagal menyelesaikan pembacaan daftar group", err)
	}
	return groups, nil
}

func (r *GroupRepository) Update(ctx context.Context, id int, params UpdateGroupParams) (Group, error) {
	if params.Name == nil || strings.TrimSpace(*params.Name) == "" {
		return Group{}, fmt.Errorf("%w: nama group wajib diisi", ErrInvalidGroupInput)
	}

	query := `UPDATE platform.groups SET name = $1 WHERE id = $2 RETURNING id, name;`
	group, err := scanGroup(r.db.QueryRowContext(ctx, query, *params.Name, id))
	if err != nil {
		return Group{}, groupRepositoryError("gagal memperbarui group", err)
	}
	return group, nil
}

// Delete mengandalkan ON DELETE CASCADE agar keanggotaan dan pemberian akses terhapus atomik bersama group.
func (r *GroupRepository) Delete(ctx context.Context, id int) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM platform.groups WHERE id = $1;`, id)
	if err != nil {
		return groupRepositoryError("gagal menghapus group", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return groupRepositoryError("gagal memeriksa hasil penghapusan group", err)
	}
	if count == 0 {
		return fmt.Errorf("gagal menghapus group: %w", ErrGroupNotFound)
	}
	return nil
}

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var group Group
	err := row.Scan(&group.ID, &group.Name)
	return group, err
}

// Error domain dapat diperiksa dengan errors.Is menggunakan pola yang sama dengan repository user.
func groupRepositoryError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrGroupNotFound)
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "groups_name_key" {
		return fmt.Errorf("%s: %w", operation, ErrGroupNameTaken)
	}
	return fmt.Errorf("%s: %w: %w", operation, ErrGroupStorage, err)
}
