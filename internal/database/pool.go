package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

type TargetPoolOptions struct {
	ConnectTimeout  time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Gate per ID menyerialkan pembuatan, pemeriksaan, dan invalidasi tanpa mengunci pool database lain.
type targetPoolState struct {
	gate chan struct{}
	db   *sql.DB
}

type TargetConnectionManager struct {
	repository *DatabaseConnectionRepository
	options    TargetPoolOptions
	mu         sync.Mutex
	pools      map[int]*targetPoolState
	shutdown   context.Context
	cancel     context.CancelFunc
	closed     bool
	closeDone  chan struct{}
	closeErr   error
}

// Constructor memasang invalidasi pada instance repository yang sama agar Update dan Delete menutup pool terkait.
func NewTargetConnectionManager(repository *DatabaseConnectionRepository, options TargetPoolOptions) (*TargetConnectionManager, error) {
	if repository == nil {
		return nil, ErrTargetManagerUnavailable
	}
	options, err := normalizedTargetPoolOptions(options)
	if err != nil {
		return nil, err
	}
	shutdown, cancel := context.WithCancel(context.Background())
	manager := &TargetConnectionManager{
		repository: repository, options: options, pools: make(map[int]*targetPoolState),
		shutdown: shutdown, cancel: cancel, closeDone: make(chan struct{}),
	}
	if err := repository.attachTargetPools(manager); err != nil {
		cancel()
		return nil, err
	}
	return manager, nil
}

// Get hanya mengelola sumber daya koneksi target; pemanggil tetap wajib memeriksa akses efektif sebelum memakai pool.
func (m *TargetConnectionManager) Get(ctx context.Context, databaseID int) (*sql.DB, error) {
	if m == nil {
		return nil, ErrTargetManagerUnavailable
	}
	if databaseID <= 0 {
		return nil, ErrInvalidDatabaseConnection
	}
	operationCtx, cancel := context.WithTimeout(ctx, m.options.ConnectTimeout)
	stop := context.AfterFunc(m.shutdown, cancel)
	defer cancel()
	defer stop()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrTargetManagerClosed
	}
	state := m.pools[databaseID]
	if state == nil {
		state = &targetPoolState{gate: make(chan struct{}, 1)}
		m.pools[databaseID] = state
	}
	m.mu.Unlock()
	select {
	case state.gate <- struct{}{}:
		defer func() { <-state.gate }()
	case <-operationCtx.Done():
		return nil, WrapTargetError("menunggu pool target", operationCtx.Err())
	}
	if m.isClosed() {
		return nil, ErrTargetManagerClosed
	}
	if err := operationCtx.Err(); err != nil {
		return nil, WrapTargetError("mengambil pool target", err)
	}
	if state.db == nil {
		credentials, err := m.repository.LoadCredentials(operationCtx, databaseID)
		if err != nil {
			return nil, WrapTargetError("memuat konfigurasi target", err)
		}
		connector, err := newTargetPostgresConnector(credentials, m.options.ConnectTimeout)
		if err != nil {
			return nil, WrapTargetError("menyiapkan pool target", err)
		}
		state.db = sql.OpenDB(connector)
		state.db.SetMaxOpenConns(m.options.MaxOpenConns)
		state.db.SetMaxIdleConns(m.options.MaxIdleConns)
		state.db.SetConnMaxLifetime(m.options.ConnMaxLifetime)
		state.db.SetConnMaxIdleTime(m.options.ConnMaxIdleTime)
	}
	if err := state.db.PingContext(operationCtx); err != nil {
		pool := state.db
		state.db = nil
		closeErr := WrapTargetError("menutup pool target yang gagal", pool.Close())
		return nil, errors.Join(WrapTargetError("memeriksa koneksi target", err), closeErr)
	}
	if m.isClosed() {
		pool := state.db
		state.db = nil
		return nil, errors.Join(ErrTargetManagerClosed, WrapTargetError("menutup pool saat shutdown", pool.Close()))
	}
	return state.db, nil
}

func (m *TargetConnectionManager) CheckConnectivity(ctx context.Context, databaseID int) error {
	_, err := m.Get(ctx, databaseID)
	return err
}

// Invalidasi tidak mengikuti pembatalan request penulis karena perubahan konfigurasi mungkin sudah di-commit.
func (m *TargetConnectionManager) Invalidate(databaseID int) error {
	if m == nil {
		return ErrTargetManagerUnavailable
	}
	if databaseID <= 0 {
		return ErrInvalidDatabaseConnection
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return m.closeErr
	}
	state := m.pools[databaseID]
	m.mu.Unlock()
	if state == nil {
		return nil
	}
	state.gate <- struct{}{}
	defer func() { <-state.gate }()
	pool := state.db
	state.db = nil
	if pool == nil {
		return nil
	}
	return WrapTargetError("menginvalidasi pool target", pool.Close())
}

// Close bersifat idempoten, membatalkan pembentukan pool, dan hanya menutup database target yang dikelola instance ini.
func (m *TargetConnectionManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return m.closeErr
	}
	m.closed = true
	m.cancel()
	states := make([]*targetPoolState, 0, len(m.pools))
	for _, state := range m.pools {
		states = append(states, state)
	}
	m.mu.Unlock()
	var closeErrors []error
	for _, state := range states {
		state.gate <- struct{}{}
		if state.db != nil {
			closeErrors = append(closeErrors, WrapTargetError("menutup pool target", state.db.Close()))
			state.db = nil
		}
		<-state.gate
	}
	m.repository.detachTargetPools(m)
	m.closeErr = errors.Join(closeErrors...)
	close(m.closeDone)
	return m.closeErr
}

func (m *TargetConnectionManager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func normalizedTargetPoolOptions(options TargetPoolOptions) (TargetPoolOptions, error) {
	if options.ConnectTimeout < 0 || options.MaxOpenConns < 0 || options.MaxIdleConns < 0 ||
		options.ConnMaxLifetime < 0 || options.ConnMaxIdleTime < 0 {
		return TargetPoolOptions{}, ErrInvalidTargetPoolOptions
	}
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = 5 * time.Second
	}
	if options.MaxOpenConns == 0 {
		options.MaxOpenConns = 10
	}
	if options.MaxIdleConns == 0 {
		options.MaxIdleConns = min(2, options.MaxOpenConns)
	}
	if options.MaxIdleConns > options.MaxOpenConns {
		return TargetPoolOptions{}, ErrInvalidTargetPoolOptions
	}
	if options.ConnMaxLifetime == 0 {
		options.ConnMaxLifetime = 30 * time.Minute
	}
	if options.ConnMaxIdleTime == 0 {
		options.ConnMaxIdleTime = 5 * time.Minute
	}
	return options, nil
}
