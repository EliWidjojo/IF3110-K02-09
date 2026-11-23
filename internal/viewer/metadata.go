package viewer

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"IF3110-K02-09/internal/database"

	"github.com/lib/pq"
)

// Model dan kontrak metadata tabel.

// Model metadata tidak bergantung pada bentuk respons HTTP sehingga dapat digunakan pemanggil service lain.
type ViewerTableMetadata struct {
	Table       ViewerTable
	Columns     []ViewerColumnMetadata
	Constraints []ViewerConstraintMetadata
	Indexes     []ViewerIndexMetadata
}

type ViewerColumnMetadata struct {
	ViewerColumn
	Position             int
	Nullable             bool
	Default              *string
	Identity             string
	Generated            string
	GenerationExpression *string
}

type ViewerConstraintMetadata struct {
	Name              string
	Type              string
	Definition        string
	Columns           []string
	Reference         *ViewerForeignKeyReference
	Deferrable        bool
	InitiallyDeferred bool
	Validated         bool
}

type ViewerForeignKeyReference struct {
	Schema  string
	Table   string
	Columns []string
}

type ViewerIndexMetadata struct {
	Name       string
	Definition string
	Method     string
	Unique     bool
	Primary    bool
	Valid      bool
	Ready      bool
	Predicate  *string
}

type ViewerTableMetadataStore interface {
	GetTableMetadata(context.Context, *sql.DB, string, string) (ViewerTableMetadata, error)
}

// Repository daftar schema dan tabel serta validasi identifier.

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

// Repository metadata kolom, constraint, dan index.

func (r *ViewerMetadataRepository) GetTableMetadata(ctx context.Context, db *sql.DB, schema, table string) (ViewerTableMetadata, error) {
	if !ValidIdentifier(schema) || !ValidIdentifier(table) {
		return ViewerTableMetadata{}, ErrInvalidViewerInput
	}
	if r == nil || db == nil {
		return ViewerTableMetadata{}, ErrViewerDependencyUnavailable
	}
	// Semua bagian detail dibaca dalam satu snapshot agar hasil tidak mencampur metadata transaksi berbeda.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ViewerTableMetadata{}, viewerMetadataError(ctx, err)
	}
	defer tx.Rollback()
	relationID, kind, err := viewerTableRelation(ctx, tx, schema, table)
	if err != nil {
		return ViewerTableMetadata{}, err
	}
	result := ViewerTableMetadata{Table: ViewerTable{Schema: schema, Name: table}}
	switch kind {
	case "r":
		result.Table.Type = "table"
	case "p":
		result.Table.Type = "partitioned_table"
	case "f":
		result.Table.Type = "foreign_table"
	default:
		return ViewerTableMetadata{}, ErrViewerTableNotFound
	}
	result.Columns, err = viewerColumnMetadata(ctx, tx, relationID)
	if err != nil {
		return ViewerTableMetadata{}, err
	}
	result.Constraints, err = viewerConstraintMetadata(ctx, tx, relationID)
	if err != nil {
		return ViewerTableMetadata{}, err
	}
	result.Indexes, err = viewerIndexMetadata(ctx, tx, relationID)
	if err != nil {
		return ViewerTableMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return ViewerTableMetadata{}, viewerMetadataError(ctx, err)
	}
	return result, nil
}

func viewerColumnMetadata(ctx context.Context, tx *sql.Tx, relationID int64) ([]ViewerColumnMetadata, error) {
	// Rantai domain diperiksa untuk nullable, sedangkan ekspresi generated dipisahkan dari default biasa.
	const query = `
		SELECT a.attname, a.attnum, pg_catalog.format_type(a.atttypid, a.atttypmod),
			NOT a.attnotnull AND NOT EXISTS (
				WITH RECURSIVE domain_types AS (
					SELECT t.oid, t.typbasetype, t.typnotnull FROM pg_catalog.pg_type AS t WHERE t.oid = a.atttypid
					UNION ALL
					SELECT t.oid, t.typbasetype, t.typnotnull FROM pg_catalog.pg_type AS t
					JOIN domain_types AS base ON t.oid = base.typbasetype
				)
				SELECT 1 FROM domain_types WHERE typnotnull
			),
			CASE WHEN a.attgenerated = '' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) END,
			a.attidentity::text, a.attgenerated::text,
			CASE WHEN a.attgenerated <> '' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) END
		FROM pg_catalog.pg_attribute AS a
		LEFT JOIN pg_catalog.pg_attrdef AS d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum ASC;
	`
	rows, err := tx.QueryContext(ctx, query, relationID)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	columns := make([]ViewerColumnMetadata, 0)
	for rows.Next() {
		var column ViewerColumnMetadata
		var defaultValue, generationExpression sql.NullString
		var identity, generated string
		if err := rows.Scan(&column.Name, &column.Position, &column.DataType, &column.Nullable,
			&defaultValue, &identity, &generated, &generationExpression); err != nil {
			return nil, viewerMetadataError(ctx, err)
		}
		column.Default = viewerNullableString(defaultValue)
		column.GenerationExpression = viewerNullableString(generationExpression)
		switch identity {
		case "a":
			column.Identity = "always"
		case "d":
			column.Identity = "by_default"
		}
		switch generated {
		case "s":
			column.Generated = "stored"
		case "v":
			column.Generated = "virtual"
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	return columns, nil
}

func viewerConstraintMetadata(ctx context.Context, tx *sql.Tx, relationID int64) ([]ViewerConstraintMetadata, error) {
	// Ordinality mempertahankan pasangan kolom foreign key gabungan sesuai urutan referensinya.
	const query = `
		SELECT c.conname, c.contype::text, pg_catalog.pg_get_constraintdef(c.oid, false),
			ARRAY(SELECT a.attname::text
				FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY AS k(attnum, key_position)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
				ORDER BY k.key_position),
			n.nspname, referenced.relname,
			ARRAY(SELECT a.attname::text
				FROM pg_catalog.unnest(c.confkey) WITH ORDINALITY AS k(attnum, key_position)
				JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.confrelid AND a.attnum = k.attnum
				ORDER BY k.key_position),
			c.condeferrable, c.condeferred, c.convalidated
		FROM pg_catalog.pg_constraint AS c
		LEFT JOIN pg_catalog.pg_class AS referenced ON referenced.oid = c.confrelid
		LEFT JOIN pg_catalog.pg_namespace AS n ON n.oid = referenced.relnamespace
		WHERE c.conrelid = $1 AND c.contype IN ('p', 'f', 'u', 'c')
		ORDER BY c.conname COLLATE "C" ASC, c.oid ASC;
	`
	rows, err := tx.QueryContext(ctx, query, relationID)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	constraints := make([]ViewerConstraintMetadata, 0)
	for rows.Next() {
		var constraint ViewerConstraintMetadata
		var kind string
		var columns, referencedColumns pq.StringArray
		var referencedSchema, referencedTable sql.NullString
		if err := rows.Scan(&constraint.Name, &kind, &constraint.Definition, &columns,
			&referencedSchema, &referencedTable, &referencedColumns,
			&constraint.Deferrable, &constraint.InitiallyDeferred, &constraint.Validated); err != nil {
			return nil, viewerMetadataError(ctx, err)
		}
		constraint.Columns = []string(columns)
		switch kind {
		case "p":
			constraint.Type = "primary_key"
		case "u":
			constraint.Type = "unique"
		case "c":
			constraint.Type = "check"
		case "f":
			constraint.Type = "foreign_key"
			if !referencedSchema.Valid || !referencedTable.Valid || len(columns) != len(referencedColumns) {
				return nil, ErrViewerMetadata
			}
			constraint.Reference = &ViewerForeignKeyReference{
				Schema: referencedSchema.String, Table: referencedTable.String, Columns: []string(referencedColumns),
			}
		default:
			return nil, ErrViewerMetadata
		}
		constraints = append(constraints, constraint)
	}
	if err := rows.Err(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	return constraints, nil
}

func viewerIndexMetadata(ctx context.Context, tx *sql.Tx, relationID int64) ([]ViewerIndexMetadata, error) {
	// Definisi PostgreSQL mencakup ekspresi, kolom INCLUDE, dan predicate tanpa menyusun ulang SQL dari client.
	const query = `
		SELECT c.relname, pg_catalog.pg_get_indexdef(i.indexrelid), am.amname,
			i.indisunique, i.indisprimary, i.indisvalid, i.indisready,
			pg_catalog.pg_get_expr(i.indpred, i.indrelid)
		FROM pg_catalog.pg_index AS i
		JOIN pg_catalog.pg_class AS c ON c.oid = i.indexrelid
		JOIN pg_catalog.pg_am AS am ON am.oid = c.relam
		WHERE i.indrelid = $1 AND i.indislive
		ORDER BY c.relname COLLATE "C" ASC, i.indexrelid ASC;
	`
	rows, err := tx.QueryContext(ctx, query, relationID)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	indexes := make([]ViewerIndexMetadata, 0)
	for rows.Next() {
		var index ViewerIndexMetadata
		var predicate sql.NullString
		if err := rows.Scan(&index.Name, &index.Definition, &index.Method,
			&index.Unique, &index.Primary, &index.Valid, &index.Ready, &predicate); err != nil {
			return nil, viewerMetadataError(ctx, err)
		}
		index.Predicate = viewerNullableString(predicate)
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	return indexes, nil
}

func viewerNullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
