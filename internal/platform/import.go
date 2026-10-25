package platform

import (
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type legacyIdentity struct {
	Users  map[int]auth.User
	Groups map[int]struct {
		ID      int
		Name    string
		Members map[int]bool
		Grants  map[string]string
	}
}

// Impor bersifat eksplisit, atomik, dan menolak sumber yang berubah setelah berhasil diimpor.
func ImportLegacy(ctx context.Context, db *sql.DB, cipher *database.DatabasePasswordCipher, identityPath, configPath string) error {
	fail := errors.New("impor legacy ditolak; periksa format, konflik identitas, dan kelengkapan konfigurasi tanpa mengubah sumber")
	content, err := os.ReadFile(identityPath)
	if err != nil {
		return fail
	}
	var data legacyIdentity
	if json.Unmarshal(content, &data) != nil || len(data.Users) == 0 {
		return fail
	}
	var configs []database.Config
	var configBytes []byte
	if configPath != "" {
		configBytes, err = os.ReadFile(configPath)
		if err != nil || json.Unmarshal(configBytes, &configs) != nil {
			return fail
		}
	}
	source, err := filepath.Abs(identityPath)
	if err != nil {
		return fail
	}
	sum := sha256.Sum256(append(append([]byte{}, content...), configBytes...))
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(311009002)`); err != nil {
		return fail
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM platform.legacy_imports WHERE source_key=$1`, source).Scan(&existing)
	if err == nil {
		if existing != fingerprint {
			return fail
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fail
	}
	userIDs, groupIDs, targetIDs := map[int]int{}, map[int]int{}, map[string]int{}
	saveMap := func(kind, key string, id int) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO platform.legacy_id_map(source_key,kind,legacy_id,new_id) VALUES($1,$2,$3,$4)`, source, kind, key, id)
		return e
	}
	for old, u := range data.Users {
		if old <= 0 || u.Username == "" || !auth.ValidPasswordHash(u.PasswordHash) {
			return fail
		}
		var id int
		var hash string
		var active, admin bool
		err = tx.QueryRowContext(ctx, `SELECT id,password_hash,is_active,is_platform_admin FROM platform.users WHERE username=$1`, u.Username).Scan(&id, &hash, &active, &admin)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `INSERT INTO platform.users(username,password_hash,is_active,is_platform_admin) VALUES($1,$2,$3,$4) RETURNING id`, u.Username, u.PasswordHash, !u.Disabled, u.Admin).Scan(&id)
		} else if err == nil && (hash != u.PasswordHash || active == u.Disabled || admin != u.Admin) {
			return fail
		}
		if err != nil {
			return fail
		}
		userIDs[old] = id
		if saveMap("user", strconv.Itoa(old), id) != nil {
			return fail
		}
	}
	for _, c := range configs {
		if c.ID == "" || targetIDs[c.ID] != 0 || !c.Driver.Valid() {
			return fail
		}
		host, name, user, password, ssl, port := "", "", "", "", "disable", 0
		if c.Driver == database.Postgres {
			u, e := url.Parse(c.DSN)
			if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
				return fail
			}
			host, name, user = u.Hostname(), strings.TrimPrefix(u.Path, "/"), u.User.Username()
			password, _ = u.User.Password()
			port = 5432
			if u.Port() != "" {
				port, err = strconv.Atoi(u.Port())
				if err != nil {
					return fail
				}
			}
			ssl = u.Query().Get("sslmode")
			if ssl == "" {
				ssl = "require"
			}
		} else {
			m, e := mysql.ParseDSN(c.DSN)
			if e != nil || m.Net != "tcp" {
				return fail
			}
			u, e := url.Parse("tcp://" + m.Addr)
			if e != nil {
				return fail
			}
			host, name, user, password = u.Hostname(), m.DBName, m.User, m.Passwd
			port, err = strconv.Atoi(u.Port())
			if err != nil {
				return fail
			}
		}
		if host == "" || name == "" || user == "" || port < 1 || port > 65535 {
			return fail
		}
		var id int
		if tx.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('platform.database_connections','id'))`).Scan(&id) != nil {
			return fail
		}
		secret, e := cipher.EncryptImportedPassword(id, password)
		if e != nil {
			return fail
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO platform.database_connections(id,name,host,port,database_name,username,ssl_mode,encrypted_credentials,driver) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, c.Name, host, port, name, user, ssl, secret, c.Driver)
		if err != nil {
			return fail
		}
		targetIDs[c.ID] = id
		if saveMap("database", c.ID, id) != nil {
			return fail
		}
	}
	for old, g := range data.Groups {
		var id int
		if tx.QueryRowContext(ctx, `INSERT INTO platform.groups(name) VALUES($1) ON CONFLICT(name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, g.Name).Scan(&id) != nil {
			return fail
		}
		groupIDs[old] = id
		if saveMap("group", strconv.Itoa(old), id) != nil {
			return fail
		}
		for member, present := range g.Members {
			if !present {
				continue
			}
			if userIDs[member] == 0 {
				return fail
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO platform.group_memberships(user_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, userIDs[member], id); err != nil {
				return fail
			}
		}
		for target, level := range g.Grants {
			if targetIDs[target] == 0 || (level != "View Only" && level != "View + Edit") {
				return fail
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO platform.group_database_access(group_id,database_id,access_level) VALUES($1,$2,$3)`, id, targetIDs[target], level); err != nil {
				return fail
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO platform.legacy_imports(source_key,fingerprint) VALUES($1,$2)`, source, fingerprint); err != nil {
		return fail
	}
	if tx.Commit() != nil {
		return fail
	}
	return nil
}
