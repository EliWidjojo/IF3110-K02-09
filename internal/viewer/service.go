package viewer

import (
	"context"
	"database/sql"
	"time"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/database"
)

const viewerOperationTimeout = 5 * time.Second

type ViewerService struct {
	access   *access.EffectiveAccessService
	pools    *database.TargetConnectionManager
	metadata ViewerMetadataStore
}

func NewViewerService(access *access.EffectiveAccessService, pools *database.TargetConnectionManager, metadata ViewerMetadataStore) *ViewerService {
	return &ViewerService{access: access, pools: pools, metadata: metadata}
}

func (s *ViewerService) ListDatabases(ctx context.Context) ([]access.AccessibleDatabase, error) {
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
	if databaseID <= 0 || !ValidIdentifier(schema) {
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

func (s *ViewerService) ReadTableData(ctx context.Context, databaseID int, schema, table string, pagination ViewerPagination) (ViewerTableData, error) {
	if databaseID <= 0 || !ValidIdentifier(schema) || !ValidIdentifier(table) {
		return ViewerTableData{}, ErrInvalidViewerInput
	}
	pagination, err := normalizedViewerPagination(pagination)
	if err != nil {
		return ViewerTableData{}, err
	}
	if s == nil {
		return ViewerTableData{}, ErrViewerDependencyUnavailable
	}
	reader, ok := s.metadata.(ViewerTableDataStore)
	if !ok {
		return ViewerTableData{}, ErrViewerDependencyUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, viewerOperationTimeout)
	defer cancel()
	// Pemeriksaan akses efektif tetap mendahului pembukaan pool dan seluruh pembacaan metadata maupun data target.
	db, err := s.readableDatabase(ctx, databaseID)
	if err != nil {
		return ViewerTableData{}, err
	}
	return reader.ReadTableData(ctx, db, schema, table, pagination)
}

func (s *ViewerService) GetTableMetadata(ctx context.Context, databaseID int, schema, table string) (ViewerTableMetadata, error) {
	if databaseID <= 0 || !ValidIdentifier(schema) || !ValidIdentifier(table) {
		return ViewerTableMetadata{}, ErrInvalidViewerInput
	}
	if s == nil {
		return ViewerTableMetadata{}, ErrViewerDependencyUnavailable
	}
	reader, ok := s.metadata.(ViewerTableMetadataStore)
	if !ok {
		return ViewerTableMetadata{}, ErrViewerDependencyUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, viewerOperationTimeout)
	defer cancel()
	// Jalur akses yang sama memeriksa identitas dan izin terkini sebelum koneksi atau query metadata digunakan.
	db, err := s.readableDatabase(ctx, databaseID)
	if err != nil {
		return ViewerTableMetadata{}, err
	}
	return reader.GetTableMetadata(ctx, db, schema, table)
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
