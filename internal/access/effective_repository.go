package access

import (
	"context"
	"database/sql"
	"fmt"

	"IF3110-K02-09/internal/admin"
)

type EffectiveAccessRepository struct {
	db *sql.DB
}

func NewEffectiveAccessRepository(db *sql.DB) *EffectiveAccessRepository {
	return &EffectiveAccessRepository{db: db}
}

// GetUserAccess membaca status aktif, keanggotaan, dan izin dalam satu query agar tidak mencampur hasil snapshot berbeda.
func (r *EffectiveAccessRepository) GetUserAccess(ctx context.Context, userID int) (UserAccessSnapshot, error) {
	if userID <= 0 {
		return UserAccessSnapshot{}, ErrInvalidAccessInput
	}
	query := `
		SELECT u.is_active, d.id, d.name, a.access_level,d.driver
		FROM platform.users AS u
		LEFT JOIN platform.group_memberships AS m ON m.user_id = u.id
		LEFT JOIN platform.group_database_access AS a ON a.group_id = m.group_id
		LEFT JOIN platform.database_connections AS d ON d.id = a.database_id
		WHERE u.id = $1
		ORDER BY d.id ASC, m.group_id ASC;
	`
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
	}
	defer rows.Close()

	snapshot := UserAccessSnapshot{Grants: make([]AccessibleDatabase, 0)}
	found := false
	for rows.Next() {
		var databaseID sql.NullInt64
		var name, level, driver sql.NullString
		if err := rows.Scan(&snapshot.IsActive, &databaseID, &name, &level, &driver); err != nil {
			return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
		}
		found = true
		// LEFT JOIN tetap menghasilkan status user meskipun user tidak memiliki group atau pemberian akses.
		if !databaseID.Valid && !name.Valid && !level.Valid {
			continue
		}
		if !databaseID.Valid || !name.Valid || !level.Valid {
			return UserAccessSnapshot{}, ErrAccessStorage
		}
		snapshot.Grants = append(snapshot.Grants, AccessibleDatabase{
			Driver: driver.String, DatabaseID: int(databaseID.Int64), Name: name.String, AccessLevel: AccessLevel(level.String),
		})
	}
	if err := rows.Err(); err != nil {
		return UserAccessSnapshot{}, fmt.Errorf("%w: %w", ErrAccessStorage, err)
	}
	if !found {
		return UserAccessSnapshot{}, admin.ErrUserNotFound
	}
	return snapshot, nil
}
