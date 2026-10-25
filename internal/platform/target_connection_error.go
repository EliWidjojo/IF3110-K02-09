package platform

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/lib/pq"
)

var (
	ErrTargetConnectionFailed       = errors.New("operasi koneksi database target gagal")
	ErrTargetConnectionNetwork      = errors.New("jaringan database target tidak tersedia")
	ErrTargetConnectionAuth         = errors.New("authentication database target gagal")
	ErrTargetManagerClosed          = errors.New("pengelola koneksi target sudah ditutup")
	ErrTargetManagerUnavailable     = errors.New("pengelola koneksi target belum tersedia")
	ErrTargetManagerAlreadyAttached = errors.New("repository sudah memiliki pengelola koneksi target")
	ErrInvalidTargetPoolOptions     = errors.New("pengaturan pool target tidak valid")
	ErrTargetDriverDebug            = errors.New("debug protokol PostgreSQL harus dinonaktifkan")
)

// TargetConnectionError menyimpan penyebab asli secara privat dan hanya mengekspos klasifikasi aman serta SQLSTATE.
// Unwrap sengaja tidak mengembalikan error driver mentah agar pesan, detail server, dan kredensial tidak ikut tercatat.
type TargetConnectionError struct {
	Operation string `json:"operation"`
	SQLState  string `json:"sql_state,omitempty"`
	Category  string `json:"category"`
	cause     error
	safeCause error
}

func (e TargetConnectionError) Error() string {
	return e.Operation + ": " + ErrTargetConnectionFailed.Error()
}

func (e TargetConnectionError) GoString() string { return e.Error() }
func (e TargetConnectionError) Unwrap() []error {
	if e.safeCause == nil {
		return []error{ErrTargetConnectionFailed}
	}
	return []error{ErrTargetConnectionFailed, e.safeCause}
}

func targetConnectionError(operation string, err error) error {
	if err == nil {
		return nil
	}
	failure := &TargetConnectionError{Operation: operation, Category: "driver", cause: err}
	var existing *TargetConnectionError
	if errors.As(err, &existing) {
		failure.Category, failure.SQLState, failure.safeCause = existing.Category, existing.SQLState, existing.safeCause
		return failure
	}
	switch {
	case errors.Is(err, context.Canceled):
		failure.Category, failure.safeCause = "canceled", context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		failure.Category, failure.safeCause = "timeout", context.DeadlineExceeded
	default:
		var postgresError *pq.Error
		var networkError net.Error
		if errors.As(err, &postgresError) && validTargetSQLState(string(postgresError.Code)) {
			failure.SQLState = string(postgresError.Code)
			failure.Category = "postgresql"
			if strings.HasPrefix(failure.SQLState, "28") {
				failure.Category, failure.safeCause = "authentication", ErrTargetConnectionAuth
			}
		} else if errors.As(err, &networkError) {
			failure.Category, failure.safeCause = "network", ErrTargetConnectionNetwork
			if networkError.Timeout() {
				failure.Category, failure.safeCause = "timeout", context.DeadlineExceeded
			}
		} else {
			for _, safe := range []error{ErrDatabaseConnectionNotFound, ErrInvalidDatabaseConnection,
				ErrDatabaseEncryptionKey, ErrDatabaseDecryption, ErrDatabaseConnectionStorage, ErrTargetDriverDebug} {
				if errors.Is(err, safe) {
					failure.Category, failure.safeCause = "configuration", safe
					break
				}
			}
		}
	}
	return failure
}

func validTargetSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if (ch < '0' || ch > '9') && (ch < 'A' || ch > 'Z') {
			return false
		}
	}
	return true
}
