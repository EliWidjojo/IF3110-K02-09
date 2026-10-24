package platform

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrDatabaseConnectionNotFound  = errors.New("konfigurasi database target tidak ditemukan")
	ErrGroupDatabaseAccessNotFound = errors.New("akses group terhadap database tidak ditemukan")
	ErrInvalidAccessLevel          = errors.New("tingkat akses harus View Only atau View + Edit")
)

// SetDatabaseAccess memakai upsert pada primary key gabungan agar pemberian dan perubahan akses tidak menghasilkan duplikasi.
func (r *GroupRepository) SetDatabaseAccess(ctx context.Context, groupID, databaseID int, level AccessLevel) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, ErrInvalidGroupInput
	}
	if !validAccessLevel(level) {
		return GroupDatabaseAccess{}, ErrInvalidAccessLevel
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal memulai pemberian akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return GroupDatabaseAccess{}, err
	}
	query := `
		INSERT INTO platform.group_database_access (group_id, database_id, access_level)
		VALUES ($1, $2, $3)
		ON CONFLICT (group_id, database_id)
		DO UPDATE SET access_level = EXCLUDED.access_level
		RETURNING group_id, database_id, access_level;
	`
	access, err := scanGroupDatabaseAccess(tx.QueryRowContext(ctx, query, groupID, databaseID, string(level)))
	if err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal memberikan akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal menyimpan akses database", err)
	}
	return access, nil
}

// GetDatabaseAccess selalu membaca tabel izin terkini agar pencabutan yang sudah di-commit berlaku pada pembacaan berikutnya.
func (r *GroupRepository) GetDatabaseAccess(ctx context.Context, groupID, databaseID int) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal memulai pembacaan akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return GroupDatabaseAccess{}, err
	}
	query := `
		SELECT group_id, database_id, access_level FROM platform.group_database_access
		WHERE group_id = $1 AND database_id = $2;
	`
	access, err := scanGroupDatabaseAccess(tx.QueryRowContext(ctx, query, groupID, databaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return GroupDatabaseAccess{}, ErrGroupDatabaseAccessNotFound
	}
	if err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal membaca akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, groupRepositoryError("gagal menyelesaikan pembacaan akses", err)
	}
	return access, nil
}

func (r *GroupRepository) ListDatabaseAccess(ctx context.Context, groupID, limit, offset int) ([]GroupDatabaseAccess, error) {
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidGroupInput
	}
	if limit == 0 {
		limit = 20
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, groupRepositoryError("gagal memulai daftar akses database", err)
	}
	defer tx.Rollback()

	if err := lockGroupForRelation(ctx, tx, groupID); err != nil {
		return nil, err
	}
	query := `
		SELECT group_id, database_id, access_level FROM platform.group_database_access
		WHERE group_id = $1 ORDER BY database_id ASC LIMIT $2 OFFSET $3;
	`
	rows, err := tx.QueryContext(ctx, query, groupID, limit, offset)
	if err != nil {
		return nil, groupRepositoryError("gagal mengambil daftar akses database", err)
	}
	defer rows.Close()

	accesses := make([]GroupDatabaseAccess, 0)
	for rows.Next() {
		access, err := scanGroupDatabaseAccess(rows)
		if err != nil {
			return nil, groupRepositoryError("gagal membaca daftar akses database", err)
		}
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, groupRepositoryError("gagal menyelesaikan daftar akses database", err)
	}
	if err := rows.Close(); err != nil {
		return nil, groupRepositoryError("gagal menutup daftar akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, groupRepositoryError("gagal menyelesaikan transaksi daftar akses", err)
	}
	return accesses, nil
}

// RevokeDatabaseAccess menghapus satu pemberian akses tanpa mengubah keanggotaan group atau konfigurasi database.
func (r *GroupRepository) RevokeDatabaseAccess(ctx context.Context, groupID, databaseID int) error {
	if groupID <= 0 || databaseID <= 0 {
		return ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return groupRepositoryError("gagal memulai pencabutan akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM platform.group_database_access WHERE group_id = $1 AND database_id = $2;`, groupID, databaseID)
	if err != nil {
		return groupRepositoryError("gagal mencabut akses database", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return groupRepositoryError("gagal memeriksa pencabutan akses", err)
	}
	if count == 0 {
		return ErrGroupDatabaseAccessNotFound
	}
	if err := tx.Commit(); err != nil {
		return groupRepositoryError("gagal menyimpan pencabutan akses", err)
	}
	return nil
}

// Pemeriksaan hanya membaca ID konfigurasi yang sudah ada tanpa mengambil kredensial atau membuka koneksi target.
func lockDatabaseAccessParents(ctx context.Context, tx *sql.Tx, groupID, databaseID int) error {
	if err := lockGroupForRelation(ctx, tx, groupID); err != nil {
		return err
	}
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.database_connections WHERE id = $1 FOR KEY SHARE;`, databaseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDatabaseConnectionNotFound
	}
	if err != nil {
		return groupRepositoryError("gagal memeriksa konfigurasi database target", err)
	}
	return nil
}

func scanGroupDatabaseAccess(row interface{ Scan(...any) error }) (GroupDatabaseAccess, error) {
	var access GroupDatabaseAccess
	err := row.Scan(&access.GroupID, &access.DatabaseID, &access.AccessLevel)
	return access, err
}

func validAccessLevel(level AccessLevel) bool {
	return level == AccessViewOnly || level == AccessViewEdit
}
