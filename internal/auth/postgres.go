package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
)

type IdentityStore interface {
	Snapshot(int) (User, access.Principal, error)
	FindUsername(string) (User, bool)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Snapshot(id int) (User, access.Principal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var u User
	var grants []byte
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.username,COALESCE(u.password_hash,''),u.is_platform_admin,NOT u.is_active,u.security_version,u.privilege_version,
 COALESCE((SELECT jsonb_object_agg(database_id,level) FROM
 (SELECT a.database_id, CASE WHEN bool_or(a.access_level='View + Edit') THEN 'View + Edit' ELSE 'View Only' END level
 FROM platform.group_memberships m JOIN platform.group_database_access a ON a.group_id=m.group_id WHERE m.user_id=u.id GROUP BY a.database_id) g),'{}'::jsonb)
 FROM platform.users u WHERE u.id=$1`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Admin, &u.Disabled, &u.SecurityVersion, &u.PrivilegeVersion, &grants)
	if err != nil || u.Disabled {
		return User{}, access.Principal{}, ErrNotFound
	}
	p := access.Principal{ID: u.ID, Username: u.Username, Admin: u.Admin, Databases: map[string]access.Level{}}
	if json.Unmarshal(grants, &p.Databases) != nil {
		return User{}, access.Principal{}, ErrNotFound
	}
	return u, p, nil
}

func (s *PostgresStore) FindUsername(name string) (User, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id int
	if s.db.QueryRowContext(ctx, `SELECT id FROM platform.users WHERE username=$1`, name).Scan(&id) != nil {
		return User{}, false
	}
	u, _, err := s.Snapshot(id)
	return u, err == nil
}

// Bootstrap hanya membuat admin pertama dan tidak pernah mengganti kredensial yang sudah tersimpan.
func (s *PostgresStore) Bootstrap(ctx context.Context, name, password string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("penyimpanan identitas tidak tersedia")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `LOCK TABLE platform.users IN EXCLUSIVE MODE`); err != nil {
		return errors.New("bootstrap tidak dapat dikunci")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.users`).Scan(&count); err != nil {
		return errors.New("identitas tidak dapat dibaca")
	}
	if count == 0 {
		if name == "" {
			return errors.New("startup pertama memerlukan BOOTSTRAP_ADMIN_USERNAME dan BOOTSTRAP_ADMIN_PASSWORD")
		}
		hash, e := HashPassword(password)
		if e != nil {
			return errors.New("password bootstrap harus 12–1024 byte")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO platform.users(username,password_hash,is_platform_admin) VALUES($1,$2,true)`, name, hash); err != nil {
			return errors.New("bootstrap admin gagal")
		}
	}
	return tx.Commit()
}

func (h *HTTP) VerifiedUserID(ctx context.Context) (int, error) {
	p, ok := access.FromContext(ctx)
	if !ok {
		return 0, admin.ErrUnauthenticated
	}
	return p.ID, nil
}
func (h *HTTP) RevokeUserSessions(_ context.Context, id int) error {
	h.Sessions.RevokeUser(id)
	return nil
}
func (h *HTTP) ValidateCSRF(r *http.Request) error {
	p, err := h.Sessions.Get(h.token(r))
	if err != nil || p.UserID == 0 || !h.csrf(r, p) {
		return admin.ErrInvalidUserCSRF
	}
	u, _, err := h.Store.Snapshot(p.UserID)
	if err != nil || u.SecurityVersion != p.SecurityVersion {
		return admin.ErrInvalidUserCSRF
	}
	return nil
}
