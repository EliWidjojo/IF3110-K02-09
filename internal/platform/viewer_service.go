package platform

import (
	"context"
	"database/sql"
	"time"
)

const viewerOperationTimeout = 5 * time.Second

type ViewerService struct {
	access   *EffectiveAccessService
	pools    *TargetConnectionManager
	metadata ViewerMetadataStore
}

func NewViewerService(access *EffectiveAccessService, pools *TargetConnectionManager, metadata ViewerMetadataStore) *ViewerService {
	return &ViewerService{access: access, pools: pools, metadata: metadata}
}

func (s *ViewerService) ListDatabases(ctx context.Context) ([]AccessibleDatabase, error) {
	if s == nil || s.access == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, viewerOperationTimeout)
	defer cancel()
	// Daftar berasal dari penyimpanan izin terkini dan tidak mengungkap konfigurasi koneksi atau membuka pool target.
	return s.access.ListAccessibleDatabases(ctx)
}

func (s *ViewerService) ListSchemas(ctx context.Context, databaseID int) ([]ViewerSchema, error) {
	if databaseID <= 0 {
		return nil, ErrInvalidViewerInput
	}
	ctx, cancel := context.WithTimeout(ctx, viewerOperationTimeout)
	defer cancel()
	db, err := s.readableDatabase(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	return s.metadata.ListSchemas(ctx, db)
}

func (s *ViewerService) ListTables(ctx context.Context, databaseID int, schema string) ([]ViewerTable, error) {
	if databaseID <= 0 || !validViewerIdentifier(schema) {
		return nil, ErrInvalidViewerInput
	}
	ctx, cancel := context.WithTimeout(ctx, viewerOperationTimeout)
	defer cancel()
	db, err := s.readableDatabase(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	return s.metadata.ListTables(ctx, db, schema)
}

func (s *ViewerService) readableDatabase(ctx context.Context, databaseID int) (*sql.DB, error) {
	if s == nil || s.access == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	// Izin selalu dibaca ulang sebelum Get yang dapat melakukan Ping atau membuka koneksi target.
	if err := s.access.RequireRead(ctx, databaseID); err != nil {
		return nil, err
	}
	if s.pools == nil || s.metadata == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	return s.pools.Get(ctx, databaseID)
}
