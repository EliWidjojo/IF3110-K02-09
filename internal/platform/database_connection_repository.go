package platform

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

var (
	ErrDatabaseConnectionNameTaken = errors.New("nama konfigurasi database sudah dipakai")
	ErrDatabaseConnectionStorage   = errors.New("penyimpanan konfigurasi database gagal")
	ErrInvalidDatabaseConnection   = errors.New("konfigurasi database tidak valid")
)

type CreateDatabaseConnectionParams struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	DatabaseName string `json:"database_name"`
	Username     string `json:"username"`
	SSLMode      string `json:"ssl_mode"`
	SSLRootCert  string `json:"ssl_root_cert"`
	Password     string `json:"-"`
}

// Password nil mempertahankan ciphertext lama, sedangkan pointer ke string kosong secara eksplisit mengganti password menjadi kosong.
type UpdateDatabaseConnectionParams struct {
	Name         *string `json:"name"`
	Host         *string `json:"host"`
	Port         *int    `json:"port"`
	DatabaseName *string `json:"database_name"`
	Username     *string `json:"username"`
	SSLMode      *string `json:"ssl_mode"`
	SSLRootCert  *string `json:"ssl_root_cert"`
	Password     *string `json:"-"`
}

// DatabaseConnectionCredentials hanya digunakan komponen backend berwenang dan tidak diserialisasi sebagai respons publik.
type DatabaseConnectionCredentials struct {
	Configuration DatabaseConnection `json:"-"`
	Password      string             `json:"-"`
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
	db        *sql.DB
	passwords *DatabasePasswordCipher
}

func NewDatabaseConnectionRepository(db *sql.DB, passwords *DatabasePasswordCipher) *DatabaseConnectionRepository {
	return &DatabaseConnectionRepository{db: db, passwords: passwords}
}

const databaseConnectionColumns = `id, name, host, port, database_name, username, ssl_mode, ssl_root_cert`

func (r *DatabaseConnectionRepository) Create(ctx context.Context, params CreateDatabaseConnectionParams) (DatabaseConnection, error) {
	if params.Port == 0 {
		params.Port = 5432
	}
	if params.SSLMode == "" {
		params.SSLMode = "require"
	}
	if !validConnectionText(params.Name) || !validConnectionHost(params.Host) ||
		!validConnectionText(params.DatabaseName) || !validConnectionText(params.Username) ||
		params.Port < 1 || params.Port > 65535 || !validConnectionSSLMode(params.SSLMode) ||
		!validOptionalConnectionText(params.SSLRootCert) || strings.ContainsRune(params.Password, '\x00') {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
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
	ciphertext, err := r.passwords.encrypt(id, params.Password)
	if err != nil {
		return DatabaseConnection{}, err
	}
	query := `
		INSERT INTO platform.database_connections
			(id, name, host, port, database_name, username, ssl_mode, ssl_root_cert, encrypted_credentials)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + databaseConnectionColumns + `;`
	connection, err := scanDatabaseConnection(tx.QueryRowContext(ctx, query, id, params.Name, params.Host,
		params.Port, params.DatabaseName, params.Username, params.SSLMode, params.SSLRootCert, ciphertext))
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
		&username, &record.configuration.SSLMode, &record.configuration.SSLRootCert, &record.ciphertext)
	if err != nil {
		return DatabaseConnectionCredentials{}, databaseConnectionRepositoryError("gagal mengambil kredensial koneksi", err)
	}
	if !username.Valid || !validConnectionText(username.String) {
		return DatabaseConnectionCredentials{}, ErrInvalidDatabaseConnection
	}
	record.configuration.Username = username.String
	password, err := r.passwords.decrypt(record.configuration.ID, record.ciphertext)
	if err != nil {
		return DatabaseConnectionCredentials{}, err
	}
	return DatabaseConnectionCredentials{Configuration: record.configuration, Password: password}, nil
}

func (r *DatabaseConnectionRepository) Update(ctx context.Context, id int, params UpdateDatabaseConnectionParams) (DatabaseConnection, error) {
	if id <= 0 || !validDatabaseConnectionUpdate(params) {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
	}
	// Interface nil menjadi SQL NULL agar COALESCE mempertahankan password lama tanpa membaca atau mendekripsinya.
	var ciphertext any
	if params.Password != nil {
		encrypted, err := r.passwords.encrypt(id, *params.Password)
		if err != nil {
			return DatabaseConnection{}, err
		}
		ciphertext = encrypted
	}
	query := `
		UPDATE platform.database_connections SET
			name = COALESCE($1, name), host = COALESCE($2, host), port = COALESCE($3, port),
			database_name = COALESCE($4, database_name), username = COALESCE($5, username),
			ssl_mode = COALESCE($6, ssl_mode), ssl_root_cert = COALESCE($7, ssl_root_cert),
			encrypted_credentials = COALESCE($8, encrypted_credentials)
		WHERE id = $9 RETURNING ` + databaseConnectionColumns + `;`
	connection, err := scanDatabaseConnection(r.db.QueryRowContext(ctx, query, params.Name, params.Host,
		params.Port, params.DatabaseName, params.Username, params.SSLMode, params.SSLRootCert, ciphertext, id))
	if err != nil {
		return DatabaseConnection{}, databaseConnectionRepositoryError("gagal memperbarui konfigurasi", err)
	}
	return connection, nil
}

// Delete mengandalkan foreign key ON DELETE CASCADE untuk mencabut pemberian akses yang merujuk konfigurasi ini.
func (r *DatabaseConnectionRepository) Delete(ctx context.Context, id int) error {
	if id <= 0 {
		return ErrInvalidDatabaseConnection
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM platform.database_connections WHERE id = $1;`, id)
	if err != nil {
		return databaseConnectionRepositoryError("gagal menghapus konfigurasi", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return databaseConnectionRepositoryError("gagal memeriksa penghapusan konfigurasi", err)
	}
	if count == 0 {
		return ErrDatabaseConnectionNotFound
	}
	return nil
}

func scanDatabaseConnection(row interface{ Scan(...any) error }) (DatabaseConnection, error) {
	var connection DatabaseConnection
	var username sql.NullString
	err := row.Scan(&connection.ID, &connection.Name, &connection.Host, &connection.Port,
		&connection.DatabaseName, &username, &connection.SSLMode, &connection.SSLRootCert)
	connection.Username = username.String
	return connection, err
}

func validDatabaseConnectionUpdate(params UpdateDatabaseConnectionParams) bool {
	if params.Name == nil && params.Host == nil && params.Port == nil && params.DatabaseName == nil &&
		params.Username == nil && params.SSLMode == nil && params.SSLRootCert == nil && params.Password == nil {
		return false
	}
	for _, value := range []*string{params.Name, params.DatabaseName, params.Username} {
		if value != nil && !validConnectionText(*value) {
			return false
		}
	}
	return (params.Host == nil || validConnectionHost(*params.Host)) &&
		(params.Port == nil || (*params.Port >= 1 && *params.Port <= 65535)) &&
		(params.SSLMode == nil || validConnectionSSLMode(*params.SSLMode)) &&
		(params.SSLRootCert == nil || validOptionalConnectionText(*params.SSLRootCert)) &&
		(params.Password == nil || !strings.ContainsRune(*params.Password, '\x00'))
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
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w: %w", operation, ErrDatabaseConnectionStorage, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w: %w", operation, ErrDatabaseConnectionStorage, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", operation, ErrDatabaseConnectionStorage)
}
