package platform

import (
	"context"
	"database/sql/driver"
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
		return nil, targetConnectionError("menyiapkan koneksi target", ErrTargetDriverDebug)
	}
	connectCtx, cancel := context.WithTimeout(ctx, c.configuration.ConnectTimeout)
	defer cancel()
	dialer := &targetContextDialer{dialer: net.Dialer{Timeout: c.configuration.ConnectTimeout, KeepAlive: 30 * time.Second}}
	connector, err := pq.NewConnectorConfig(c.configuration)
	if err != nil {
		return nil, targetConnectionError("menyiapkan koneksi target", err)
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
		return nil, targetConnectionError("membuka koneksi target", err)
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
	stop := context.AfterFunc(ctx, func() {
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
