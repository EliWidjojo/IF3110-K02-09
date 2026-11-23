package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
)

// QUEUED hanya berarti pengajuan tersimpan. Worker kelak wajib memeriksa
// approval, revision, izin terbaru, dan lock target sebelum mengeksekusinya.
type JobStatus string

const (
	JobQueued      JobStatus = "QUEUED"
	JobRunning     JobStatus = "RUNNING"
	JobSuccess     JobStatus = "SUCCESS"
	JobFailed      JobStatus = "FAILED"
	JobNeedsReview JobStatus = "NEEDS_REVIEW"
)

var (
	ErrJobNotFound = errors.New("job tidak ditemukan pada database ini")
	ErrJobConflict = errors.New("idempotency key sudah digunakan untuk pengajuan berbeda")
	ErrJobStorage  = errors.New("penyimpanan job tidak tersedia")
)

// JobSubmission merupakan snapshot input, bukan referensi ke revision yang dapat berubah.
// Identitas pengaju, status, dan worker selalu ditetapkan backend.
type JobSubmission struct {
	Version      int    `json:"version"`
	QuerySQL     string `json:"query_sql"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}

type Job struct {
	ID          int64            `json:"id"`
	DatabaseID  int              `json:"database_id"`
	SubmittedBy int              `json:"submitted_by"`
	Submission  JobSubmission    `json:"submission"`
	Driver      database.Dialect `json:"driver"`
	Status      JobStatus        `json:"status"`
	WorkerID    *string          `json:"worker_id"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	StartedAt   *time.Time       `json:"started_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
	ErrorCode   string           `json:"error_code"`
	StatusURL   string           `json:"status_url"`
}

type jobConfiguration interface {
	Configuration(context.Context, int) (database.DatabaseConnection, error)
}

// Kontrak, validasi, penyimpanan, dan HTTP job berada dalam satu file fitur.
type JobService struct {
	db       *sql.DB
	access   access.DatabasePermissionChecker
	config   jobConfiguration
	identity admin.VerifiedUserProvider
}

func NewJobService(db *sql.DB, permissions access.DatabasePermissionChecker, config jobConfiguration, identity admin.VerifiedUserProvider) *JobService {
	return &JobService{db: db, access: permissions, config: config, identity: identity}
}

// ValidateSubmission tidak membuka koneksi target atau menjalankan SQL.
func ValidateSubmission(ctx context.Context, req JobSubmission, driver database.Dialect, key string) error {
	if len(key) < 1 || len(key) > 128 {
		return &InputError{"Idempotency-Key wajib diisi, maksimum 128 karakter ASCII tanpa spasi"}
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return &InputError{"Idempotency-Key harus berupa ASCII tanpa spasi"}
		}
	}
	if req.Version <= 0 || req.Version > 2147483647 || len(req.QuerySQL) > 12288 || !utf8.ValidString(req.QuerySQL) || strings.ContainsRune(req.QuerySQL, 0) {
		return &InputError{"versi atau ukuran/encoding SQL tidak valid (maksimum 12 KiB)"}
	}
	if _, err := database.Statements(req.QuerySQL, driver, req.NonAtomicDDL); err != nil {
		return errors.Join(ErrInvalidMigration, err)
	}
	if driver == database.Postgres {
		if err := validatePostgresMigration(ctx, req.QuerySQL); err != nil {
			return errors.Join(ErrInvalidMigration, err)
		}
	}
	return nil
}

func (s *JobService) SubmitJob(ctx context.Context, databaseID int, key string, req JobSubmission) (Job, error) {
	if databaseID <= 0 {
		return Job{}, &InputError{"ID database tidak valid"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.access.RequireEdit(ctx, databaseID); err != nil {
		return Job{}, err
	}
	actor, err := s.identity.VerifiedUserID(ctx)
	if err != nil || actor <= 0 {
		return Job{}, admin.ErrUnauthenticated
	}
	cfg, err := s.config.Configuration(ctx, databaseID)
	if err != nil {
		return Job{}, err
	}
	if err = ValidateSubmission(ctx, req, cfg.Driver, key); err != nil {
		return Job{}, err
	}
	return s.createJob(ctx, databaseID, actor, key, req, cfg.Driver)
}

const jobColumns = `id, database_id, submitted_by, version, query_sql, non_atomic_ddl,
	driver, status, worker_id, created_at, updated_at, started_at, finished_at, error_code`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.DatabaseID, &j.SubmittedBy, &j.Submission.Version,
		&j.Submission.QuerySQL, &j.Submission.NonAtomicDDL, &j.Driver, &j.Status,
		&j.WorkerID, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt, &j.ErrorCode)
	j.StatusURL = fmt.Sprintf("/api/databases/%d/migrations/jobs/%d", j.DatabaseID, j.ID)
	return j, err
}

func (s *JobService) createJob(ctx context.Context, databaseID, actor int, key string, req JobSubmission, driver database.Dialect) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `INSERT INTO platform.migration_jobs
		(database_id, submitted_by, idempotency_key, version, query_sql, non_atomic_ddl, driver)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (database_id, submitted_by, idempotency_key) DO NOTHING
		RETURNING `+jobColumns, databaseID, actor, key, req.Version, req.QuerySQL, req.NonAtomicDDL, driver))
	if errors.Is(err, sql.ErrNoRows) {
		// Statement terpisah memperoleh snapshot baru setelah INSERT paralel selesai.
		// Tidak memakai DO UPDATE agar retry tidak mengubah job/status/timestamp lama.
		j, err = scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM platform.migration_jobs
			WHERE database_id=$1 AND submitted_by=$2 AND idempotency_key=$3`, databaseID, actor, key))
		if err == nil && (j.Submission != req || j.Driver != driver) {
			return Job{}, ErrJobConflict
		}
	}
	if err != nil {
		return Job{}, ErrJobStorage
	}
	return j, nil
}

func (s *JobService) GetJob(ctx context.Context, databaseID int, jobID int64) (Job, error) {
	if databaseID <= 0 || jobID <= 0 {
		return Job{}, &InputError{"ID database atau job tidak valid"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Hak baca database berlaku juga untuk job pengguna lain pada database itu.
	if err := s.access.RequireRead(ctx, databaseID); err != nil {
		return Job{}, err
	}
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM platform.migration_jobs
		WHERE database_id=$1 AND id=$2`, databaseID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, ErrJobStorage
	}
	return j, nil
}

type JobHandler struct {
	service *JobService
	csrf    admin.UserCSRFVerifier
}

func NewJobHandler(service *JobService, csrf admin.UserCSRFVerifier) *JobHandler {
	return &JobHandler{service: service, csrf: csrf}
}

func RegisterJobRoutes(mux *http.ServeMux, h *JobHandler) {
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/jobs", h.HandleSubmitJob)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/jobs/{job_id}", h.HandleGetJob)
}

func (h *JobHandler) HandleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if h.csrf == nil || h.csrf.ValidateCSRF(r) != nil {
		admin.WriteUserServiceError(w, admin.ErrInvalidUserCSRF)
		return
	}
	databaseID, err := jobDatabaseID(r)
	if err != nil {
		writeJobError(w, err)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		writeJobError(w, &InputError{"tepat satu header Idempotency-Key wajib disertakan"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	var req JobSubmission
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	j, err := h.service.SubmitJob(r.Context(), databaseID, r.Header.Get("Idempotency-Key"), req)
	if err != nil {
		writeJobError(w, err)
		return
	}
	w.Header().Set("Location", j.StatusURL)
	httpapi.WriteJSON(w, http.StatusAccepted, j, nil)
}

func (h *JobHandler) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	databaseID, err := jobDatabaseID(r)
	if err != nil {
		writeJobError(w, err)
		return
	}
	jobID, err := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeJobError(w, &InputError{"ID job tidak valid"})
		return
	}
	j, err := h.service.GetJob(r.Context(), databaseID, jobID)
	if err != nil {
		writeJobError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, j, nil)
}

func jobDatabaseID(r *http.Request) (int, error) {
	id, err := strconv.ParseInt(r.PathValue("database_id"), 10, 32)
	if err != nil || id <= 0 {
		return 0, &InputError{"ID database tidak valid"}
	}
	return int(id), nil
}

func writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrJobNotFound):
		httpapi.WriteJSON(w, 404, nil, &httpapi.Error{Code: "job_not_found", Message: ErrJobNotFound.Error()})
	case errors.Is(err, ErrJobConflict):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "idempotency_conflict", Message: ErrJobConflict.Error()})
	case errors.Is(err, ErrJobStorage):
		httpapi.WriteJSON(w, 503, nil, &httpapi.Error{Code: "job_storage_unavailable", Message: ErrJobStorage.Error()})
	default:
		writeError(w, err)
	}
}
