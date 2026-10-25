package platform

import (
	"context"
	"errors"
)

var ErrDatabaseConnectionDependencyUnavailable = errors.New("komponen administrasi database belum tersedia")

type DatabaseConnectionService struct {
	repository *DatabaseConnectionRepository
	pools      *TargetConnectionManager
	admin      PlatformAdminAuthorizer
}

func NewDatabaseConnectionService(repository *DatabaseConnectionRepository, pools *TargetConnectionManager, admin PlatformAdminAuthorizer) *DatabaseConnectionService {
	return &DatabaseConnectionService{repository: repository, pools: pools, admin: admin}
}

func (s *DatabaseConnectionService) requireAdmin(ctx context.Context) error {
	if s == nil || s.repository == nil || s.admin == nil {
		return ErrDatabaseConnectionDependencyUnavailable
	}
	err := s.admin.RequirePlatformAdmin(ctx)
	if errors.Is(err, ErrUserDependencyUnavailable) {
		return ErrDatabaseConnectionDependencyUnavailable
	}
	return err
}

// Instance repository harus sama agar jalur penulisan selalu memakai invalidasi otomatis milik pengelola pool.
func (s *DatabaseConnectionService) requirePoolManager() error {
	if s.pools == nil || s.pools.repository != s.repository || s.pools.isClosed() {
		return ErrDatabaseConnectionDependencyUnavailable
	}
	s.repository.poolMu.RLock()
	attached := s.repository.targetPools == s.pools
	s.repository.poolMu.RUnlock()
	if !attached {
		return ErrDatabaseConnectionDependencyUnavailable
	}
	return nil
}

func (s *DatabaseConnectionService) Create(ctx context.Context, params CreateDatabaseConnectionParams) (DatabaseConnection, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return DatabaseConnection{}, err
	}
	if err := s.requirePoolManager(); err != nil {
		return DatabaseConnection{}, err
	}
	params, err := normalizedDatabaseConnectionCreate(params)
	if err != nil {
		return DatabaseConnection{}, err
	}
	return s.repository.Create(ctx, params)
}

func (s *DatabaseConnectionService) GetByID(ctx context.Context, id int) (DatabaseConnection, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return DatabaseConnection{}, err
	}
	return s.repository.GetByID(ctx, id)
}

func (s *DatabaseConnectionService) List(ctx context.Context, limit, offset int) ([]DatabaseConnection, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.repository.List(ctx, limit, offset)
}

func (s *DatabaseConnectionService) Update(ctx context.Context, id int, params UpdateDatabaseConnectionParams) (DatabaseConnection, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return DatabaseConnection{}, err
	}
	if err := s.requirePoolManager(); err != nil {
		return DatabaseConnection{}, err
	}
	if id <= 0 || !validDatabaseConnectionUpdate(params) {
		return DatabaseConnection{}, ErrInvalidDatabaseConnection
	}
	return s.repository.Update(ctx, id, params)
}

func (s *DatabaseConnectionService) Delete(ctx context.Context, id int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if err := s.requirePoolManager(); err != nil {
		return err
	}
	// Repository menghapus referensi akses sesuai foreign key tanpa menjalankan SQL pada riwayat migration database target.
	return s.repository.Delete(ctx, id)
}

func (s *DatabaseConnectionService) CheckConnectivity(ctx context.Context, id int) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if err := s.requirePoolManager(); err != nil {
		return err
	}
	return s.pools.CheckConnectivity(ctx, id)
}
