package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"IF3110-K02-09/internal/access"
	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/httpapi"
	"IF3110-K02-09/internal/observability"
)

// QUEUED menyimpan snapshot sambil menunggu persetujuan dan klaim worker.
type JobStatus string

const (
	JobQueued            JobStatus = "QUEUED"
	JobRunning           JobStatus = "RUNNING"
	JobPartial           JobStatus = "PARTIAL"
	JobReady             JobStatus = "READY"
	JobRecovering        JobStatus = "RECOVERING"
	JobBlocked           JobStatus = "BLOCKED"
	JobSuccess           JobStatus = "SUCCESS"
	JobFailed            JobStatus = "FAILED"
	JobNeedsReview       JobStatus = "NEEDS_REVIEW"
	JobPartiallyReverted JobStatus = "PARTIALLY_REVERTED"
	JobReverted          JobStatus = "REVERTED"
)

var (
	ErrJobNotFound    = errors.New("job tidak ditemukan pada database ini")
	ErrJobConflict    = errors.New("idempotency key sudah digunakan untuk pengajuan berbeda")
	ErrRetryConflict  = errors.New("retry tidak berlaku untuk target, status job, atau nomor retry ini")
	ErrCancelConflict = errors.New("pembatalan memerlukan batch committed dan SQL pemulihan yang sudah disetujui; jika tidak tersedia, ajukan migration perbaikan")
	ErrJobStorage     = errors.New("penyimpanan job tidak tersedia")
)

// JobSubmission merupakan snapshot input, bukan referensi ke revision yang dapat berubah.
// Identitas pengaju, status, dan worker selalu ditetapkan backend.
type JobSubmission struct {
	BatchSize    int    `json:"batch_size,omitempty"`
	DownSQL      string `json:"down_sql,omitempty"`
	Operation    string `json:"operation,omitempty"`
	SourceJob    int64  `json:"source_job,omitempty"`
	Version      int    `json:"version"`
	QuerySQL     string `json:"query_sql"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}

type Job struct {
	RecoveryCount    int        `json:"recovery_count"`
	NextAttemptAt    *time.Time `json:"next_attempt_at,omitempty"`
	registrationHash string
	inspectOnly      bool
	CancelOrigin     JobStatus `json:"cancel_origin,omitempty"`
	CancelWaiting    bool      `json:"cancel_waiting,omitempty"`
	RevertedBatches  int64     `json:"reverted_batches"`
	RevertedRows     int64     `json:"reverted_rows"`
	RetryCount       int64     `json:"retry_count"`
	retryPending     bool
	AttemptID        *int64           `json:"attempt_id"`
	CommittedBatches int64            `json:"committed_batches"`
	CommittedRows    int64            `json:"committed_rows"`
	ID               int64            `json:"id"`
	DatabaseID       int              `json:"database_id"`
	SubmittedBy      int              `json:"submitted_by"`
	Submission       JobSubmission    `json:"submission"`
	Driver           database.Dialect `json:"driver"`
	Status           JobStatus        `json:"status"`
	WorkerID         *string          `json:"worker_id"`
	HeartbeatAt      *time.Time       `json:"heartbeat_at"`
	LeaseUntil       *time.Time       `json:"lease_until"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	StartedAt        *time.Time       `json:"started_at"`
	FinishedAt       *time.Time       `json:"finished_at"`
	ErrorCode        string           `json:"error_code"`
	StatusURL        string           `json:"status_url"`
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
	if req.BatchSize < 0 || req.BatchSize > 100000 || (req.BatchSize > 0 && (driver != database.Postgres || batchUpdate.FindStringSubmatch(req.QuerySQL) == nil || (req.DownSQL != "" && req.DownSQL != batchRestoreSQL(req.QuerySQL)))) {
		return &InputError{"batch memerlukan PostgreSQL, ukuran 1-100000, dan UPDATE public.tabel SET kolom = TRUE atau FALSE"}
	}
	if req.DownSQL != "" && req.BatchSize == 0 {
		down := JobSubmission{Version: req.Version, QuerySQL: req.DownSQL, NonAtomicDDL: req.NonAtomicDDL}
		if err := ValidateSubmission(ctx, down, driver, key); err != nil {
			return err
		}
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
	if req.Operation == "" {
		req.Operation = "apply"
	}
	switch req.Operation {
	case "apply":
		if req.SourceJob != 0 {
			return Job{}, &InputError{"source_job hanya untuk rollback atau forward"}
		}
	case "rollback", "forward":
		if req.SourceJob <= 0 || req.QuerySQL != "" || req.DownSQL != "" || req.BatchSize != 0 || req.NonAtomicDDL {
			return Job{}, &InputError{"gunakan source_job dan versi eksekusi baru"}
		}
		source, err := s.GetJob(ctx, databaseID, req.SourceJob)
		if err != nil {
			return Job{}, err
		}
		if source.Status != JobSuccess || source.Submission.DownSQL == "" || source.Submission.BatchSize != 0 || (req.Operation == "forward") != (source.Submission.Operation == "rollback") {
			return Job{}, &InputError{"sumber tidak mendukung operasi ini"}
		}
		req.QuerySQL, req.DownSQL = source.Submission.DownSQL, source.Submission.QuerySQL
		req.NonAtomicDDL = source.Submission.NonAtomicDDL
	default:
		return Job{}, &InputError{"operation tidak valid"}
	}
	if err = ValidateSubmission(ctx, req, cfg.Driver, key); err != nil {
		return Job{}, err
	}
	return s.createJob(ctx, databaseID, actor, key, req, cfg.Driver)
}

const jobColumns = `id, database_id, submitted_by, version, query_sql, non_atomic_ddl,
	driver, status, worker_id, created_at, updated_at, started_at, finished_at, error_code, heartbeat_at, lease_until, batch_size, down_sql, operation, source_job, attempt_id, committed_batches, committed_rows, retry_count, retry_pending, cancel_origin, reverted_batches, reverted_rows,cancel_waiting,
	recovery_count,next_attempt_at,registration_hash,inspect_only`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.DatabaseID, &j.SubmittedBy, &j.Submission.Version,
		&j.Submission.QuerySQL, &j.Submission.NonAtomicDDL, &j.Driver, &j.Status,
		&j.WorkerID, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt, &j.ErrorCode, &j.HeartbeatAt, &j.LeaseUntil, &j.Submission.BatchSize, &j.Submission.DownSQL, &j.Submission.Operation, &j.Submission.SourceJob, &j.AttemptID, &j.CommittedBatches, &j.CommittedRows, &j.RetryCount, &j.retryPending, &j.CancelOrigin, &j.RevertedBatches, &j.RevertedRows, &j.CancelWaiting, &j.RecoveryCount, &j.NextAttemptAt, &j.registrationHash, &j.inspectOnly)
	j.StatusURL = fmt.Sprintf("/api/databases/%d/migrations/jobs/%d", j.DatabaseID, j.ID)
	return j, err
}

func (s *JobService) createJob(ctx context.Context, databaseID, actor int, key string, req JobSubmission, driver database.Dialect) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `INSERT INTO platform.migration_jobs
		(database_id, submitted_by, idempotency_key, version, query_sql, non_atomic_ddl, driver, batch_size,down_sql,operation,source_job,trace_context)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (database_id, submitted_by, idempotency_key) DO NOTHING
		RETURNING `+jobColumns, databaseID, actor, key, req.Version, req.QuerySQL, req.NonAtomicDDL, driver, req.BatchSize, req.DownSQL, req.Operation, req.SourceJob,
		// Trace context API ikut disimpan agar span worker tersambung ke request pengaju.
		observability.InjectTrace(ctx)))
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

// RetryBatch queues the same immutable submission and starts a new approval round.
// Numbered requests make a lost HTTP response safe to replay after the worker finishes.
func (s *JobService) RetryBatch(ctx context.Context, databaseID int, jobID, number int64) (Job, error) {
	return s.queueBatchAction(ctx, databaseID, jobID, number, false)
}

func (s *JobService) queueBatchAction(ctx context.Context, databaseID int, jobID, number int64, cancelBatch bool) (Job, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if databaseID <= 0 || jobID <= 0 || number <= 0 {
		return Job{}, &InputError{"ID database, job, dan nomor retry harus positif"}
	}
	if err := s.access.RequireEdit(ctx, databaseID); err != nil {
		return Job{}, err
	}
	actor, err := s.identity.VerifiedUserID(ctx)
	if err != nil || actor <= 0 {
		return Job{}, admin.ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, ErrJobStorage
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM platform.migration_jobs
		WHERE database_id=$1 AND id=$2 FOR UPDATE`, databaseID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, ErrJobStorage
	}
	if number <= j.RetryCount && cancelBatch == (j.CancelOrigin != "") {
		return j, nil
	}
	origin := j.CancelOrigin
	waitForBatch := cancelBatch && origin == "" && j.Status == JobRunning
	if cancelBatch {
		eligible := waitForBatch || j.Status == JobPartial || j.Status == JobSuccess || j.Status == JobPartiallyReverted ||
			(origin != "" && j.Status == JobNeedsReview)
		if !eligible || number != j.RetryCount+1 || j.AttemptID == nil || j.CommittedBatches == 0 ||
			j.Submission.BatchSize == 0 || j.Submission.DownSQL == "" {
			return Job{}, ErrCancelConflict
		}
		if waitForBatch {
			origin = JobPartial
		} else if origin == "" {
			origin = j.Status
		}
	} else if (origin != "" && (j.Status != JobPartial || j.RevertedBatches != 0)) || number != j.RetryCount+1 || j.AttemptID == nil || j.Submission.BatchSize == 0 ||
		(j.Status != JobPartial && j.Status != JobFailed) {
		return Job{}, ErrRetryConflict
	} else {
		origin = ""
	}
	if err := ValidateSubmission(ctx, j.Submission, j.Driver, "retry"); err != nil {
		return Job{}, err
	}
	j, err = scanJob(tx.QueryRowContext(ctx, `UPDATE platform.migration_jobs
		SET retry_count=$2,retry_pending=$4,retry_requested_by=$3,cancel_origin=$5,cancel_waiting=$6,recovery_count=0,next_attempt_at=NULL,inspect_only=false,
		    status=CASE WHEN $6 THEN status ELSE 'QUEUED' END,finished_at=NULL,
		    worker_id=CASE WHEN $6 THEN worker_id END,heartbeat_at=CASE WHEN $6 THEN heartbeat_at END,error_code=''
		WHERE id=$1 AND EXISTS (SELECT 1 FROM platform.job_admission WHERE id=$1 AND same_target)
		RETURNING `+jobColumns, jobID, number, actor, !cancelBatch, origin, waitForBatch))
	if errors.Is(err, sql.ErrNoRows) {
		if cancelBatch {
			return Job{}, ErrCancelConflict
		}
		return Job{}, ErrRetryConflict
	}
	if err != nil {
		return Job{}, ErrJobStorage
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM platform.job_approvals WHERE job_id=$1", jobID); err != nil {
		return Job{}, ErrJobStorage
	}
	if err := tx.Commit(); err != nil {
		return Job{}, ErrJobStorage
	}
	return j, nil
}

func NewJobHandler(service *JobService, csrf admin.UserCSRFVerifier) *JobHandler {
	return &JobHandler{service: service, csrf: csrf}
}

func RegisterJobRoutes(mux *http.ServeMux, h *JobHandler) {
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/jobs/{job_id}/recovery", h.handleRecovery)
	mux.HandleFunc("PUT /api/databases/{database_id}/migrations/jobs/{job_id}/cancellations/{number}", func(w http.ResponseWriter, r *http.Request) { h.handleBatchAction(w, r, true) })
	mux.HandleFunc("PUT /api/databases/{database_id}/migrations/jobs/{job_id}/retries/{number}", h.HandleRetryBatch)
	mux.HandleFunc("PUT /api/databases/{database_id}/migrations/jobs/{job_id}/approvals/{group_id}", h.HandleApprove)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/execute", h.HandleSubmitJob)
	mux.HandleFunc("POST /api/databases/{database_id}/migrations/jobs", h.HandleSubmitJob)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/jobs/{job_id}", h.HandleGetJob)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/jobs", h.HandleListJobs)
	mux.HandleFunc("GET /api/databases/{database_id}/migrations/jobs/{job_id}/approvals", h.HandleListApprovals)
}

// ListJobs membaca job terbaru pada satu database untuk dipantau SPA; hanya perlu hak baca.
func (s *JobService) ListJobs(ctx context.Context, databaseID, limit, offset int) ([]Job, error) {
	if databaseID <= 0 || limit < 1 || limit > 100 || offset < 0 {
		return nil, &InputError{"ID database atau pagination tidak valid"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.access.RequireRead(ctx, databaseID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM platform.migration_jobs
		WHERE database_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, databaseID, limit, offset)
	if err != nil {
		return nil, ErrJobStorage
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, ErrJobStorage
		}
		jobs = append(jobs, j)
	}
	if rows.Err() != nil {
		return nil, ErrJobStorage
	}
	return jobs, nil
}

// JobApproval menunjukkan status approval putaran saat ini untuk setiap group yang memiliki akses.
type JobApproval struct {
	GroupID        int        `json:"group_id"`
	GroupName      string     `json:"group_name"`
	LeaderID       *int       `json:"leader_id"`
	LeaderUsername *string    `json:"leader_username"`
	Approved       bool       `json:"approved"`
	ApprovedAt     *time.Time `json:"approved_at"`
}

func (s *JobService) ListApprovals(ctx context.Context, databaseID int, jobID int64) ([]JobApproval, error) {
	if _, err := s.GetJob(ctx, databaseID, jobID); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT g.group_id, gr.name, l.user_id, u.username, a.approved_at
		FROM platform.migration_jobs j
		JOIN platform.group_database_access g ON g.database_id=j.database_id
		JOIN platform.groups gr ON gr.id=g.group_id
		LEFT JOIN platform.group_leaders l ON l.group_id=g.group_id
		LEFT JOIN platform.users u ON u.id=l.user_id
		LEFT JOIN platform.job_approvals a ON a.job_id=j.id AND a.group_id=g.group_id
		WHERE j.database_id=$1 AND j.id=$2 ORDER BY gr.name, g.group_id`, databaseID, jobID)
	if err != nil {
		return nil, ErrJobStorage
	}
	defer rows.Close()
	approvals := []JobApproval{}
	for rows.Next() {
		var a JobApproval
		if err := rows.Scan(&a.GroupID, &a.GroupName, &a.LeaderID, &a.LeaderUsername, &a.ApprovedAt); err != nil {
			return nil, ErrJobStorage
		}
		a.Approved = a.ApprovedAt != nil
		approvals = append(approvals, a)
	}
	if rows.Err() != nil {
		return nil, ErrJobStorage
	}
	return approvals, nil
}

func (h *JobHandler) HandleListJobs(w http.ResponseWriter, r *http.Request) {
	databaseID, err := jobDatabaseID(r)
	limit, e1 := httpapi.PaginationValue(r, "limit", 20)
	offset, e2 := httpapi.PaginationValue(r, "offset", 0)
	if err != nil || e1 != nil || e2 != nil {
		writeJobError(w, &InputError{"ID database atau pagination tidak valid"})
		return
	}
	jobs, err := h.service.ListJobs(r.Context(), databaseID, limit, offset)
	if err != nil {
		writeJobError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, jobs, nil)
}

func (h *JobHandler) HandleListApprovals(w http.ResponseWriter, r *http.Request) {
	databaseID, err := jobDatabaseID(r)
	jobID, e := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil || e != nil || jobID <= 0 {
		writeJobError(w, &InputError{"ID database atau job tidak valid"})
		return
	}
	approvals, err := h.service.ListApprovals(r.Context(), databaseID, jobID)
	if err != nil {
		writeJobError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, approvals, nil)
}

// Re-read authoritative history after an operator has restored connectivity
// or resolved an ambiguous target attempt. The request cannot set its outcome.
func (h *JobHandler) handleRecovery(w http.ResponseWriter, r *http.Request) {
	if h.csrf == nil || h.csrf.ValidateCSRF(r) != nil {
		admin.WriteUserServiceError(w, admin.ErrInvalidUserCSRF)
		return
	}
	dbID, err := jobDatabaseID(r)
	jobID, e := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	if err != nil || e != nil || jobID <= 0 {
		writeJobError(w, &InputError{"ID database atau job tidak valid"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err = h.service.access.RequireEdit(ctx, dbID); err != nil {
		writeJobError(w, err)
		return
	}
	j, err := scanJob(h.service.db.QueryRowContext(ctx, `UPDATE platform.migration_jobs SET status='RECOVERING',inspect_only=true,
		recovery_count=0,next_attempt_at=clock_timestamp(),finished_at=NULL,error_code='checking_history'
		WHERE database_id=$1 AND id=$2 AND status='NEEDS_REVIEW' AND attempt_id IS NOT NULL
		AND EXISTS (SELECT 1 FROM platform.job_admission a WHERE a.id=$2 AND a.same_target)
		RETURNING `+jobColumns, dbID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		j, err = h.service.GetJob(ctx, dbID, jobID)
		if err == nil && !(j.inspectOnly && (j.Status == JobRunning || j.Status == JobRecovering)) {
			httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "recovery_conflict", Message: "pemeriksaan memerlukan attempt pada target asli dengan status NEEDS_REVIEW"})
			return
		}
	} else if err != nil {
		err = ErrJobStorage
	}
	if err != nil {
		writeJobError(w, err)
		return
	}
	slog.InfoContext(ctx, "migration.job.recovery_request", "status", string(j.Status), "job_id", jobID)
	w.Header().Set("Location", j.StatusURL)
	httpapi.WriteJSON(w, http.StatusAccepted, j, nil)
}

func (h *JobHandler) HandleRetryBatch(w http.ResponseWriter, r *http.Request) {
	h.handleBatchAction(w, r, false)
}

func (h *JobHandler) handleBatchAction(w http.ResponseWriter, r *http.Request, cancelBatch bool) {
	if h.csrf == nil || h.csrf.ValidateCSRF(r) != nil {
		admin.WriteUserServiceError(w, admin.ErrInvalidUserCSRF)
		return
	}
	dbID, err := jobDatabaseID(r)
	jobID, jobErr := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	number, numberErr := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || jobErr != nil || numberErr != nil {
		writeJobError(w, &InputError{"ID database, job, atau nomor retry tidak valid"})
		return
	}
	j, err := h.service.queueBatchAction(r.Context(), dbID, jobID, number, cancelBatch)
	event := "migration.batch.retry_request"
	if cancelBatch {
		event = "migration.batch.cancel_request"
	}
	if err != nil {
		slog.WarnContext(r.Context(), event, "status", "rejected", "job_id", jobID, "request_number", number, "error", err.Error())
		writeJobError(w, err)
		return
	}
	slog.InfoContext(r.Context(), event, "status", string(j.Status), "job_id", jobID, "request_number", number)
	w.Header().Set("Location", j.StatusURL)
	httpapi.WriteJSON(w, http.StatusAccepted, j, nil)
}

func (h *JobHandler) HandleApprove(w http.ResponseWriter, r *http.Request) {
	if h.csrf == nil || h.csrf.ValidateCSRF(r) != nil {
		admin.WriteUserServiceError(w, admin.ErrInvalidUserCSRF)
		return
	}
	id, err := jobDatabaseID(r)
	job, e1 := strconv.ParseInt(r.PathValue("job_id"), 10, 64)
	group, e2 := strconv.ParseInt(r.PathValue("group_id"), 10, 32)
	var round int64
	var roundErr error
	if r.URL.Query().Has("retry_count") {
		round, roundErr = strconv.ParseInt(r.URL.Query().Get("retry_count"), 10, 64)
	}
	if err != nil || e1 != nil || e2 != nil || roundErr != nil || round < 0 || job <= 0 || group <= 0 {
		writeJobError(w, &InputError{"ID database, job atau group tidak valid"})
		return
	}
	ctx := r.Context()
	actor, err := h.service.identity.VerifiedUserID(ctx)
	if err != nil || actor <= 0 {
		writeJobError(w, admin.ErrUnauthenticated)
		return
	}
	// Only the current active leader can approve this immutable job for their own group.
	var allowed bool
	err = h.service.db.QueryRowContext(ctx, `WITH eligible AS (
	 SELECT j.id,l.group_id,l.user_id FROM platform.migration_jobs j
	 JOIN platform.group_database_access g ON g.database_id=j.database_id
	 JOIN platform.group_leaders l ON l.group_id=g.group_id
	 JOIN platform.users u ON u.id=l.user_id
	 WHERE j.database_id=$1 AND j.id=$2 AND l.group_id=$3 AND l.user_id=$4 AND u.is_active
	 AND (j.status='QUEUED' OR (j.status='RUNNING' AND j.cancel_waiting)) AND j.retry_count=$5
	 FOR SHARE OF j,g,l,u
	), saved AS (INSERT INTO platform.job_approvals(job_id,group_id,leader_id)
	 SELECT id,group_id,user_id FROM eligible ON CONFLICT DO NOTHING)
	 SELECT EXISTS(SELECT 1 FROM eligible)`, id, job, group, actor, round).Scan(&allowed)
	if err != nil {
		writeJobError(w, ErrJobStorage)
		return
	}
	if !allowed {
		slog.WarnContext(ctx, "migration.job.approve", "status", "denied", "job_id", job, "group_id", group)
		writeJobError(w, access.ErrDatabaseAccessDenied)
		return
	}
	slog.InfoContext(ctx, "migration.job.approve", "status", "approved", "job_id", job, "group_id", group, "retry_count", round)
	w.WriteHeader(http.StatusNoContent)
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
	if err := h.service.access.RequireEdit(r.Context(), databaseID); err != nil {
		writeJobError(w, err)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		writeJobError(w, &InputError{"tepat satu header Idempotency-Key wajib disertakan"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, httpapi.MaxRequestBytes)
	var req struct {
		JobSubmission
		PrepareCancellation bool `json:"prepare_cancellation"`
	}
	if !httpapi.DecodeJSON(w, r, &req) {
		return
	}
	if req.PrepareCancellation {
		if req.BatchSize <= 0 || req.DownSQL != "" || batchRestoreSQL(req.QuerySQL) == "" {
			writeJobError(w, &InputError{"pemulihan hanya tersedia untuk template UPDATE batch"})
			return
		}
		req.DownSQL = batchRestoreSQL(req.QuerySQL)
	}
	j, err := h.service.SubmitJob(r.Context(), databaseID, r.Header.Get("Idempotency-Key"), req.JobSubmission)
	if err != nil {
		slog.WarnContext(r.Context(), "migration.job.submit", "status", "rejected", "error", err.Error())
		writeJobError(w, err)
		return
	}
	slog.InfoContext(r.Context(), "migration.job.submit", "status", "queued", "job_id", j.ID, "version", j.Submission.Version,
		"operation", j.Submission.Operation, "source_job", j.Submission.SourceJob, "batch_size", j.Submission.BatchSize)
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
	case errors.Is(err, ErrCancelConflict):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "cancel_conflict", Message: err.Error()})
	case errors.Is(err, ErrRetryConflict):
		httpapi.WriteJSON(w, 409, nil, &httpapi.Error{Code: "retry_conflict", Message: ErrRetryConflict.Error()})
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
