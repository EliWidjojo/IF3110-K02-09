package database

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/lib/pq"
)

// Konfigurasi bertipe menghindari interpolasi DSN dan pewarisan parameter environment milik database internal platform.
type targetPostgresConnector struct {
	configuration pq.Config
}

// Driver membaca PQGO_DEBUG saat inisialisasi package dan mode tersebut dapat mencetak password koneksi ke stderr.
var targetDriverDebugAtStartup = os.Getenv("PQGO_DEBUG") == "1"

func newTargetPostgresConnector(credentials DatabaseConnectionCredentials, timeout time.Duration) (*targetPostgresConnector, error) {
	if targetDriverDebugAtStartup || os.Getenv("PQGO_DEBUG") == "1" {
		return nil, ErrTargetDriverDebug
	}
	configuration := credentials.Configuration
	if configuration.ID <= 0 || !validConnectionHost(configuration.Host) ||
		configuration.Port < 1 || configuration.Port > 65535 ||
		!validConnectionText(configuration.DatabaseName) || !validConnectionText(configuration.Username) ||
		!validConnectionSSLMode(configuration.SSLMode) || !validOptionalConnectionText(configuration.SSLRootCert) {
		return nil, ErrInvalidDatabaseConnection
	}
	if configuration.SSLRootCert == "system" && configuration.SSLMode != "verify-full" {
		return nil, ErrInvalidDatabaseConnection
	}
	return &targetPostgresConnector{configuration: pq.Config{
		Host: configuration.Host, Port: uint16(configuration.Port), Database: configuration.DatabaseName,
		User: configuration.Username, Password: credentials.Password,
		// Path yang tidak dapat menjadi file mencegah fallback ke pgpass atau sertifikat client milik proses platform.
		Passfile: filepath.Join(os.DevNull, "disabled"), SSLCert: filepath.Join(os.DevNull, "disabled"),
		SSLMode: pq.SSLMode(configuration.SSLMode), SSLRootCert: configuration.SSLRootCert,
		SSLSNI: true, ConnectTimeout: timeout, ApplicationName: "database-management-platform",
		ClientEncoding: "UTF8", Datestyle: "ISO, MDY",
		MinProtocolVersion: pq.ProtocolVersion30, MaxProtocolVersion: pq.ProtocolVersion30,
	}}, nil
}

func (c *targetPostgresConnector) Driver() driver.Driver { return &pq.Driver{} }

func (c *targetPostgresConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if targetDriverDebugAtStartup || os.Getenv("PQGO_DEBUG") == "1" {
		return nil, WrapTargetError("menyiapkan koneksi target", ErrTargetDriverDebug)
	}
	connectCtx, cancel := context.WithTimeout(ctx, c.configuration.ConnectTimeout)
	defer cancel()
	dialer := &targetContextDialer{handshake: connectCtx, dialer: net.Dialer{Timeout: c.configuration.ConnectTimeout, KeepAlive: 30 * time.Second}}
	connector, err := pq.NewConnectorConfig(c.configuration)
	if err != nil {
		return nil, WrapTargetError("menyiapkan koneksi target", err)
	}
	connector.Dialer(dialer)
	connection, err := connector.Connect(connectCtx)
	// Pembatalan hanya mengawasi proses pembentukan koneksi, bukan umur koneksi yang sudah dikembalikan ke pool.
	dialer.finish()
	if err == nil && connectCtx.Err() != nil {
		connection.Close()
		err = connectCtx.Err()
	}
	if err != nil {
		if connectCtx.Err() != nil {
			err = connectCtx.Err()
		}
		return nil, WrapTargetError("membuka koneksi target", err)
	}
	return connection, nil
}

func (*targetPostgresConnector) String() string   { return "[parameter koneksi target disembunyikan]" }
func (*targetPostgresConnector) GoString() string { return "[parameter koneksi target disembunyikan]" }

type targetDialCancellation struct {
	stop func() bool
	done chan struct{}
}

// Dialer dibuat untuk satu Connect agar context juga membatalkan handshake TLS dan authentication yang sedang berjalan.
type targetContextDialer struct {
	handshake     context.Context
	dialer        net.Dialer
	cancellations []targetDialCancellation
}

func (d *targetContextDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *targetContextDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	dialer := d.dialer
	dialer.Timeout = timeout
	return dialer.Dial(network, address)
}

func (d *targetContextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	connection, err := d.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	// Context dial milik driver dapat berakhir sebelum handshake; pengawasan mengikuti umur Connect.
	watch := d.handshake
	if watch == nil {
		watch = ctx
	}
	stop := context.AfterFunc(watch, func() {
		defer close(done)
		connection.Close()
	})
	d.cancellations = append(d.cancellations, targetDialCancellation{stop: stop, done: done})
	return connection, nil
}

func (d *targetContextDialer) finish() {
	for _, cancellation := range d.cancellations {
		if !cancellation.stop() {
			<-cancellation.done
		}
	}
}

func openConfiguredTarget(ctx context.Context, credentials DatabaseConnectionCredentials, timeout time.Duration) (*sql.DB, error) {
	c := credentials.Configuration
	var db *sql.DB
	if c.Driver.IsMySQL() {
		cfg := mysql.NewConfig()
		cfg.User = c.Username
		cfg.Passwd = credentials.Password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
		cfg.DBName = c.DatabaseName
		cfg.ParseTime = true
		cfg.Params = map[string]string{"time_zone": "'+00:00'"}
		cfg.Loc = time.UTC
		cfg.Timeout = timeout
		cfg.ReadTimeout = 35 * time.Second
		cfg.WriteTimeout = timeout
		cfg.MultiStatements = false
		cfg.AllowAllFiles = false
		if c.SSLMode != "disable" {
			if c.SSLMode != "require" && c.SSLMode != "verify-full" {
				return nil, ErrInvalidDatabaseConnection
			}
			if c.SSLRootCert != "" && c.SSLRootCert != "system" {
				return nil, ErrInvalidDatabaseConnection
			}
			cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Host}
		}
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, ErrInvalidDatabaseConnection
		}
		db = sql.OpenDB(connector)
	} else {
		connector, err := newTargetPostgresConnector(credentials, timeout)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(connector)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, WrapTargetError("membuka target", err)
	}
	return db, nil
}
