package migration

import (
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/viewer"
	"context"
	"database/sql"
	"regexp"
	"sort"
 "strconv"
	"strings"
	"time"
)

type DesiredColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}
type DesiredTable struct {
	Name    string          `json:"name"`
	Columns []DesiredColumn `json:"columns"`
}
type DesiredSchema struct {
	Schema string         `json:"schema"`
	Tables []DesiredTable `json:"tables"`
}
type Difference struct {
	Object           string `json:"object"`
	Kind             string `json:"kind"`
	Message          string `json:"message"`
	RequiresDecision bool   `json:"requires_decision"`
}
type DiffResult struct {
	Differences []Difference `json:"differences"`
	SQL         string       `json:"sql"`
	Limitations string       `json:"limitations"`
}
type actualColumn struct {
	Type     string
	Nullable bool
}

var sizedType = regexp.MustCompile(`^(?:varchar|character varying)\(([1-9][0-9]{0,5})\)$`)
var numericType = regexp.MustCompile(`^numeric\(([1-9][0-9]{0,3})(?:,([0-9]{1,3}))?\)$`)

func normalizeType(input string, d database.Dialect) (string, bool) {
	t := strings.Join(strings.Fields(strings.ToLower(input)), " ")
	aliases := map[string]string{"int": "integer", "int4": "integer", "int8": "bigint", "int2": "smallint", "bool": "boolean", "varchar": "character varying", "timestamptz": "timestamp with time zone", "timestamp": "timestamp without time zone", "decimal": "numeric"}
	if d.IsMySQL(){if t=="tinyint(1)"{t="boolean"};if strings.HasPrefix(t,"int(")&&strings.HasSuffix(t,")"){t="integer"};if strings.HasPrefix(t,"bigint(")&&strings.HasSuffix(t,")"){t="bigint"};if strings.HasPrefix(t,"smallint(")&&strings.HasSuffix(t,")"){t="smallint"}}
 if a, ok := aliases[t]; ok {
		t = a
	}
	if sizedType.MatchString(t) {
		t = strings.Replace(t, "varchar(", "character varying(", 1)
		if d.IsMySQL() {
			t = strings.Replace(t, "character varying(", "varchar(", 1)
		}
		return t, true
	}
	t=strings.Replace(t,"decimal(","numeric(",1)
 if match:=numericType.FindStringSubmatch(t);match!=nil{
  precision,_:=strconv.Atoi(match[1]);scale:=0;if match[2]!=""{scale,_=strconv.Atoi(match[2])};maxPrecision:=1000;maxScale:=1000;if d.IsMySQL(){maxPrecision=65;maxScale=30}
  if precision>maxPrecision || scale>precision || scale>maxScale{return "",false};return t,true
 }
	allowed := map[string]bool{"integer": true, "bigint": true, "smallint": true, "text": true, "boolean": true, "numeric": true, "date": true, "time without time zone": true, "timestamp without time zone": true, "timestamp with time zone": true, "uuid": true, "json": true, "jsonb": true, "bytea": true, "real": true, "double precision": true, "character varying": true}
	if !allowed[t] {
		return "", false
	}
	if d.IsMySQL() {
		switch t {
		case "bytea":
			t = "blob"
		case "jsonb":
			t = "json"
		case "timestamp with time zone", "timestamp without time zone":
			t = "datetime(6)"
		case "time without time zone":
			t = "time(6)"
		case "uuid":
			t = "char(36)"
		case "character varying":
			return "", false
		}
	}
	return t, true
}
func (e *Engine) Diff(ctx context.Context, id int, desired DesiredSchema) (DiffResult, error) {
	if err := e.access.RequireEdit(ctx, id); err != nil {
		return DiffResult{}, err
	}
	if !viewer.ValidIdentifier(desired.Schema) || desired.Schema == "platform_migrations" || strings.HasPrefix(desired.Schema, "pg_") || desired.Schema == "information_schema" || len(desired.Tables) > 50 {
		return DiffResult{}, &InputError{"schema atau jumlah table tidak valid"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, err := e.pools.Configuration(ctx, id)
	if err != nil {
		return DiffResult{}, err
	}
	if cfg.Driver.IsMySQL() && desired.Schema != cfg.DatabaseName {
		return DiffResult{}, &InputError{"schema harus sama dengan database target"}
	}
	db, err := e.pools.Get(ctx, id)
	if err != nil {
		return DiffResult{}, err
	}
	if cfg.Driver==database.Postgres{var exists bool;if db.QueryRowContext(ctx,`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`,desired.Schema).Scan(&exists)!=nil{return DiffResult{},viewer.ErrViewerMetadata};if !exists{return DiffResult{},&InputError{"schema tujuan belum ada; buat schema melalui editor dahulu"}}}
 current, err := inspectSchema(ctx, db, cfg.Driver, desired.Schema)
	if err != nil {
		return DiffResult{}, database.WrapTargetError("membaca schema diff", err)
	}
	return GenerateDiff(cfg.Driver, desired, current)
}
func inspectSchema(ctx context.Context, db *sql.DB, d database.Dialect, schema string) (map[string]map[string]actualColumn, error) {
	current := map[string]map[string]actualColumn{}
	q := `SELECT c.relname,a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),NOT a.attnotnull FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname,a.attnum`
	if d.IsMySQL() {
		q = `SELECT t.table_name,c.column_name,c.column_type,c.is_nullable='YES' FROM information_schema.tables t LEFT JOIN information_schema.columns c ON c.table_schema=t.table_schema AND c.table_name=t.table_name WHERE t.table_schema=? AND t.table_type='BASE TABLE' ORDER BY t.table_name,c.ordinal_position`
	}
	rows, err := db.QueryContext(ctx, q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var column, typ sql.NullString
		var nullable sql.NullBool
		if err = rows.Scan(&table, &column, &typ, &nullable); err != nil {
			return nil, err
		}
		if current[table] == nil {
			current[table] = map[string]actualColumn{}
		}
		if column.Valid {
			current[table][column.String] = actualColumn{typ.String, nullable.Bool}
		}
	}
	return current, rows.Err()
}

// Kandidat hanya menambahkan objek; penghapusan, rename, dan perubahan berisiko selalu meminta keputusan pengguna.
func GenerateDiff(d database.Dialect, desired DesiredSchema, current map[string]map[string]actualColumn) (DiffResult, error) {
	result := DiffResult{Differences: []Difference{}, Limitations: "Membandingkan table biasa, nama/type/nullability column. Constraint, index, default, identity, enum, view, rename, dan penghapusan otomatis tidak didukung."}
	seen := map[string]bool{}
	sqlLines := []string{}
	tables := append([]DesiredTable{}, desired.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	for _, table := range tables {
		if !viewer.ValidIdentifier(table.Name) || seen[table.Name] || len(table.Columns) == 0 || len(table.Columns) > 100 {
			return result, &InputError{"table atau column duplikat/tidak valid"}
		}
		seen[table.Name] = true
		cols := map[string]bool{}
		definitions := []string{}
		existing, exists := current[table.Name]
		for _, column := range table.Columns {
			typ, ok := normalizeType(column.Type, d)
			if !viewer.ValidIdentifier(column.Name) || cols[column.Name] || !ok {
				return result, &InputError{"nama column duplikat atau tipe di luar cakupan"}
			}
			cols[column.Name] = true
			definition := d.Quote(column.Name) + " " + typ
			if !column.Nullable {
				definition += " NOT NULL"
			}
			definitions = append(definitions, definition)
			if !exists {
				continue
			}
			old, present := existing[column.Name]
			object := table.Name + "." + column.Name
			if !present {
				if !column.Nullable {
					result.Differences = append(result.Differences, Difference{object, "add_column", "Column NOT NULL memerlukan keputusan pengisian nilai untuk data yang sudah ada; tulis SQL setelah ditinjau.", true})
					continue
				}
				result.Differences = append(result.Differences, Difference{object, "add_column", "Tambahkan column nullable.", false})
				sqlLines = append(sqlLines, "ALTER TABLE "+d.Quote(desired.Schema)+"."+d.Quote(table.Name)+" ADD COLUMN "+definition+";")
			} else {
				oldType, recognized := normalizeType(old.Type, d)
 if !recognized{oldType=strings.ToLower(strings.Join(strings.Fields(old.Type)," "))}
				if oldType != typ {
					result.Differences = append(result.Differences, Difference{object, "change_type", "Tipe saat ini " + old.Type + ", tujuan " + typ + "; keputusan konversi diperlukan.", true})
				}
				if old.Nullable != column.Nullable {
					result.Differences = append(result.Differences, Difference{object, "change_nullability", "Perubahan nullability memerlukan tinjauan data dan SQL manual.", true})
				}
			}
		}
		if !exists {
			result.Differences = append(result.Differences, Difference{table.Name, "create_table", "Buat table baru.", false})
			suffix := ""
			if d.IsMySQL() {
				suffix = " ENGINE=InnoDB"
			}
			sqlLines = append(sqlLines, "CREATE TABLE "+d.Quote(desired.Schema)+"."+d.Quote(table.Name)+" (\n  "+strings.Join(definitions, ",\n  ")+"\n)"+suffix+";")
		} else {
			names := []string{}
			for name := range existing {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !cols[name] {
					result.Differences = append(result.Differences, Difference{table.Name + "." + name, "column_absent", "Column tidak ada pada tujuan; penghapusan atau rename tidak ditebak.", true})
				}
			}
		}
	}
	names := []string{}
	for name := range current {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			result.Differences = append(result.Differences, Difference{name, "table_absent", "Table tidak ada pada tujuan; penghapusan atau rename tidak ditebak.", true})
		}
	}
	result.SQL = strings.Join(sqlLines, "\n\n")
	return result, nil
}
