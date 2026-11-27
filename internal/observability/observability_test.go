package observability

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// capture memasang logger JSON ke buffer dan tracer in-memory untuk satu test.
func capture(t *testing.T) (*bytes.Buffer, *tracetest.InMemoryExporter) {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(NewHandler(&buf, slog.LevelDebug)))
	exp := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { slog.SetDefault(previous); _ = provider.Shutdown(context.Background()) })
	return &buf, exp
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log bukan JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func find(entries []map[string]any, event string) []map[string]any {
	var out []map[string]any
	for _, e := range entries {
		if e["event"] == event {
			out = append(out, e)
		}
	}
	return out
}

func TestLogRedactsSensitiveAttributesAndAddsTrace(t *testing.T) {
	buf, _ := capture(t)
	ctx, span := Start(context.Background(), "unit", "job_id", 42)
	slog.InfoContext(With(ctx, "database_id", 3), "auth.login", "password", "rahasia-sekali", "csrf_token", "abc", "session", "s1", "username", "alice")
	End(span, nil)
	out := buf.String()
	for _, secret := range []string{"rahasia-sekali", `"abc"`, `"s1"`} {
		if strings.Contains(out, secret) {
			t.Fatalf("nilai sensitif bocor ke log: %s", out)
		}
	}
	e := lines(t, buf)[0]
	if e["event"] != "auth.login" || e["level"] != "INFO" || e["time"] == nil {
		t.Fatalf("atribut dasar tidak sesuai: %v", e)
	}
	if e["job_id"] != float64(42) || e["database_id"] != float64(3) || e["username"] != "alice" {
		t.Fatalf("atribut korelasi hilang: %v", e)
	}
	if id, _ := e["trace_id"].(string); len(id) != 32 || e["span_id"] == nil {
		t.Fatalf("trace_id/span_id tidak dicatat: %v", e)
	}
}

func TestSetupWritesJSONAndSpansToSeparateStreams(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, exporter := range []string{"", "none"} {
		var logs, spans bytes.Buffer
		shutdown, err := setup("atlas-test", &logs, &spans, exporter, "info")
		if err != nil {
			t.Fatal(err)
		}
		ctx, span := Start(context.Background(), "unit")
		slog.InfoContext(ctx, "unit.event")
		slog.DebugContext(ctx, "unit.debug")
		span.End()
		if err := shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		e := lines(t, &logs)
		if len(e) != 1 || e[0]["service"] != "atlas-test" || e[0]["trace_id"] == nil {
			t.Fatalf("exporter %q: log %v", exporter, e)
		}
		if (exporter == "") != strings.Contains(spans.String(), `"Name":"unit"`) {
			t.Fatalf("exporter %q: span stream %q", exporter, spans.String())
		}
	}
}

func TestTraceContextSurvivesQueue(t *testing.T) {
	_, exp := capture(t)
	apiCtx, apiSpan := Start(context.Background(), "api")
	stored := InjectTrace(apiCtx)
	apiSpan.End()
	if !strings.HasPrefix(stored, "00-") {
		t.Fatalf("traceparent tidak valid: %q", stored)
	}
	workerCtx, workerSpan := Start(ExtractTrace(context.Background(), stored), "worker.process")
	_ = workerCtx
	workerSpan.End()
	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("jumlah span %d", len(spans))
	}
	api, worker := spans[0], spans[1]
	if worker.SpanContext.TraceID() != api.SpanContext.TraceID() || worker.Parent.SpanID() != api.SpanContext.SpanID() {
		t.Fatal("span worker bukan anak span API")
	}
	if got := ExtractTrace(context.Background(), ""); got == nil {
		t.Fatal("context kosong harus tetap dikembalikan")
	}
}

// mockDB membungkus driver sqlmock dengan WrapConnector.
func mockDB(t *testing.T, role string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	dsn := "observability_" + t.Name()
	raw, mock, err := sqlmock.NewWithDSN(dsn, sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	db := sql.OpenDB(WrapConnector(fixedConnector{raw.Driver(), dsn}, DB{System: "postgresql", Role: role}))
	t.Cleanup(func() { db.Close() })
	return db, mock
}

// fixedConnector memakai koneksi sqlmock yang sama melalui DSN uniknya.
type fixedConnector struct {
	d   driver.Driver
	dsn string
}

func (c fixedConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(c.dsn) }
func (c fixedConnector) Driver() driver.Driver                       { return c.d }

func TestSQLWrapperLogsExecQueryAndTransaction(t *testing.T) {
	buf, exp := capture(t)
	db, mock := mockDB(t, "target")
	multi := "CREATE TABLE public.a (id INT); CREATE TABLE public.b (id INT)"
	mock.ExpectExec(multi).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE public.items SET done = TRUE WHERE id=ANY($1)").WithArgs("{1,2}").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id FROM public.items WHERE owner=$1").WithArgs("secret-owner").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2).AddRow(3))
	ctx := With(context.Background(), "job_id", 9, "batch", 1)

	if _, err := db.ExecContext(ctx, multi); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE public.items SET done = TRUE WHERE id=ANY($1)", "{1,2}"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, "SELECT id FROM public.items WHERE owner=$1", "secret-owner")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	rows.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	entries := lines(t, buf)
	if strings.Contains(buf.String(), "secret-owner") {
		t.Fatal("parameter teks harus disamarkan")
	}
	finishes := find(entries, "sql.finish")
	if len(finishes) != 3 || len(find(entries, "sql.start")) != 3 {
		t.Fatalf("start/finish tidak lengkap: %v", entries)
	}
	if finishes[0]["query"] != multi || finishes[0]["status"] != "success" || finishes[0]["duration_ms"] == nil {
		t.Fatalf("exec multi-statement tidak tercatat: %v", finishes[0])
	}
	if finishes[1]["rows"] != float64(2) || finishes[1]["job_id"] != float64(9) || finishes[1]["batch"] != float64(1) {
		t.Fatalf("rows/korelasi batch tidak tercatat: %v", finishes[1])
	}
	if finishes[2]["rows"] != float64(3) {
		t.Fatalf("jumlah row query tidak tercatat: %v", finishes[2])
	}
	if len(find(entries, "sql.begin")) != 1 || len(find(entries, "sql.commit")) != 1 {
		t.Fatal("begin/commit harus dicatat terpisah")
	}
	if len(exp.GetSpans()) != 4 {
		t.Fatalf("span db: %d", len(exp.GetSpans()))
	}
}

func TestSQLWrapperRecordsSQLStateAndRollback(t *testing.T) {
	buf, exp := capture(t)
	db, mock := mockDB(t, "target")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO public.items VALUES (1)").WillReturnError(&pq.Error{Code: "23505", Message: "duplicate key value"})
	mock.ExpectRollback()
	tx, _ := db.Begin()
	if _, err := tx.Exec("INSERT INTO public.items VALUES (1)"); err == nil {
		t.Fatal("error diharapkan")
	}
	_ = tx.Rollback()
	entries := lines(t, buf)
	failed := find(entries, "sql.finish")
	if len(failed) != 1 || failed[0]["sqlstate"] != "23505" || failed[0]["level"] != "ERROR" || failed[0]["status"] != "error" {
		t.Fatalf("SQLSTATE tidak dicatat: %v", failed)
	}
	if len(find(entries, "sql.rollback")) != 1 {
		t.Fatal("rollback harus dicatat")
	}
	var errored bool
	for _, s := range exp.GetSpans() {
		if s.Status.Code.String() == "Error" {
			errored = true
		}
	}
	if !errored {
		t.Fatal("span error tidak ditandai")
	}
	if SQLState(errors.New("biasa")) != "" {
		t.Fatal("error non-database tidak punya SQLSTATE")
	}
}

func TestPlatformQueriesLogAtDebug(t *testing.T) {
	buf, _ := capture(t)
	db, mock := mockDB(t, "platform")
	mock.ExpectExec("SELECT 1").WillReturnResult(sqlmock.NewResult(0, 0))
	if _, err := db.Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	for _, e := range lines(t, buf) {
		if e["level"] != "DEBUG" {
			t.Fatalf("query platform sukses harus debug: %v", e)
		}
	}
}

func TestHTTPMiddlewareLogsActorRouteAndHidesSecrets(t *testing.T) {
	buf, exp := capture(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetActor(r.Context(), 7)
		slog.InfoContext(r.Context(), "migration.job.submitted")
		w.WriteHeader(http.StatusAccepted)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/databases/12/migrations/jobs?token=x", strings.NewReader(`{"password":"p"}`))
	req.Header.Set("Cookie", "session=very-secret")
	req.Header.Set("X-CSRF-Token", "csrf-secret")
	rec := httptest.NewRecorder()
	HTTP(inner).ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("request id tidak dikirim")
	}
	out := buf.String()
	for _, secret := range []string{"very-secret", "csrf-secret", "token=x", `"p"`} {
		if strings.Contains(out, secret) {
			t.Fatalf("data sensitif bocor: %s", out)
		}
	}
	entries := lines(t, buf)
	reqLog := find(entries, "http.request")
	if len(reqLog) != 1 || reqLog[0]["actor"] != float64(7) || reqLog[0]["http_status"] != float64(202) ||
		reqLog[0]["route"] != "/api/databases/{id}/migrations/jobs" || reqLog[0]["database_id"] != float64(12) {
		t.Fatalf("log request tidak sesuai: %v", reqLog)
	}
	inside := find(entries, "migration.job.submitted")
	if len(inside) != 1 || inside[0]["trace_id"] != reqLog[0]["trace_id"] || inside[0]["request_id"] != reqLog[0]["request_id"] {
		t.Fatal("log di dalam handler harus berbagi trace dan request id")
	}
	if spans := exp.GetSpans(); len(spans) != 1 || spans[0].Name != "POST /api/databases/{id}/migrations/jobs" {
		t.Fatalf("span HTTP: %v", spans)
	}
	buf.Reset()
	HTTP(inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if len(find(lines(t, buf), "http.request")) != 0 {
		t.Fatal("aset statis tidak perlu dicatat")
	}
}
