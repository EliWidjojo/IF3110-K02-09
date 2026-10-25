package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"

	"github.com/lib/pq"
)

var (
	ErrGroupDatabaseAccessNotFound = errors.New("akses group terhadap database tidak ditemukan")
	ErrInvalidAccessLevel          = errors.New("tingkat akses harus View Only atau View + Edit")
)

type GrantRepository struct{ db *sql.DB }

func NewGrantRepository(db *sql.DB) *GrantRepository { return &GrantRepository{db: db} }

// SetDatabaseAccess memakai upsert pada primary key gabungan agar pemberian dan perubahan akses tidak menghasilkan duplikasi.
func (r *GrantRepository) SetDatabaseAccess(ctx context.Context, groupID, databaseID int, level AccessLevel) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	if !validAccessLevel(level) {
		return GroupDatabaseAccess{}, ErrInvalidAccessLevel
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memulai pemberian akses", err)
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
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memberikan akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal menyimpan akses database", err)
	}
	return access, nil
}

// GetDatabaseAccess selalu membaca tabel izin terkini agar pencabutan yang sudah di-commit berlaku pada pembacaan berikutnya.
func (r *GrantRepository) GetDatabaseAccess(ctx context.Context, groupID, databaseID int) (GroupDatabaseAccess, error) {
	if groupID <= 0 || databaseID <= 0 {
		return GroupDatabaseAccess{}, admin.ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal memulai pembacaan akses", err)
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
		return GroupDatabaseAccess{}, grantRepositoryError("gagal membaca akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return GroupDatabaseAccess{}, grantRepositoryError("gagal menyelesaikan pembacaan akses", err)
	}
	return access, nil
}

func (r *GrantRepository) ListDatabaseAccess(ctx context.Context, groupID, limit, offset int) ([]GroupDatabaseAccess, error) {
	if groupID <= 0 || limit < 0 || limit > 100 || offset < 0 {
		return nil, admin.ErrInvalidGroupInput
	}
	if limit == 0 {
		limit = 20
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, grantRepositoryError("gagal memulai daftar akses database", err)
	}
	defer tx.Rollback()

	if err := lockGrantGroup(ctx, tx, groupID); err != nil {
		return nil, err
	}
	query := `
		SELECT group_id, database_id, access_level FROM platform.group_database_access
		WHERE group_id = $1 ORDER BY database_id ASC LIMIT $2 OFFSET $3;
	`
	rows, err := tx.QueryContext(ctx, query, groupID, limit, offset)
	if err != nil {
		return nil, grantRepositoryError("gagal mengambil daftar akses database", err)
	}
	defer rows.Close()

	accesses := make([]GroupDatabaseAccess, 0)
	for rows.Next() {
		access, err := scanGroupDatabaseAccess(rows)
		if err != nil {
			return nil, grantRepositoryError("gagal membaca daftar akses database", err)
		}
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, grantRepositoryError("gagal menyelesaikan daftar akses database", err)
	}
	if err := rows.Close(); err != nil {
		return nil, grantRepositoryError("gagal menutup daftar akses database", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, grantRepositoryError("gagal menyelesaikan transaksi daftar akses", err)
	}
	return accesses, nil
}

// RevokeDatabaseAccess menghapus satu pemberian akses tanpa mengubah keanggotaan group atau konfigurasi database.
func (r *GrantRepository) RevokeDatabaseAccess(ctx context.Context, groupID, databaseID int) error {
	if groupID <= 0 || databaseID <= 0 {
		return admin.ErrInvalidGroupInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return grantRepositoryError("gagal memulai pencabutan akses", err)
	}
	defer tx.Rollback()

	if err := lockDatabaseAccessParents(ctx, tx, groupID, databaseID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM platform.group_database_access WHERE group_id = $1 AND database_id = $2;`, groupID, databaseID)
	if err != nil {
		return grantRepositoryError("gagal mencabut akses database", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return grantRepositoryError("gagal memeriksa pencabutan akses", err)
	}
	if count == 0 {
		return ErrGroupDatabaseAccessNotFound
	}
	if err := tx.Commit(); err != nil {
		return grantRepositoryError("gagal menyimpan pencabutan akses", err)
	}
	return nil
}

// Pemeriksaan hanya membaca ID konfigurasi yang sudah ada tanpa mengambil kredensial atau membuka koneksi target.
func lockDatabaseAccessParents(ctx context.Context, tx *sql.Tx, groupID, databaseID int) error {
	if err := lockGrantGroup(ctx, tx, groupID); err != nil {
		return err
	}
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.database_connections WHERE id = $1 FOR KEY SHARE;`, databaseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return database.ErrDatabaseConnectionNotFound
	}
	if err != nil {
		return grantRepositoryError("gagal memeriksa konfigurasi database target", err)
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

// Kunci group menjaga keberadaan induk selama transaksi pemberian atau pembacaan akses database.
func lockGrantGroup(ctx context.Context, tx *sql.Tx, groupID int) error {
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM platform.groups WHERE id = $1 FOR KEY SHARE;`, groupID).Scan(&id)
	if err != nil {
		return grantRepositoryError("gagal memeriksa group", err)
	}
	return nil
}

// Error domain dapat diperiksa dengan errors.Is menggunakan pola yang sama dengan repository user.
func grantRepositoryError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, admin.ErrGroupNotFound)
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "groups_name_key" {
		return fmt.Errorf("%s: %w", operation, admin.ErrGroupNameTaken)
	}
	return fmt.Errorf("%s: %w: %w", operation, admin.ErrGroupStorage, err)
}
