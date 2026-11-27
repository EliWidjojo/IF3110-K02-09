// Package observability menyiapkan structured log JSON (log/slog) dan tracing OpenTelemetry.
// Log ditulis ke stdout, span ke stderr; keduanya tidak disimpan di database platform.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "IF3110-K02-09"

var propagator = propagation.TraceContext{}

// Setup memasang logger default dan TracerProvider global untuk satu proses (server atau worker).
// OTEL_TRACES_EXPORTER=none mematikan exporter span; LOG_LEVEL=debug menampilkan log debug.
func Setup(service string) (func(context.Context) error, error) {
	return setup(service, os.Stdout, os.Stderr, os.Getenv("OTEL_TRACES_EXPORTER"), os.Getenv("LOG_LEVEL"))
}

func setup(service string, logOut, spanOut io.Writer, exporter, level string) (func(context.Context) error, error) {
	lvl := slog.LevelInfo
	if strings.EqualFold(level, "debug") {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(NewHandler(logOut, lvl)).With("service", service))
	otel.SetTextMapPropagator(propagator)
	if strings.EqualFold(exporter, "none") {
		// Tanpa exporter, trace ID tetap dibuat agar log masih dapat dikorelasikan.
		provider := sdktrace.NewTracerProvider()
		otel.SetTracerProvider(provider)
		return provider.Shutdown, nil
	}
	exp, err := stdouttrace.New(stdouttrace.WithWriter(spanOut))
	if err != nil {
		return nil, errors.New("exporter trace tidak dapat dibuat")
	}
	res, err := sdkresource.Merge(sdkresource.Default(), sdkresource.NewSchemaless(attribute.String("service.name", service)))
	if err != nil {
		res = sdkresource.Default()
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// ---------- Atribut korelasi di context ----------

type attrsKey struct{}
type requestKey struct{}

// requestInfo diisi oleh lapisan dalam (mis. middleware auth) dan dibaca middleware HTTP luar.
type requestInfo struct {
	mu    sync.Mutex
	actor int
}

// With menempelkan atribut korelasi (database_id, job_id, attempt_id, batch, ...) ke context.
// Semua log dengan context ini membawa atribut tersebut, dan span aktif ikut diberi atribut yang sama.
func With(ctx context.Context, args ...any) context.Context {
	if len(args) == 0 {
		return ctx
	}
	record := slog.Record{}
	record.Add(args...)
	var added []slog.Attr
	record.Attrs(func(a slog.Attr) bool { added = append(added, a); return true })
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(spanAttrs(added)...)
	}
	current, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	merged := make([]slog.Attr, 0, len(current)+len(added))
	for _, old := range current {
		replaced := false
		for _, a := range added {
			if a.Key == old.Key {
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, old)
		}
	}
	return context.WithValue(ctx, attrsKey{}, append(merged, added...))
}

// SetActor mencatat ID user terverifikasi untuk request ini dan log selanjutnya.
func SetActor(ctx context.Context, userID int) context.Context {
	if info, ok := ctx.Value(requestKey{}).(*requestInfo); ok {
		info.mu.Lock()
		info.actor = userID
		info.mu.Unlock()
	}
	return With(ctx, "actor", userID)
}

// ---------- Span ----------

// Start membuat span anak dan menempelkan atribut yang sama ke log berikutnya.
func Start(ctx context.Context, name string, args ...any) (context.Context, trace.Span) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name)
	return With(ctx, args...), span
}

// End menutup span dan mencatat error bila ada.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// InjectTrace mengubah trace context aktif menjadi header W3C traceparent untuk disimpan pada job queue.
func InjectTrace(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ExtractTrace memulihkan trace context yang dibawa job agar span worker menjadi anak dari span API.
func ExtractTrace(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return propagator.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

func spanAttrs(attrs []slog.Attr) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		v := a.Value.Resolve()
		switch v.Kind() {
		case slog.KindInt64:
			out = append(out, attribute.Int64(a.Key, v.Int64()))
		case slog.KindUint64:
			out = append(out, attribute.Int64(a.Key, int64(v.Uint64())))
		case slog.KindBool:
			out = append(out, attribute.Bool(a.Key, v.Bool()))
		case slog.KindFloat64:
			out = append(out, attribute.Float64(a.Key, v.Float64()))
		case slog.KindDuration:
			out = append(out, attribute.Float64(a.Key+"_ms", float64(v.Duration().Microseconds())/1000))
		default:
			out = append(out, attribute.String(a.Key, v.String()))
		}
	}
	return out
}

// ---------- slog handler ----------

// Handler menambahkan trace_id, span_id, dan atribut korelasi context ke setiap log JSON,
// serta menyamarkan atribut sensitif berdasarkan namanya.
type Handler struct{ next slog.Handler }

func NewHandler(w io.Writer, level slog.Leveler) *Handler {
	return &Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr})}
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey:
			a.Key = "time"
		case slog.MessageKey:
			a.Key = "event"
		}
	}
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{h.next.WithAttrs(attrs)}
}
func (h *Handler) WithGroup(name string) slog.Handler { return &Handler{h.next.WithGroup(name)} }
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(attrsKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.next.Handle(ctx, r)
}

var sensitiveWords = []string{"password", "secret", "token", "cookie", "csrf", "session", "dsn", "credential", "authorization", "capability"}

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, word := range sensitiveWords {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// ---------- HTTP ----------

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// HTTP membuat span per request API dan satu log http.request. Header Cookie/CSRF,
// query string, dan body tidak pernah dicatat. Aset statis tidak dicatat agar log tetap ringkas.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		info := &requestInfo{}
		id := requestID()
		ctx := context.WithValue(r.Context(), requestKey{}, info)
		ctx, span := otel.Tracer(tracerName).Start(ctx, r.Method+" "+r.URL.Path, trace.WithSpanKind(trace.SpanKindServer))
		ctx = With(ctx, "request_id", id)
		if dbID := databaseID(r.URL.Path); dbID > 0 {
			ctx = With(ctx, "database_id", dbID)
		}
		span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path))
		w.Header().Set("X-Request-ID", id)
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(ctx)
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		route := routeOf(r.URL.Path)
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", sw.status))
		level, status := slog.LevelInfo, "success"
		if sw.status >= 500 {
			level, status = slog.LevelError, "error"
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		} else if sw.status >= 400 {
			level, status = slog.LevelWarn, "rejected"
		}
		args := []any{"method", r.Method, "route", route, "http_status", sw.status, "status", status,
			"duration_ms", ms(time.Since(started)), "response_bytes", sw.bytes}
		info.mu.Lock()
		if info.actor > 0 {
			args = append(args, "actor", info.actor)
		}
		info.mu.Unlock()
		slog.Log(ctx, level, "http.request", args...)
		span.End()
	})
}

// routeOf mengganti segmen numerik dengan {id} agar nama span dan log memiliki kardinalitas rendah.
// Pola ServeMux bersarang tidak terlihat dari middleware luar, sehingga normalisasi dilakukan di sini.
func routeOf(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p != "" && strings.Trim(p, "0123456789") == "" {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// databaseID membaca ID database dari path API seperti /api/databases/{id}/... atau /api/viewer/databases/{id}/...
func databaseID(path string) int {
	parts := strings.Split(path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "databases" {
			if n, err := strconv.Atoi(parts[i+1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func requestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
