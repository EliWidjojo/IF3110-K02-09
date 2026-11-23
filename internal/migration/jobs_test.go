package migration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateJobSubmission(t *testing.T) {
	for _, tc := range []struct {
		name, sql, key   string
		version          int
		driver           database.Dialect
		nonAtomic, valid bool
	}{
		{"postgres", "CREATE TABLE example(id INT)", "submit-1", 1, database.Postgres, false, true},
		{"mysql dml", "INSERT INTO example VALUES (1)", "submit-1", 1, database.MySQL, false, true},
		{"mysql ddl opt in", "CREATE TABLE example(id INT)", "submit-1", 1, database.MySQL, true, true},
		{"mysql ddl requires opt in", "CREATE TABLE example(id INT)", "submit-1", 1, database.MySQL, false, false},
		{"empty", " ", "a", 1, database.Postgres, false, false},
		{"comment only", "-- comment", "a", 1, database.Postgres, false, false},
		{"syntax", "CREATE TABLE", "a", 1, database.Postgres, false, false},
		{"transaction", "COMMIT", "a", 1, database.Postgres, false, false},
		{"version", "CREATE TABLE example(id INT)", "a", 0, database.Postgres, false, false},
		{"oversized", strings.Repeat("x", 12289), "a", 1, database.Postgres, false, false},
		{"missing key", "CREATE TABLE example(id INT)", "", 1, database.Postgres, false, false},
		{"oversized key", "CREATE TABLE example(id INT)", strings.Repeat("a", 129), 1, database.Postgres, false, false},
		{"space key", "CREATE TABLE example(id INT)", "a b", 1, database.Postgres, false, false},
		{"nul", "CREATE TABLE example(id INT)\x00", "a", 1, database.Postgres, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSubmission(context.Background(), JobSubmission{tc.version, tc.sql, tc.nonAtomic}, tc.driver, tc.key)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type jobTestPermissions struct{ err error }

func (p jobTestPermissions) RequireRead(context.Context, int) error { return p.err }
func (p jobTestPermissions) RequireEdit(context.Context, int) error { return p.err }

type jobTestIdentity struct{}

func (jobTestIdentity) VerifiedUserID(context.Context) (int, error) { return 7, nil }

type jobTestConfig struct{}

func (jobTestConfig) Configuration(context.Context, int) (database.DatabaseConnection, error) {
	return database.DatabaseConnection{Driver: database.Postgres}, nil
}

type jobTestCSRF struct{ err error }

func (c jobTestCSRF) ValidateCSRF(*http.Request) error { return c.err }

func TestJobsRejectAccessBeforeStorage(t *testing.T) {
	s := NewJobService(nil, jobTestPermissions{access.ErrDatabaseAccessDenied}, nil, nil)
	if _, err := s.SubmitJob(context.Background(), 1, "key", JobSubmission{}); !errors.Is(err, access.ErrDatabaseAccessDenied) {
		t.Fatal(err)
	}
	if _, err := s.GetJob(context.Background(), 1, 1); !errors.Is(err, access.ErrDatabaseAccessDenied) {
		t.Fatal(err)
	}
}

func TestJobStorageFailureIsSanitized(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewJobService(db, jobTestPermissions{}, jobTestConfig{}, jobTestIdentity{})
	mock.ExpectQuery("INSERT INTO platform.migration_jobs").WillReturnError(errors.New("private database credentials"))
	if _, err := s.SubmitJob(context.Background(), 1, "key", JobSubmission{Version: 1, QuerySQL: "CREATE TABLE example(id INT)"}); !errors.Is(err, ErrJobStorage) {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT .* FROM platform.migration_jobs").WithArgs(1, int64(2)).WillReturnError(errors.New("private database credentials"))
	if _, err := s.GetJob(context.Background(), 1, 2); !errors.Is(err, ErrJobStorage) {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestJobHTTPRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, key, contentType string
		csrf                                       error
		want                                       int
	}{
		{"csrf", "POST", "/api/databases/1/migrations/jobs", `{}`, "key", "application/json", admin.ErrInvalidUserCSRF, 403},
		{"database id", "POST", "/api/databases/0/migrations/jobs", `{}`, "key", "application/json", nil, 400},
		{"missing key", "POST", "/api/databases/1/migrations/jobs", `{}`, "", "application/json", nil, 400},
		{"unknown actor", "POST", "/api/databases/1/migrations/jobs", `{"submitted_by":1}`, "key", "application/json", nil, 400},
		{"unknown worker", "POST", "/api/databases/1/migrations/jobs", `{"worker_id":"w1"}`, "key", "application/json", nil, 400},
		{"unknown status", "POST", "/api/databases/1/migrations/jobs", `{"status":"SUCCESS"}`, "key", "application/json", nil, 400},
		{"multiple JSON", "POST", "/api/databases/1/migrations/jobs", `{} {}`, "key", "application/json", nil, 400},
		{"content type", "POST", "/api/databases/1/migrations/jobs", `{}`, "key", "text/plain", nil, 415},
		{"body size", "POST", "/api/databases/1/migrations/jobs", `{"query_sql":"` + strings.Repeat("x", 17000) + `"}`, "key", "application/json", nil, 413},
		{"job id", "GET", "/api/databases/1/migrations/jobs/0", "", "", "", nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterJobRoutes(mux, NewJobHandler(nil, jobTestCSRF{tc.csrf}))
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("HTTP %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
