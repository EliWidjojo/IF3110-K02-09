package viewer

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"IF3110-K02-09/internal/database"

	"github.com/lib/pq"
)

const (
	ViewerMaxResultBytes  = 2 * 1024 * 1024
	ViewerDefaultPageSize = 50
	ViewerMaxPageSize     = 100
)

var (
	ErrViewerResultTooLarge = errors.New("hasil melebihi batas 2 MiB; pilih ukuran halaman lebih kecil")
	ErrViewerTableNotFound  = errors.New("tabel viewer tidak ditemukan")
	ErrViewerTableData      = errors.New("pembacaan data tabel target gagal")
)

type ViewerPagination struct {
	Limit  int
	Offset int
}

type ViewerColumn struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	// ValueType membawa tipe dasar hasil query agar serializer dapat membedakan domain numerik, JSON, waktu, dan biner.
	ValueType string `json:"-"`
}

// Posisi nilai setiap baris mengikuti Columns agar nama kolom tidak perlu digandakan pada setiap baris.
type ViewerTableData struct {
	Columns     []ViewerColumn `json:"columns"`
	Rows        [][]any        `json:"rows"`
	Limit       int            `json:"limit"`
	Offset      int            `json:"offset"`
	HasMore     bool           `json:"has_more"`
	OrderBy     []string       `json:"order_by"`
	StableOrder bool           `json:"stable_order"`
}

// Kontrak terpisah mempertahankan kompatibilitas penyedia metadata yang hanya mendukung daftar schema dan tabel.
type ViewerTableDataStore interface {
	ReadTableData(context.Context, *sql.DB, string, string, ViewerPagination) (ViewerTableData, error)
}

func (r *ViewerMetadataRepository) ReadTableData(ctx context.Context, db *sql.DB, schema, table string, pagination ViewerPagination) (ViewerTableData, error) {
	if !ValidIdentifier(schema) || !ValidIdentifier(table) {
		return ViewerTableData{}, ErrInvalidViewerInput
	}
	pagination, err := normalizedViewerPagination(pagination)
	if err != nil {
		return ViewerTableData{}, err
	}
	if r == nil || db == nil {
		return ViewerTableData{}, ErrViewerDependencyUnavailable
	}
	// Snapshot per halaman menyatukan metadata dan data, sedangkan read-only mencegah penulisan melalui transaksi viewer.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	defer tx.Rollback()

	relationID, kind, err := viewerTableRelation(ctx, tx, schema, table)
	if err != nil {
		return ViewerTableData{}, err
	}
	columns, primaryKey, err := viewerTableColumns(ctx, tx, relationID)
	if err != nil {
		return ViewerTableData{}, err
	}
	projection := make([]string, len(columns))
	for index, column := range columns {
		projection[index] = pq.QuoteIdentifier(column.Name)
	}
	selectColumns := strings.Join(projection, ", ")
	if len(columns) == 0 {
		selectColumns = "*"
	}
	qualifiedTable := pq.QuoteIdentifier(schema) + "." + pq.QuoteIdentifier(table)
	// ONLY mengecualikan pewarisan tradisional karena primary key induk tidak menjamin keunikan pada tabel anak.
	if kind != "p" {
		qualifiedTable = "ONLY " + qualifiedTable
	}
	query := "SELECT " + selectColumns + " FROM " + qualifiedTable
	if len(primaryKey) > 0 {
		order := make([]string, len(primaryKey))
		for index, name := range primaryKey {
			order[index] = pq.QuoteIdentifier(name) + " ASC"
		}
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	// Identifier selalu di-quote secara terpisah; limit dan offset tetap menjadi parameter nilai.
	query += " LIMIT $1 OFFSET $2"
	rows, err := tx.QueryContext(ctx, query, pagination.Limit+1, pagination.Offset)
	if err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	defer rows.Close()
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	if len(columnTypes) != len(columns) {
		return ViewerTableData{}, ErrViewerTableData
	}
	for index, columnType := range columnTypes {
		columns[index].ValueType = columnType.DatabaseTypeName()
	}
	result := ViewerTableData{
		Columns: columns, Rows: make([][]any, 0, pagination.Limit),
		Limit: pagination.Limit, Offset: pagination.Offset,
		OrderBy: primaryKey, StableOrder: len(primaryKey) > 0,
	}
	resultBytes := 0
	for rows.Next() {
		// Satu baris tambahan hanya mendeteksi halaman berikutnya tanpa menghitung seluruh isi tabel.
		if len(result.Rows) == pagination.Limit {
			result.HasMore = true
			break
		}
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return ViewerTableData{}, viewerTableDataError(ctx, err)
		}
		for index, value := range values {
			switch v := value.(type) {
			case []byte:
				resultBytes += len(v)
			case string:
				resultBytes += len(v)
			default:
				resultBytes += 32
			}
			if resultBytes > ViewerMaxResultBytes {
				return ViewerTableData{}, ErrViewerResultTooLarge
			}
			values[index], err = viewerJSONValue(value, columnTypes[index].DatabaseTypeName())
			if err != nil {
				return ViewerTableData{}, err
			}
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return ViewerTableData{}, viewerTableDataError(ctx, err)
	}
	return result, nil
}

func viewerTableRelation(ctx context.Context, tx *sql.Tx, schema, table string) (int64, string, error) {
	const query = `
		SELECT c.oid, c.relkind::text
		FROM pg_catalog.pg_namespace AS n
		LEFT JOIN pg_catalog.pg_class AS c
			ON c.relnamespace = n.oid AND c.relname = $2 AND c.relkind IN ('r', 'p', 'f')
		WHERE n.nspname = $1
			AND left(n.nspname, 3) <> 'pg_' AND n.nspname NOT IN ('information_schema','platform_migrations');
	`
	var relationID sql.NullInt64
	var kind sql.NullString
	err := tx.QueryRowContext(ctx, query, schema, table).Scan(&relationID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrViewerSchemaNotFound
	}
	if err != nil {
		return 0, "", viewerMetadataError(ctx, err)
	}
	if !relationID.Valid || !kind.Valid {
		return 0, "", ErrViewerTableNotFound
	}
	return relationID.Int64, kind.String, nil
}

func viewerTableColumns(ctx context.Context, tx *sql.Tx, relationID int64) ([]ViewerColumn, []string, error) {
	// Ordinality mempertahankan urutan primary key gabungan dan indnkeyatts mengecualikan kolom INCLUDE.
	const query = `
		SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), pk.position
		FROM pg_catalog.pg_attribute AS a
		LEFT JOIN (
			SELECT k.attnum, k.position
			FROM pg_catalog.pg_index AS i
			CROSS JOIN LATERAL pg_catalog.unnest(i.indkey) WITH ORDINALITY AS k(attnum, position)
			WHERE i.indrelid = $1 AND i.indisprimary AND i.indisvalid AND i.indislive
				AND k.position <= i.indnkeyatts
		) AS pk ON pk.attnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum ASC;
	`
	rows, err := tx.QueryContext(ctx, query, relationID)
	if err != nil {
		return nil, nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	columns := make([]ViewerColumn, 0)
	type keyColumn struct {
		name     string
		position int64
	}
	keys := make([]keyColumn, 0)
	for rows.Next() {
		var column ViewerColumn
		var position sql.NullInt64
		if err := rows.Scan(&column.Name, &column.DataType, &position); err != nil {
			return nil, nil, viewerMetadataError(ctx, err)
		}
		columns = append(columns, column)
		if position.Valid {
			keys = append(keys, keyColumn{name: column.Name, position: position.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, viewerMetadataError(ctx, err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, viewerMetadataError(ctx, err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].position < keys[j].position })
	primaryKey := make([]string, 0, len(keys))
	for _, key := range keys {
		primaryKey = append(primaryKey, key.name)
	}
	return columns, primaryKey, nil
}

func normalizedViewerPagination(pagination ViewerPagination) (ViewerPagination, error) {
	if pagination.Limit < 0 || pagination.Limit > ViewerMaxPageSize || pagination.Offset < 0 {
		return ViewerPagination{}, ErrInvalidViewerInput
	}
	if pagination.Limit == 0 {
		pagination.Limit = ViewerDefaultPageSize
	}
	return pagination, nil
}

// Bytea disalin agar tidak memakai buffer driver, sedangkan tipe tekstual dan numerik presisi tinggi tetap berupa string.
func viewerJSONValue(value any, databaseType string) (any, error) {
	switch typed := value.(type) {
	case nil, bool, int64, string:
		return value, nil
	case []byte:
		if databaseType == "BYTEA" {
			return append([]byte{}, typed...), nil
		}
		return string(typed), nil
	case time.Time:
		return viewerTemporalValue(typed, databaseType), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return strconv.FormatFloat(typed, 'g', -1, 64), nil
		}
		return typed, nil
	default:
		return nil, ErrViewerTableData
	}
}

// Format waktu mempertahankan perbedaan tanggal, waktu tanpa zona, akhir hari 24:00, dan instant dengan zona.
func viewerTemporalValue(value time.Time, databaseType string) string {
	switch databaseType {
	case "DATE":
		return value.Format("2006-01-02")
	case "TIME", "TIMETZ":
		clock := value.Format("15:04:05.999999999")
		// Driver pq menandai nilai PostgreSQL 24:00 sebagai tanggal 2 Januari tahun nol.
		if value.Year() == 0 && value.Month() == time.January && value.Day() == 2 {
			clock = "24:00:00"
		}
		if databaseType == "TIMETZ" {
			clock += value.Format("-07:00:00")
		}
		return clock
	case "TIMESTAMP":
		return value.Format("2006-01-02T15:04:05.999999999")
	case "TIMESTAMPTZ":
		return value.UTC().Format(time.RFC3339Nano)
	default:
		return value.Format(time.RFC3339Nano)
	}
}

func viewerTableDataError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ErrViewerTableData, ctx.Err())
	}
	var databaseError *pq.Error
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "42P01":
			return ErrViewerTableNotFound
		case "3F000":
			return ErrViewerSchemaNotFound
		}
	}
	return errors.Join(ErrViewerTableData, database.WrapTargetError("membaca data tabel target", err))
}
