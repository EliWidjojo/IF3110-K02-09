package viewer

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"IF3110-K02-09/internal/database"
)

var (
	ErrInvalidViewerInput          = errors.New("parameter viewer tidak valid")
	ErrViewerSchemaNotFound        = errors.New("schema viewer tidak ditemukan")
	ErrViewerMetadata              = errors.New("pembacaan metadata target gagal")
	ErrViewerDependencyUnavailable = errors.New("komponen viewer belum tersedia")
)

type ViewerSchema struct {
	Name string `json:"name"`
}

type ViewerTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

// Pool diberikan per operasi agar repository metadata tidak menyimpan koneksi atau kredensial database terpilih.
type ViewerMetadataStore interface {
	ListSchemas(context.Context, *sql.DB) ([]ViewerSchema, error)
	ListTables(context.Context, *sql.DB, string) ([]ViewerTable, error)
}

type ViewerMetadataRepository struct{}

func NewViewerMetadataRepository() *ViewerMetadataRepository {
	return &ViewerMetadataRepository{}
}

func (r *ViewerMetadataRepository) ListSchemas(ctx context.Context, db *sql.DB) ([]ViewerSchema, error) {
	if r == nil || db == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	// Prefix pg_ mencakup katalog, TOAST, dan schema sementara sehingga seluruh schema sistem disembunyikan.
	const query = `
		SELECT n.nspname
		FROM pg_catalog.pg_namespace AS n
		WHERE left(n.nspname, 3) <> 'pg_' AND n.nspname NOT IN ('information_schema','platform_migrations')
		ORDER BY n.nspname COLLATE "C" ASC;
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	schemas := make([]ViewerSchema, 0)
	for rows.Next() {
		var schema ViewerSchema
		if err := rows.Scan(&schema.Name); err != nil {
			return nil, viewerMetadataError(ctx, err)
		}
		schemas = append(schemas, schema)
	}
	if err := rows.Err(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	return schemas, nil
}

func (r *ViewerMetadataRepository) ListTables(ctx context.Context, db *sql.DB, schema string) ([]ViewerTable, error) {
	if !ValidIdentifier(schema) {
		return nil, ErrInvalidViewerInput
	}
	if r == nil || db == nil {
		return nil, ErrViewerDependencyUnavailable
	}
	// LEFT JOIN membedakan schema yang kosong dari schema tidak ada dalam satu snapshot katalog.
	const query = `
		SELECT c.relname, c.relkind::text
		FROM pg_catalog.pg_namespace AS n
		LEFT JOIN pg_catalog.pg_class AS c
			ON c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'f')
		WHERE n.nspname = $1
			AND left(n.nspname, 3) <> 'pg_' AND n.nspname NOT IN ('information_schema','platform_migrations')
		ORDER BY c.relname COLLATE "C" ASC, c.oid ASC;
	`
	rows, err := db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	tables := make([]ViewerTable, 0)
	found := false
	for rows.Next() {
		found = true
		var name, kind sql.NullString
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, viewerMetadataError(ctx, err)
		}
		if !name.Valid && !kind.Valid {
			continue
		}
		var tableType string
		switch kind.String {
		case "r":
			tableType = "table"
		case "p":
			tableType = "partitioned_table"
		case "f":
			tableType = "foreign_table"
		default:
			return nil, ErrViewerMetadata
		}
		if !name.Valid || !kind.Valid {
			return nil, ErrViewerMetadata
		}
		tables = append(tables, ViewerTable{Schema: schema, Name: name.String, Type: tableType})
	}
	if err := rows.Err(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	if !found {
		return nil, ErrViewerSchemaNotFound
	}
	return tables, nil
}

// Nama schema dan tabel mengikuti batas bawaan PostgreSQL 63 byte tanpa mengubah kapitalisasi atau spasi identifier.
func ValidIdentifier(name string) bool {
	return name != "" && len(name) <= 63 && utf8.ValidString(name) &&
		strings.IndexFunc(name, unicode.IsControl) == -1
}

// Penyebab driver disimpan dalam pembungkus aman agar detail server tidak bocor lewat respons atau pencatatan error.
func viewerMetadataError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ErrViewerMetadata, ctx.Err())
	}
	return errors.Join(ErrViewerMetadata, database.WrapTargetError("membaca katalog target", err))
}
