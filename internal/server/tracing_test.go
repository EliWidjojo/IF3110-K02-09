package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"IF3110-K02-09/internal/migration"
	"IF3110-K02-09/internal/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Trace API -> queue -> worker -> lock -> query harus berada pada satu trace ID,
// dan log JSON di worker membawa trace_id yang sama tanpa membocorkan kredensial.
func TestTracePropagatesFromAPIToWorker(t *testing.T) {
	f := fixture(t)
	exp := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(provider)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(observability.NewHandler(&logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(previous); _ = provider.Shutdown(context.Background()) })

	f.grant(t, 2, "View + Edit")
	alice := &browser{t: t, handler: f.handler}
	alice.login("alice")
	job, code := f.submitAndRun(alice, `{"version":1,"query_sql":"CREATE TABLE public.traced (id int)"}`)
	if code != 202 || job.Status != migration.JobSuccess {
		t.Fatalf("job tidak sukses: code=%d status=%s", code, job.Status)
	}

	var api, worker sdktrace.ReadOnlySpan
	names := map[string]int{}
	for _, s := range exp.GetSpans().Snapshots() {
		names[s.Name()]++
		switch {
		case s.Name() == "POST /api/databases/{id}/migrations/execute":
			api = s
		case s.Name() == "worker.process":
			worker = s
		}
	}
	if api == nil || worker == nil {
		t.Fatalf("span API/worker tidak ditemukan: %v", names)
	}
	traceID := api.SpanContext().TraceID()
	if worker.SpanContext().TraceID() != traceID || worker.Parent().SpanID() != api.SpanContext().SpanID() {
		t.Fatal("span worker tidak menjadi anak span API")
	}
	var lock, query bool
	for _, s := range exp.GetSpans().Snapshots() {
		if s.SpanContext().TraceID() != traceID {
			continue
		}
		lock = lock || s.Name() == "worker.lock"
		query = query || (s.Name() == "db.exec" && hasAttr(s, "db.statement", "CREATE TABLE public.traced (id int)"))
	}
	if !lock || !query {
		t.Fatalf("span lock/query tidak berada pada trace yang sama: %v", names)
	}

	var result map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["event"] == "worker.job.result" {
			result = e
		}
	}
	if result == nil || result["status"] != "SUCCESS" || result["trace_id"] != traceID.String() || result["job_id"] != float64(job.ID) {
		t.Fatalf("log hasil worker tidak berkorelasi: %v", result)
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), f.roles.Password) {
		t.Fatal("log membocorkan password")
	}
}

func hasAttr(s sdktrace.ReadOnlySpan, key, value string) bool {
	for _, a := range s.Attributes() {
		if string(a.Key) == key && a.Value.AsString() == value {
			return true
		}
	}
	return false
}
