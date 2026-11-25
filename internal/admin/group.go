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

// Model group dan keanggotaan.

// Group merepresentasikan kelompok pengguna yang menjadi dasar pemberian akses database.
type Group struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GroupMembership memisahkan keanggotaan dari user agar satu user dapat tergabung dalam beberapa group.
type GroupMembership struct {
	UserID  int `json:"user_id"`
	GroupID int `json:"group_id"`
}

// Service dan validasi group serta keanggotaan.

var ErrGroupDependencyUnavailable = errors.New("komponen pengelolaan group belum tersedia")

// PlatformAdminAuthorizer menggunakan pemeriksaan backend yang sudah disediakan UserService tanpa menerima klaim peran dari client.
type PlatformAdminAuthorizer interface {
	RequirePlatformAdmin(context.Context) error
}

type GroupStore interface {
	SetLeader(context.Context, int, int) error
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

func (s *GroupService) SetLeader(ctx context.Context, groupID, userID int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if groupID <= 0 || userID <= 0 {
		return ErrInvalidGroupInput
	}
	return s.store.SetLeader(ctx, groupID, userID)
}

func (r *GroupRepository) SetLeader(ctx context.Context, groupID, userID int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrGroupStorage
	}
	defer tx.Rollback()
	var id int
	if err = tx.QueryRowContext(ctx, `SELECT id FROM platform.groups WHERE id=$1 FOR UPDATE`, groupID).Scan(&id); err != nil {
		return groupRepositoryError("group leader", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT u.id FROM platform.group_memberships m JOIN platform.users u ON u.id=m.user_id
		WHERE m.group_id=$1 AND u.id=$2 AND u.is_active FOR SHARE`, groupID, userID).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidGroupInput
	} else if err != nil {
		return ErrGroupStorage
	}
	// Changing leader invalidates approvals from the previous tenure through the FK.
	if _, err = tx.ExecContext(ctx, `DELETE FROM platform.group_leaders WHERE group_id=$1 AND user_id<>$2`, groupID, userID); err != nil {
		return ErrGroupStorage
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO platform.group_leaders(group_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, groupID, userID); err != nil {
		return ErrGroupStorage
	}
	return tx.Commit()
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

// Repository group.

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

// Repository keanggotaan group.

var (
	ErrGroupMembershipExists   = errors.New("user sudah menjadi anggota group")
	ErrGroupMembershipNotFound = errors.New("keanggotaan group tidak ditemukan")
)

func (r *GroupRepository) AddMember(ctx context.Context, groupID, userID int) (GroupMembership, error) {
	if groupID <= 0 || userID <= 0 {
		return GroupMembership{}, ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupMembership{}, groupRepositoryError("gagal memulai penambahan anggota", err)
	}
	defer tx.Rollback()

	if err := lockMembershipParents(ctx, tx, groupID, userID); err != nil {
		return GroupMembership{}, err
	}
	query := `
		INSERT INTO platform.group_memberships (user_id, group_id)
		VALUES ($1, $2) RETURNING user_id, group_id;
	`
	var membership GroupMembership
	err = tx.QueryRowContext(ctx, query, userID, groupID).Scan(&membership.UserID, &membership.GroupID)
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "group_memberships_pkey" {
			return GroupMembership{}, fmt.Errorf("gagal menambahkan anggota: %w", ErrGroupMembershipExists)
		}
		return GroupMembership{}, groupRepositoryError("gagal menambahkan anggota", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupMembership{}, groupRepositoryError("gagal menyimpan keanggotaan", err)
	}
	return membership, nil
}

func (r *GroupRepository) RemoveMember(ctx context.Context, groupID, userID int) error {
	if groupID <= 0 || userID <= 0 {
		return ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return groupRepositoryError("gagal memulai penghapusan anggota", err)
	}
	defer tx.Rollback()

	if err := lockMembershipParents(ctx, tx, groupID, userID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM platform.group_memberships WHERE group_id = $1 AND user_id = $2;`, groupID, userID)
	if err != nil {
		return groupRepositoryError("gagal mengeluarkan anggota", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return groupRepositoryError("gagal memeriksa penghapusan anggota", err)
	}
	if count == 0 {
		return ErrGroupMembershipNotFound
	}
	if err := tx.Commit(); err != nil {
		return groupRepositoryError("gagal menyimpan penghapusan anggota", err)
	}
	return nil
}

// ListMembers membaca keanggotaan terkini dari database tanpa menyimpan salinan izin pada session atau cache.
func (r *GroupRepository) ListMembers(ctx context.Context, groupID, limit, offset int) ([]User, error) {
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidGroupInput
	}
	if limit == 0 {
		limit = 20
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, groupRepositoryError("gagal memulai pembacaan anggota", err)
	}
	defer tx.Rollback()

	if err := lockGroupForRelation(ctx, tx, groupID); err != nil {
		return nil, err
	}
	query := `
		SELECT u.id, u.username, u.is_active, u.is_platform_admin
		FROM platform.group_memberships AS m
		JOIN platform.users AS u ON u.id = m.user_id
		WHERE m.group_id = $1
		ORDER BY u.id ASC
		LIMIT $2 OFFSET $3;
	`
	rows, err := tx.QueryContext(ctx, query, groupID, limit, offset)
	if err != nil {
		return nil, groupRepositoryError("gagal mengambil daftar anggota", err)
	}
	defer rows.Close()

	members := make([]User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, groupRepositoryError("gagal membaca anggota group", err)
		}
		members = append(members, user)
	}
	if err := rows.Err(); err != nil {
		return nil, groupRepositoryError("gagal menyelesaikan pembacaan anggota", err)
	}
	if err := rows.Close(); err != nil {
		return nil, groupRepositoryError("gagal menutup pembacaan anggota", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, groupRepositoryError("gagal menyelesaikan transaksi pembacaan anggota", err)
	}
	return members, nil
}

// Kunci pada baris induk mencegah penghapusan user atau group di antara pemeriksaan keberadaan dan perubahan keanggotaan.
func lockMembershipParents(ctx context.Context, tx *sql.Tx, groupID, userID int) error {
	if err := lockGroupForRelation(ctx, tx, groupID); err != nil {
		return err
	}
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.users WHERE id = $1 FOR KEY SHARE;`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return groupRepositoryError("gagal memeriksa user anggota", err)
	}
	return nil
}

// Kunci group menjaga keberadaan induk selama transaksi keanggotaan maupun pemberian akses database.
func lockGroupForRelation(ctx context.Context, tx *sql.Tx, groupID int) error {
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.groups WHERE id = $1 FOR KEY SHARE;`, groupID).Scan(&id)
	if err != nil {
		return groupRepositoryError("gagal memeriksa group", err)
	}
	return nil
}
