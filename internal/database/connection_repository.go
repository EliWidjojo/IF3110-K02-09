package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lib/pq"
)

var (
	ErrDatabaseConnectionNotFound   = errors.New("konfigurasi database target tidak ditemukan")
	ErrDatabaseConnectionNameTaken  = errors.New("nama konfigurasi database sudah dipakai")
	ErrDatabaseConnectionStorage    = errors.New("penyimpanan konfigurasi database gagal")
	ErrInvalidDatabaseConnection    = errors.New("konfigurasi database tidak valid")
	ErrDatabaseConnectionReferenced = errors.New("konfigurasi database masih dirujuk data lain")
)

type CreateDatabaseConnectionParams struct {
	Driver        Dialect `json:"driver"`
	AuditUsername string  `json:"audit_username"`
	AuditPassword string  `json:"-"`
	Name          string  `json:"name"`
	Host          string  `json:"host"`
	Port          int     `json:"port"`
	DatabaseName  string  `json:"database_name"`
	Username      string  `json:"username"`
	SSLMode       string  `json:"ssl_mode"`
	SSLRootCert   string  `json:"ssl_root_cert"`
	Password      string  `json:"-"`
}

// Password nil mempertahankan ciphertext lama, sedangkan pointer ke string kosong secara eksplisit mengganti password menjadi kosong.
type UpdateDatabaseConnectionParams struct {
	Driver        *Dialect `json:"driver"`
	AuditUsername *string  `json:"audit_username"`
	AuditPassword *string  `json:"-"`
	Name          *string  `json:"name"`
	Host          *string  `json:"host"`
	Port          *int     `json:"port"`
	DatabaseName  *string  `json:"database_name"`
	Username      *string  `json:"username"`
	SSLMode       *string  `json:"ssl_mode"`
	SSLRootCert   *string  `json:"ssl_root_cert"`
	Password      *string  `json:"-"`
}

// DatabaseConnectionCredentials hanya digunakan komponen backend berwenang dan tidak diserialisasi sebagai respons publik.
type DatabaseConnectionCredentials struct {
	Configuration DatabaseConnection `json:"-"`
	Password      string             `json:"-"`
	AuditPassword string             `json:"-"`
}

// Record internal memisahkan ciphertext dari model publik agar pengambilan konfigurasi tidak mengekspos rahasia.
type databaseConnectionRecord struct {
	configuration DatabaseConnection
	ciphertext    []byte
}

func (CreateDatabaseConnectionParams) String() string   { return "[input koneksi disembunyikan]" }
func (CreateDatabaseConnectionParams) GoString() string { return "[input koneksi disembunyikan]" }
func (UpdateDatabaseConnectionParams) String() string   { return "[input koneksi disembunyikan]" }
func (UpdateDatabaseConnectionParams) GoString() string { return "[input koneksi disembunyikan]" }
func (DatabaseConnectionCredentials) String() string    { return "[kredensial koneksi disembunyikan]" }
func (DatabaseConnectionCredentials) GoString() string  { return "[kredensial koneksi disembunyikan]" }
func (databaseConnectionRecord) String() string         { return "[record koneksi disembunyikan]" }
func (databaseConnectionRecord) GoString() string       { return "[record koneksi disembunyikan]" }

type DatabaseConnectionRepository struct {
	db          *sql.DB
	passwords   *DatabasePasswordCipher
	poolMu      sync.RWMutex
	targetPools *TargetConnectionManager
}

func NewDatabaseConnectionRepository(db *sql.DB, passwords *DatabasePasswordCipher) *DatabaseConnectionRepository {
	return &DatabaseConnectionRepository{db: db, passwords: passwords}
}

const databaseConnectionColumns = `id, name, host, port, database_name, username, ssl_mode, ssl_root_cert,
	COALESCE(octet_length(encrypted_credentials), 0) > 0 AS password_configured,driver,audit_username`

func (r *DatabaseConnectionRepository) Create(ctx context.Context, params CreateDatabaseConnectionParams) (DatabaseConnection, error) {
	params, err := normalizedDatabaseConnectionCreate(params)
	if err != nil {
		return DatabaseConnection{}, err
	}
	if err := r.passwords.requireKey(); err != nil {
		return DatabaseConnection{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal memulai penyimpanan konfigurasi", err)
	}
	defer tx.Rollback()

	// ID dialokasikan sebelum enkripsi agar password terikat pada identitas koneksi sejak pertama disimpan.
	var id int
	err = tx.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('platform.database_connections', 'id'));`).Scan(&id)
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal menyiapkan identitas koneksi", err)
	}
	ciphertext, err := r.encryptSecrets(id, params.Password, params.AuditPassword)
	if err != nil {
		return DatabaseConnection{}, err
	}
	query := `
		INSERT INTO platform.database_connections
			(id, name, host, port, database_name, username, ssl_mode, ssl_root_cert, encrypted_credentials,driver,audit_username)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,$10,$11)
		RETURNING ` + databaseConnectionColumns + `;`
	connection, err := scanDatabaseConnection(tx.QueryRowContext(ctx, query, id, params.Name, params.Host,
		params.Port, params.DatabaseName, params.Username, params.SSLMode, params.SSLRootCert, ciphertext, params.Driver, params.AuditUsername))
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal menyimpan konfigurasi", err)
	}
	if err := tx.Commit(); err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal menyelesaikan penyimpanan konfigurasi", err)
	}
	return connection, nil
}

func (r *DatabaseConnectionRepository) GetByID(ctx context.Context, id int) (DatabaseConnection, error) {
	if id <= 0 {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
	}
	query := `SELECT ` + databaseConnectionColumns + ` FROM platform.database_connections WHERE id = $1;`
	connection, err := scanDatabaseConnection(r.db.QueryRowContext(ctx, query, id))
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal mengambil konfigurasi", err)
	}
	return connection, nil
}

// List mengambil metadata dan indikator password saja agar daftar admin tidak memuat kredensial terenkripsi maupun plaintext.
func (r *DatabaseConnectionRepository) List(ctx context.Context, limit, offset int) ([]DatabaseConnection, error) {
	if limit < 0 || limit > 100 || offset < 0 {
		return nil, ErrInvalidDatabaseConnection
	}
	if limit == 0 {
		limit = 20
	}
	query := `SELECT ` + databaseConnectionColumns + ` FROM platform.database_connections ORDER BY id ASC LIMIT $1 OFFSET $2;`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, databaseConnectionRepositoryError("gagal mengambil daftar konfigurasi", err)
	}
	defer rows.Close()
	connections := make([]DatabaseConnection, 0)
	for rows.Next() {
		connection, err := scanDatabaseConnection(rows)
		if err != nil {
			return nil, databaseConnectionRepositoryError("gagal membaca daftar konfigurasi", err)
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseConnectionRepositoryError("gagal menyelesaikan daftar konfigurasi", err)
	}
	return connections, nil
}

// LoadCredentials tidak menggantikan pemeriksaan izin dan hanya boleh dipakai backend setelah otorisasi database target.
func (r *DatabaseConnectionRepository) LoadCredentials(ctx context.Context, id int) (DatabaseConnectionCredentials, error) {
	if id <= 0 {
		return DatabaseConnectionCredentials{}, ErrInvalidDatabaseConnection
	}
	if err := r.passwords.requireKey(); err != nil {
		return DatabaseConnectionCredentials{}, err
	}
	query := `SELECT ` + databaseConnectionColumns + `, encrypted_credentials FROM platform.database_connections WHERE id = $1;`
	var record databaseConnectionRecord
	var username sql.NullString
	err := r.db.QueryRowContext(ctx, query, id).Scan(&record.configuration.ID, &record.configuration.Name,
		&record.configuration.Host, &record.configuration.Port, &record.configuration.DatabaseName,
		&username, &record.configuration.SSLMode, &record.configuration.SSLRootCert,
		&record.configuration.PasswordConfigured, &record.configuration.Driver, &record.configuration.AuditUsername, &record.ciphertext)
	if err != nil {
		return DatabaseConnectionCredentials{}, databaseConnectionRepositoryError("gagal mengambil kredensial koneksi", err)
	}
	if !username.Valid || !validConnectionText(username.String) {
		return DatabaseConnectionCredentials{}, ErrInvalidDatabaseConnection
	}
	record.configuration.Username = username.String
	password, auditPassword, err := r.decryptSecrets(record.configuration.ID, record.ciphertext)
	if err != nil {
		return DatabaseConnectionCredentials{}, err
	}
	return DatabaseConnectionCredentials{Configuration: record.configuration, Password: password, AuditPassword: auditPassword}, nil
}

func (r *DatabaseConnectionRepository) Update(ctx context.Context, id int, params UpdateDatabaseConnectionParams) (DatabaseConnection, error) {
	if id <= 0 || !validDatabaseConnectionUpdate(params) {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
	}
	var ciphertext any
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal memulai pembaruan konfigurasi", err)
	}
	defer tx.Rollback()
	current, err := scanDatabaseConnection(tx.QueryRowContext(ctx,
		`SELECT `+databaseConnectionColumns+` FROM platform.database_connections WHERE id = $1 FOR UPDATE;`, id))
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal mengambil konfigurasi untuk pembaruan", err)
	}
	// Validasi gabungan dilakukan dengan kunci baris agar patch TLS bersamaan tidak menghasilkan kombinasi yang tidak valid.
	if !validDatabaseConnectionConfiguration(mergedDatabaseConnection(current, params)) {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
	}
	if params.Password != nil || params.AuditPassword != nil {
		var stored []byte
		if err = tx.QueryRowContext(ctx, `SELECT encrypted_credentials FROM platform.database_connections WHERE id=$1`, id).Scan(&stored); err != nil {
			return DatabaseConnection{}, ErrDatabaseConnectionStorage
		}
		password, auditPassword, e := r.decryptSecrets(id, stored)
		if e != nil {
			return DatabaseConnection{}, e
		}
		if params.Password != nil {
			password = *params.Password
		}
		if params.AuditPassword != nil {
			auditPassword = *params.AuditPassword
		}
		ciphertext, err = r.encryptSecrets(id, password, auditPassword)
		if err != nil {
			return DatabaseConnection{}, err
		}
	}
	query := `
		UPDATE platform.database_connections SET
			name = COALESCE($1, name), host = COALESCE($2, host), port = COALESCE($3, port),
			database_name = COALESCE($4, database_name), username = COALESCE($5, username),
			ssl_mode = COALESCE($6, ssl_mode), ssl_root_cert = COALESCE($7, ssl_root_cert),
			encrypted_credentials = COALESCE($8, encrypted_credentials),driver=COALESCE($10,driver),audit_username=COALESCE($11,audit_username)
		WHERE id = $9 RETURNING ` + databaseConnectionColumns + `;`
	connection, err := scanDatabaseConnection(tx.QueryRowContext(ctx, query, params.Name, params.Host,
		params.Port, params.DatabaseName, params.Username, params.SSLMode, params.SSLRootCert, ciphertext, id, params.Driver, params.AuditUsername))
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal memperbarui konfigurasi", err)
	}
	commitErr := tx.Commit()
	// Pool diinvalidasi setelah kunci transaksi dilepas, termasuk saat hasil commit tidak dapat dipastikan.
	invalidationErr := r.invalidateTargetPool(id)
	if commitErr != nil {
		return DatabaseConnection{}, errors.Join(databaseConnectionRepositoryError("gagal menyimpan pembaruan konfigurasi", commitErr), invalidationErr)
	}
	if invalidationErr != nil {
		return DatabaseConnection{}, invalidationErr
	}
	return connection, nil
}

// Delete mengandalkan foreign key ON DELETE CASCADE untuk mencabut pemberian akses yang merujuk konfigurasi ini.
func (r *DatabaseConnectionRepository) Delete(ctx context.Context, id int) error {
	if id <= 0 {
		return ErrInvalidDatabaseConnection
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM platform.database_connections WHERE id = $1;`, id)
	invalidationErr := r.invalidateTargetPool(id)
	if err != nil {
		return errors.Join(databaseConnectionRepositoryError("gagal menghapus konfigurasi", err), invalidationErr)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return errors.Join(databaseConnectionRepositoryError("gagal memeriksa penghapusan konfigurasi", err), invalidationErr)
	}
	if count == 0 {
		return errors.Join(ErrDatabaseConnectionNotFound, invalidationErr)
	}
	return invalidationErr
}

func (r *DatabaseConnectionRepository) attachTargetPools(manager *TargetConnectionManager) error {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	if r.targetPools != nil {
		return ErrTargetManagerAlreadyAttached
	}
	r.targetPools = manager
	return nil
}

func (r *DatabaseConnectionRepository) detachTargetPools(manager *TargetConnectionManager) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	if r.targetPools == manager {
		r.targetPools = nil
	}
}

func (r *DatabaseConnectionRepository) invalidateTargetPool(databaseID int) error {
	r.poolMu.RLock()
	manager := r.targetPools
	r.poolMu.RUnlock()
	if manager == nil {
		return nil
	}
	return manager.Invalidate(databaseID)
}

func scanDatabaseConnection(row interface{ Scan(...any) error }) (DatabaseConnection, error) {
	var connection DatabaseConnection
	var username sql.NullString
	err := row.Scan(&connection.ID, &connection.Name, &connection.Host, &connection.Port,
		&connection.DatabaseName, &username, &connection.SSLMode, &connection.SSLRootCert, &connection.PasswordConfigured, &connection.Driver, &connection.AuditUsername)
	connection.Username = username.String
	return connection, err
}

func validDatabaseConnectionUpdate(params UpdateDatabaseConnectionParams) bool {
	if params.Name == nil && params.Host == nil && params.Port == nil && params.DatabaseName == nil &&
		params.Username == nil && params.SSLMode == nil && params.SSLRootCert == nil && params.Password == nil && params.AuditUsername == nil && params.AuditPassword == nil && params.Driver == nil {
		return false
	}
	for _, value := range []*string{params.Name, params.DatabaseName, params.Username} {
		if value != nil && !validConnectionText(*value) {
			return false
		}
	}
	return (params.Driver == nil || params.Driver.Valid()) && (params.AuditUsername == nil || validOptionalConnectionText(*params.AuditUsername)) && (params.AuditPassword == nil || !strings.ContainsRune(*params.AuditPassword, 0)) && (params.Host == nil || validConnectionHost(*params.Host)) &&
		(params.Port == nil || (*params.Port >= 1 && *params.Port <= 65535)) &&
		(params.SSLMode == nil || validConnectionSSLMode(*params.SSLMode)) &&
		(params.SSLRootCert == nil || validOptionalConnectionText(*params.SSLRootCert)) &&
		(params.Password == nil || !strings.ContainsRune(*params.Password, '\x00'))
}

func normalizedDatabaseConnectionCreate(params CreateDatabaseConnectionParams) (CreateDatabaseConnectionParams, error) {
	if params.Driver == "" {
		params.Driver = Postgres
	}
	if params.Port == 0 {
		params.Port = 5432
		if params.Driver.IsMySQL() {
			params.Port = 3306
		}
	}
	if params.SSLMode == "" {
		params.SSLMode = "require"
	}
	configuration := DatabaseConnection{
		Driver: params.Driver, AuditUsername: params.AuditUsername, Name: params.Name, Host: params.Host, Port: params.Port, DatabaseName: params.DatabaseName,
		Username: params.Username, SSLMode: params.SSLMode, SSLRootCert: params.SSLRootCert,
	}
	if !validDatabaseConnectionConfiguration(configuration) || strings.ContainsRune(params.Password, '\x00') {
		return CreateDatabaseConnectionParams{}, ErrInvalidDatabaseConnection
	}
	return params, nil
}

func mergedDatabaseConnection(configuration DatabaseConnection, params UpdateDatabaseConnectionParams) DatabaseConnection {
	for _, attribute := range []struct {
		input  *string
		stored *string
	}{
		{params.Name, &configuration.Name}, {params.Host, &configuration.Host},
		{params.DatabaseName, &configuration.DatabaseName}, {params.Username, &configuration.Username},
		{params.SSLMode, &configuration.SSLMode}, {params.SSLRootCert, &configuration.SSLRootCert},
	} {
		if attribute.input != nil {
			*attribute.stored = *attribute.input
		}
	}
	if params.Driver != nil {
		configuration.Driver = *params.Driver
	}
	if params.AuditUsername != nil {
		configuration.AuditUsername = *params.AuditUsername
	}
	if params.Port != nil {
		configuration.Port = *params.Port
	}
	return configuration
}

// Validasi gabungan menjaga pengaturan CA sistem tetap memakai verifikasi hostname penuh.
func validDatabaseConnectionConfiguration(configuration DatabaseConnection) bool {
	return configuration.Driver.Valid() && validOptionalConnectionText(configuration.AuditUsername) && validConnectionText(configuration.Name) && validConnectionHost(configuration.Host) &&
		configuration.Port >= 1 && configuration.Port <= 65535 &&
		validConnectionText(configuration.DatabaseName) && validConnectionText(configuration.Username) &&
		validConnectionSSLMode(configuration.SSLMode) && validOptionalConnectionText(configuration.SSLRootCert) &&
		(configuration.SSLRootCert != "system" || configuration.SSLMode == "verify-full")
}

func validConnectionText(value string) bool {
	return strings.TrimSpace(value) != "" && validOptionalConnectionText(value)
}

func validOptionalConnectionText(value string) bool {
	return utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func validConnectionHost(host string) bool {
	return validConnectionText(host) && strings.IndexFunc(host, unicode.IsSpace) == -1 &&
		!strings.ContainsAny(host, "=@?#") && !strings.Contains(host, "://")
}

func validConnectionSSLMode(mode string) bool {
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

// Error driver tidak dibungkus agar parameter sensitif atau detail baris tidak ikut terbawa ke response maupun log pemanggil.
func databaseConnectionRepositoryError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrDatabaseConnectionNotFound)
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "database_connections_name_key" {
		return fmt.Errorf("%s: %w", operation, ErrDatabaseConnectionNameTaken)
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%s: %w", operation, ErrDatabaseConnectionReferenced)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w: %w", operation, ErrDatabaseConnectionStorage, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w: %w", operation, ErrDatabaseConnectionStorage, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", operation, ErrDatabaseConnectionStorage)
}

type credentialEnvelope struct {
	Format        string `json:"format"`
	Password      string `json:"password"`
	AuditPassword string `json:"audit_password"`
}

func (r *DatabaseConnectionRepository) encryptSecrets(id int, password, auditPassword string) ([]byte, error) {
	b, err := json.Marshal(credentialEnvelope{"platform-credentials-v2", password, auditPassword})
	if err != nil {
		return nil, ErrDatabaseEncryption
	}
	defer clear(b)
	return r.passwords.encrypt(id, string(b))
}
func (r *DatabaseConnectionRepository) decryptSecrets(id int, ciphertext []byte) (string, string, error) {
	raw, err := r.passwords.decrypt(id, ciphertext)
	if err != nil {
		return "", "", err
	}
	var envelope credentialEnvelope
	if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Format == "platform-credentials-v2" {
		return envelope.Password, envelope.AuditPassword, nil
	}
	return raw, "", nil
}

// OpenAudit membuka akun pencatat terpisah tanpa membagikan kredensialnya kepada SQL pengguna.
func (r *DatabaseConnectionRepository) OpenAudit(ctx context.Context, id int) (*sql.DB, DatabaseConnection, error) {
	credentials, err := r.LoadCredentials(ctx, id)
	if err != nil {
		return nil, DatabaseConnection{}, err
	}
	c := credentials.Configuration
	if c.AuditUsername == "" || c.AuditUsername == c.Username {
		return nil, c, ErrInvalidDatabaseConnection
	}
	credentials.Configuration.Username = c.AuditUsername
	credentials.Password = credentials.AuditPassword
	if c.Driver.IsMySQL() {
		credentials.Configuration.DatabaseName = HistorySchema(c.Driver, c.DatabaseName)
	}
	db, err := openConfiguredTarget(ctx, credentials, 5*time.Second)
	return db, c, err
}
