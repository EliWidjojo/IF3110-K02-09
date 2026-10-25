package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

type Dialect string

const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	MariaDB  Dialect = "mariadb"
)

func (d Dialect) IsMySQL() bool { return d == MySQL || d == MariaDB }
func (d Dialect) Valid() bool   { return d == Postgres || d.IsMySQL() }
func (d Dialect) Bind(n int) string {
	if d.IsMySQL() {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}
func (d Dialect) Quote(name string) string {
	if d.IsMySQL() {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type Config struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Driver Dialect `json:"driver"`
	DSN    string  `json:"dsn"`
}
type Target struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Dialect Dialect `json:"driver"`
	DB      *sql.DB `json:"-"`
}

func Open(ctx context.Context, c Config) (*Target, error) {
	if c.ID == "" || !c.Driver.Valid() || c.DSN == "" {
		return nil, errors.New("invalid target configuration")
	}
	driver, dsn := "postgres", c.DSN
	if c.Driver.IsMySQL() {
		driver = "mysql"
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			return nil, errors.New("invalid MySQL DSN")
		}
		if cfg.DBName == "" {
			return nil, errors.New("MySQL DSN requires a database")
		}
		cfg.ParseTime = true
		cfg.MultiStatements = false
		cfg.AllowAllFiles = false
		cfg.AllowCleartextPasswords = false
		cfg.Timeout = 5 * time.Second
		cfg.ReadTimeout = 15 * time.Second
		cfg.WriteTimeout = 15 * time.Second
		dsn = cfg.FormatDSN()
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, errors.New("cannot open target database")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("target database connection failed")
	}
	return &Target{ID: c.ID, Name: c.Name, Dialect: c.Driver, DB: db}, nil
}

type Result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

func readRows(rows *sql.Rows) (Result, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	result := Result{Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return Result{}, err
		}
		for i, value := range values {
			if b, ok := value.([]byte); ok {
				values[i] = string(b)
			}
		}
		result.Rows = append(result.Rows, values)
	}
	return result, rows.Err()
}
func (t *Target) Metadata(ctx context.Context) (map[string]Result, error) {
	filter := "table_schema NOT IN ('pg_catalog','information_schema')"
	schemaQuery := "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('pg_catalog','information_schema')"
	indexQuery := "SELECT schemaname, tablename, indexname, indexdef FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog','information_schema')"
	if t.Dialect.IsMySQL() {
		filter = "table_schema = DATABASE()"
		schemaQuery = "SELECT schema_name FROM information_schema.schemata WHERE schema_name = DATABASE()"
		indexQuery = "SELECT table_schema, table_name, index_name, column_name, non_unique FROM information_schema.statistics WHERE table_schema = DATABASE() ORDER BY table_name, index_name, seq_in_index"
	}
	queries := map[string]string{
		"schemas":     schemaQuery,
		"tables":      "SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE " + filter,
		"columns":     "SELECT table_schema, table_name, column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE " + filter + " ORDER BY table_schema, table_name, ordinal_position",
		"constraints": "SELECT table_schema, table_name, constraint_name, constraint_type FROM information_schema.table_constraints WHERE " + filter,
		"indexes":     indexQuery,
	}
	result := map[string]Result{}
	for key, query := range queries {
		rows, err := t.DB.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		data, err := readRows(rows)
		if err != nil {
			return nil, err
		}
		result[key] = data
	}
	return result, nil
}
func (t *Target) Data(ctx context.Context, schema, table string, limit, offset int) (Result, error) {
	if schema == "" || table == "" || strings.ContainsRune(schema+table, 0) || limit < 1 || limit > 100 || offset < 0 {
		return Result{}, errors.New("invalid data request")
	}
	tx, err := t.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = " + t.Dialect.Bind(1) + " AND table_name = " + t.Dialect.Bind(2)
	if t.Dialect.IsMySQL() {
		query += " AND table_schema = DATABASE()"
	} else {
		query += " AND table_schema NOT IN ('pg_catalog','information_schema')"
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, schema, table).Scan(&count); err != nil {
		return Result{}, err
	}
	if count == 0 {
		return Result{}, errors.New("table not found in target")
	}
	query = "SELECT * FROM " + t.Dialect.Quote(schema) + "." + t.Dialect.Quote(table) + " LIMIT " + t.Dialect.Bind(1) + " OFFSET " + t.Dialect.Bind(2)
	rows, err := tx.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return Result{}, err
	}
	result, err := readRows(rows)
	if err != nil {
		return Result{}, err
	}
	return result, tx.Commit()
}
