package viewer

import (
	"IF3110-K02-09/internal/database"
	"context"
	"database/sql"
	"strings"
)

type mysqlViewer struct{ schema string }

func (m mysqlViewer) ListSchemas(ctx context.Context, db *sql.DB) ([]ViewerSchema, error) {
	var name string
	if err := db.QueryRowContext(ctx, `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=DATABASE()`).Scan(&name); err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	return []ViewerSchema{{name}}, nil
}
func (m mysqlViewer) ListTables(ctx context.Context, db *sql.DB, schema string) ([]ViewerTable, error) {
	if schema != m.schema {
		return nil, ErrViewerSchemaNotFound
	}
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME`)
	if err != nil {
		return nil, viewerMetadataError(ctx, err)
	}
	defer rows.Close()
	out := []ViewerTable{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return nil, ErrViewerMetadata
		}
		out = append(out, ViewerTable{schema, name, "table"})
	}
	return out, rows.Err()
}
func (m mysqlViewer) GetTableMetadata(ctx context.Context, db *sql.DB, schema, table string) (ViewerTableMetadata, error) {
	out := ViewerTableMetadata{Table: ViewerTable{schema, table, "table"}, Columns: []ViewerColumnMetadata{}, Constraints: []ViewerConstraintMetadata{}, Indexes: []ViewerIndexMetadata{}}
	if schema != m.schema {
		return out, ErrViewerSchemaNotFound
	}
	if !ValidIdentifier(table) {
		return out, ErrInvalidViewerInput
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, viewerMetadataError(ctx, err)
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND TABLE_TYPE='BASE TABLE'`, table).Scan(&count) != nil {
		return out, ErrViewerMetadata
	}
	if count != 1 {
		return out, ErrViewerTableNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT COLUMN_NAME,ORDINAL_POSITION,COLUMN_TYPE,IS_NULLABLE='YES',COLUMN_DEFAULT,EXTRA,GENERATION_EXPRESSION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`, table)
	if err != nil {
		return out, viewerMetadataError(ctx, err)
	}
	for rows.Next() {
		var c ViewerColumnMetadata
		var def, generated sql.NullString
		var extra string
		if rows.Scan(&c.Name, &c.Position, &c.DataType, &c.Nullable, &def, &extra, &generated) != nil {
			rows.Close()
			return out, ErrViewerMetadata
		}
		c.Default = viewerNullableString(def)
		if strings.Contains(extra, "auto_increment") {
			c.Identity = "auto_increment"
		}
		if generated.Valid && generated.String != "" {
			c.Generated = extra
			c.GenerationExpression = &generated.String
		}
		out.Columns = append(out.Columns, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, viewerMetadataError(ctx, err)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT tc.CONSTRAINT_NAME,tc.CONSTRAINT_TYPE,kcu.COLUMN_NAME,kcu.REFERENCED_TABLE_SCHEMA,kcu.REFERENCED_TABLE_NAME,kcu.REFERENCED_COLUMN_NAME FROM information_schema.TABLE_CONSTRAINTS tc LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu ON kcu.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA AND kcu.TABLE_NAME=tc.TABLE_NAME AND kcu.CONSTRAINT_NAME=tc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.TABLE_NAME=? ORDER BY tc.CONSTRAINT_NAME,kcu.ORDINAL_POSITION`, table)
	if err != nil {
		return out, viewerMetadataError(ctx, err)
	}
	for rows.Next() {
		var name, kind string
		var column, rs, rt, rc sql.NullString
		if rows.Scan(&name, &kind, &column, &rs, &rt, &rc) != nil {
			rows.Close()
			return out, ErrViewerMetadata
		}
		index := len(out.Constraints) - 1
		if index < 0 || out.Constraints[index].Name != name {
			out.Constraints = append(out.Constraints, ViewerConstraintMetadata{Name: name, Type: strings.ToLower(strings.ReplaceAll(kind, " ", "_")), Definition: kind, Columns: []string{}, Validated: true})
			index++
		}
		c := &out.Constraints[index]
		if column.Valid {
			c.Columns = append(c.Columns, column.String)
		}
		if rt.Valid {
			if c.Reference == nil {
				c.Reference = &ViewerForeignKeyReference{Schema: rs.String, Table: rt.String, Columns: []string{}}
			}
			c.Reference.Columns = append(c.Reference.Columns, rc.String)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, viewerMetadataError(ctx, err)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT INDEX_NAME,INDEX_TYPE,NON_UNIQUE=0,COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX`, table)
	if err != nil {
		return out, viewerMetadataError(ctx, err)
	}
	for rows.Next() {
		var name, method string
		var unique bool
		var column sql.NullString
		if rows.Scan(&name, &method, &unique, &column) != nil {
			rows.Close()
			return out, ErrViewerMetadata
		}
		i := len(out.Indexes) - 1
		if i < 0 || out.Indexes[i].Name != name {
			out.Indexes = append(out.Indexes, ViewerIndexMetadata{Name: name, Method: method, Unique: unique, Primary: name == "PRIMARY", Valid: true, Ready: true})
			i++
		}
		if out.Indexes[i].Definition != "" {
			out.Indexes[i].Definition += ", "
		}
		out.Indexes[i].Definition += column.String
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, viewerMetadataError(ctx, err)
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return out, viewerMetadataError(ctx, err)
	}
	return out, nil
}
func (m mysqlViewer) ReadTableData(ctx context.Context, db *sql.DB, schema, table string, pagination ViewerPagination) (ViewerTableData, error) {
	out := ViewerTableData{Rows: [][]any{}, Columns: []ViewerColumn{}, Limit: pagination.Limit, Offset: pagination.Offset, OrderBy: []string{}}
	metadata, err := m.GetTableMetadata(ctx, db, schema, table)
	if err != nil {
		return out, err
	}
	projection := []string{}
	for _, column := range metadata.Columns {
		typ := strings.ToUpper(strings.Split(column.DataType, "(")[0])
		typ = strings.Fields(typ)[0]
		valueType := "TEXT"
		binary := false
		switch typ {
		case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "BIT":
			valueType = "BYTEA"
			binary = true
		case "SMALLINT", "INT", "INTEGER", "BIGINT", "TINYINT", "MEDIUMINT":
			valueType = "BIGINT"
		case "DECIMAL", "NUMERIC":
			valueType = "NUMERIC"
		case "FLOAT", "DOUBLE", "REAL":
			valueType = "FLOAT8"
		case "DATE", "TIME", "DATETIME", "TIMESTAMP", "YEAR":
			valueType = "TIMESTAMP"
		case "JSON":
			valueType = "JSON"
		}
		name := database.MariaDB.Quote(column.Name)
		if !binary {
			name = "CAST(" + name + " AS CHAR)"
		}
		projection = append(projection, name)
		out.Columns = append(out.Columns, ViewerColumn{Name: column.Name, DataType: column.DataType, ValueType: valueType})
	}
	for _, c := range metadata.Constraints {
		if c.Type == "primary_key" {
			out.OrderBy = c.Columns
			out.StableOrder = true
		}
	}
	if len(projection) == 0 {
		projection = []string{"*"}
	}
	q := "SELECT " + strings.Join(projection, ",") + " FROM " + database.MariaDB.Quote(schema) + "." + database.MariaDB.Quote(table)
	if out.StableOrder {
		names := []string{}
		for _, c := range out.OrderBy {
			names = append(names, database.MariaDB.Quote(c))
		}
		q += " ORDER BY " + strings.Join(names, ",")
	}
	q += " LIMIT ? OFFSET ?"
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, viewerTableDataError(ctx, err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, q, pagination.Limit+1, pagination.Offset)
	if err != nil {
		return out, viewerTableDataError(ctx, err)
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		if len(out.Rows) == pagination.Limit {
			out.HasMore = true
			break
		}
		values := make([]any, len(out.Columns))
		dest := make([]any, len(values))
		for i := range dest {
			dest[i] = &values[i]
		}
		if rows.Scan(dest...) != nil {
			return out, ErrViewerTableData
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				size += len(b)
				if out.Columns[i].ValueType == "BYTEA" {
					values[i] = append([]byte{}, b...)
				} else {
					values[i] = string(b)
				}
			}
		}
		if size > ViewerMaxResultBytes {
			return out, ErrViewerResultTooLarge
		}
		out.Rows = append(out.Rows, values)
	}
	if err = rows.Err(); err != nil {
		return out, viewerTableDataError(ctx, err)
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return out, viewerTableDataError(ctx, err)
	}
	return out, nil
}
