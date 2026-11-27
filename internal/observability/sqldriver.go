package observability

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// DB menjelaskan koneksi yang dibungkus. Role "target" adalah database yang dikelola pengguna;
// role "platform" adalah database internal. Query platform yang berhasil dicatat pada level debug
// agar polling worker dan pembacaan identitas tidak menenggelamkan log eksekusi SQL target.
type DB struct {
	System string // postgresql atau mysql
	Role   string // target, target_audit, atau platform
}

const maxQueryLog = 4096

// WrapConnector membungkus connector database/sql sehingga setiap query, exec, begin, commit,
// dan rollback menghasilkan span db.* dan log sql.* dengan durasi, jumlah row, dan SQLSTATE.
// Interface opsional driver asli diteruskan apa adanya (fallback driver.ErrSkip) agar perilaku
// driver seperti simple query protocol lib/pq untuk SQL multi-statement tidak berubah.
func WrapConnector(c driver.Connector, info DB) driver.Connector {
	return &connector{next: c, info: info}
}

type connector struct {
	next driver.Connector
	info DB
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.next.Connect(ctx)
	if err != nil {
		slog.WarnContext(ctx, "sql.connect", "db_system", c.info.System, "db_role", c.info.Role, "status", "error", "error", errText(err))
		return nil, err
	}
	return &conn{next: raw, info: c.info}, nil
}
func (c *connector) Driver() driver.Driver { return c.next.Driver() }

type conn struct {
	next driver.Conn
	info DB
}

var (
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var prepared driver.Stmt
	var err error
	if p, ok := c.next.(driver.ConnPrepareContext); ok {
		prepared, err = p.PrepareContext(ctx, query)
	} else {
		prepared, err = c.next.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &stmt{next: prepared, query: query, info: c.info}, nil
}
func (c *conn) Close() error { return c.next.Close() }
func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	op := c.start(ctx, "db.transaction", "BEGIN", nil)
	var inner driver.Tx
	var err error
	if b, ok := c.next.(driver.ConnBeginTx); ok {
		inner, err = b.BeginTx(op.ctx, opts)
	} else {
		inner, err = c.next.Begin() //nolint:staticcheck // fallback untuk driver lama
	}
	if err != nil {
		op.finish("sql.begin", err, -1)
		return nil, err
	}
	op.log(slog.LevelInfo, "sql.begin", "success")
	return &tx{next: inner, op: op}, nil
}
func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.next.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	op := c.start(ctx, "db.exec", query, args)
	res, err := e.ExecContext(op.ctx, query, args)
	if errors.Is(err, driver.ErrSkip) {
		op.span.End()
		return nil, err
	}
	rows := int64(-1)
	if err == nil && res != nil {
		if n, e := res.RowsAffected(); e == nil {
			rows = n
		}
	}
	op.finish("sql.finish", err, rows)
	return res, err
}
func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.next.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	op := c.start(ctx, "db.query", query, args)
	rows, err := q.QueryContext(op.ctx, query, args)
	if errors.Is(err, driver.ErrSkip) {
		op.span.End()
		return nil, err
	}
	if err != nil {
		op.finish("sql.finish", err, -1)
		return nil, err
	}
	return &rowsWrap{next: rows, op: op}, nil
}
func (c *conn) Ping(ctx context.Context) error {
	if p, ok := c.next.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}
func (c *conn) ResetSession(ctx context.Context) error {
	if r, ok := c.next.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
func (c *conn) IsValid() bool {
	if v, ok := c.next.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
func (c *conn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.next.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// ---------- statement ----------

type stmt struct {
	next  driver.Stmt
	query string
	info  DB
}

func (s *stmt) Close() error  { return s.next.Close() }
func (s *stmt) NumInput() int { return s.next.NumInput() }
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	op := (&conn{info: s.info}).start(ctx, "db.exec", s.query, args)
	var res driver.Result
	var err error
	if e, ok := s.next.(driver.StmtExecContext); ok {
		res, err = e.ExecContext(op.ctx, args)
	} else {
		res, err = s.next.Exec(values(args)) //nolint:staticcheck // fallback untuk driver lama
	}
	rows := int64(-1)
	if err == nil && res != nil {
		if n, e := res.RowsAffected(); e == nil {
			rows = n
		}
	}
	op.finish("sql.finish", err, rows)
	return res, err
}
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	op := (&conn{info: s.info}).start(ctx, "db.query", s.query, args)
	var rows driver.Rows
	var err error
	if q, ok := s.next.(driver.StmtQueryContext); ok {
		rows, err = q.QueryContext(op.ctx, args)
	} else {
		rows, err = s.next.Query(values(args)) //nolint:staticcheck // fallback untuk driver lama
	}
	if err != nil {
		op.finish("sql.finish", err, -1)
		return nil, err
	}
	return &rowsWrap{next: rows, op: op}, nil
}
func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := s.next.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}
func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

// ---------- transaction ----------

type tx struct {
	next driver.Tx
	op   *operation
}

func (t *tx) Commit() error {
	err := t.next.Commit()
	t.op.finish("sql.commit", err, -1)
	return err
}
func (t *tx) Rollback() error {
	err := t.next.Rollback()
	t.op.span.SetAttributes(attribute.Bool("db.rollback", true))
	t.op.finish("sql.rollback", err, -1)
	return err
}

// ---------- rows ----------

// rowsWrap menghitung row yang dibaca dan meneruskan metadata kolom yang dipakai viewer.
type rowsWrap struct {
	next   driver.Rows
	op     *operation
	count  int64
	closed bool
	err    error
}

func (r *rowsWrap) Columns() []string { return r.next.Columns() }
func (r *rowsWrap) Close() error {
	err := r.next.Close()
	if !r.closed {
		r.closed = true
		final := r.err
		if final == nil {
			final = err
		}
		r.op.finish("sql.finish", final, r.count)
	}
	return err
}
func (r *rowsWrap) Next(dest []driver.Value) error {
	err := r.next.Next(dest)
	if err == nil {
		r.count++
	} else if err != io.EOF {
		r.err = err
	}
	return err
}
func (r *rowsWrap) HasNextResultSet() bool {
	if n, ok := r.next.(driver.RowsNextResultSet); ok {
		return n.HasNextResultSet()
	}
	return false
}
func (r *rowsWrap) NextResultSet() error {
	if n, ok := r.next.(driver.RowsNextResultSet); ok {
		return n.NextResultSet()
	}
	return io.EOF
}
func (r *rowsWrap) ColumnTypeDatabaseTypeName(i int) string {
	if c, ok := r.next.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return c.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}
func (r *rowsWrap) ColumnTypeScanType(i int) reflect.Type {
	if c, ok := r.next.(driver.RowsColumnTypeScanType); ok {
		return c.ColumnTypeScanType(i)
	}
	return reflect.TypeFor[any]()
}
func (r *rowsWrap) ColumnTypeNullable(i int) (bool, bool) {
	if c, ok := r.next.(driver.RowsColumnTypeNullable); ok {
		return c.ColumnTypeNullable(i)
	}
	return false, false
}
func (r *rowsWrap) ColumnTypeLength(i int) (int64, bool) {
	if c, ok := r.next.(driver.RowsColumnTypeLength); ok {
		return c.ColumnTypeLength(i)
	}
	return 0, false
}
func (r *rowsWrap) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	if c, ok := r.next.(driver.RowsColumnTypePrecisionScale); ok {
		return c.ColumnTypePrecisionScale(i)
	}
	return 0, 0, false
}

// ---------- satu operasi SQL ----------

type operation struct {
	ctx     context.Context
	span    trace.Span
	info    DB
	started time.Time
	base    []any
}

func (c *conn) start(ctx context.Context, spanName, query string, args []driver.NamedValue) *operation {
	ctx, span := otel.Tracer(tracerName).Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))
	q := truncate(query)
	span.SetAttributes(attribute.String("db.system", c.info.System), attribute.String("db.role", c.info.Role), attribute.String("db.statement", q))
	op := &operation{ctx: ctx, span: span, info: c.info, started: time.Now(),
		base: []any{"db_system", c.info.System, "db_role", c.info.Role, "query", q, "params", params(args)}}
	if spanName != "db.transaction" {
		op.log(slog.LevelInfo, "sql.start", "started")
	}
	return op
}

func (op *operation) log(level slog.Level, event, status string, extra ...any) {
	if op.info.Role == "platform" && level < slog.LevelWarn {
		level = slog.LevelDebug
	}
	args := append(append([]any{}, op.base...), "status", status)
	slog.Log(op.ctx, level, event, append(args, extra...)...)
}

func (op *operation) finish(event string, err error, rows int64) {
	d := time.Since(op.started)
	extra := []any{"duration_ms", ms(d)}
	if rows >= 0 {
		extra = append(extra, "rows", rows)
		op.span.SetAttributes(attribute.Int64("db.rows", rows))
	}
	level, status := slog.LevelInfo, "success"
	if err != nil && !errors.Is(err, driver.ErrSkip) {
		level, status = slog.LevelError, "error"
		if state := SQLState(err); state != "" {
			extra = append(extra, "sqlstate", state)
			op.span.SetAttributes(attribute.String("db.response.status_code", state))
		}
		extra = append(extra, "error", errText(err))
		op.span.RecordError(err)
		op.span.SetStatus(codes.Error, errText(err))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			level = slog.LevelWarn
		}
	}
	op.log(level, event, status, extra...)
	op.span.End()
}

// SQLState mengambil kode SQLSTATE dari error PostgreSQL atau MySQL/MariaDB.
func SQLState(err error) string {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return string(pe.Code)
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		if s := strings.TrimRight(string(me.SQLState[:]), "\x00"); s != "" {
			return s
		}
		return fmt.Sprint(me.Number)
	}
	return ""
}

// errText tidak menyertakan detail driver yang bisa memuat nilai data; hanya pesan utama.
func errText(err error) string {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return truncate(err.Error())
}

func truncate(s string) string {
	if len(s) > maxQueryLog {
		return s[:maxQueryLog] + "â€¦[truncated]"
	}
	return s
}

// params menampilkan parameter tanpa membocorkan data: teks/biner disamarkan, angka dan boolean apa adanya.
func params(args []driver.NamedValue) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	for i, a := range args {
		switch v := a.Value.(type) {
		case nil:
			out[i] = "NULL"
		case int64, int32, int, float64, float32, bool:
			out[i] = fmt.Sprint(v)
		case time.Time:
			out[i] = v.UTC().Format(time.RFC3339)
		case string:
			out[i] = fmt.Sprintf("***(text,%d)", len(v))
		case []byte:
			out[i] = fmt.Sprintf("***(bytes,%d)", len(v))
		default:
			out[i] = fmt.Sprintf("***(%T)", v)
		}
	}
	return out
}
