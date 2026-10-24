package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

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

	if err := lockMembershipGroup(ctx, tx, groupID); err != nil {
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
	if err := lockMembershipGroup(ctx, tx, groupID); err != nil {
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

// Kunci group juga membedakan group tidak ditemukan dari daftar anggota kosong secara konsisten selama transaksi.
func lockMembershipGroup(ctx context.Context, tx *sql.Tx, groupID int) error {
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.groups WHERE id = $1 FOR KEY SHARE;`, groupID).Scan(&id)
	if err != nil {
		return groupRepositoryError("gagal memeriksa group anggota", err)
	}
	return nil
}
