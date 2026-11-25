package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"IF3110-K02-09/internal/admin"
	"IF3110-K02-09/internal/database"
)

var (
	ErrProposalNotFound    = errors.New("proposal migration tidak ditemukan")
	ErrInvalidState        = errors.New("status proposal tidak valid untuk aksi ini")
	ErrSelfApprovalBlocked = errors.New("pembuat migration tidak dapat menyetujui pengajuannya sendiri")
	ErrChecksumMismatch    = errors.New("isi SQL berubah setelah disetujui (checksum mismatch)")
	ErrMissingRollbackSQL  = errors.New("rollback_sql wajib diisi untuk pengajuan migration")
)

// AsyncDispatcher adalah kontrak integrasi dengan modul Asynchronous Processing milik Nate.
type AsyncDispatcher interface {
	EnqueueMigration(ctx context.Context, databaseID int, proposalID int64, direction MigrationDirection) error
}

// EnsureWorkflowTables membuat tabel proposals pada audit schema jika belum tersedia.
func ensureWorkflowTables(ctx context.Context, control *sql.Conn, cfg database.DatabaseConnection) error {
	schema := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName))
	var ddl string
	if cfg.Driver.IsMySQL() {
		ddl = `CREATE TABLE IF NOT EXISTS ` + schema + `.proposals (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			version INT NOT NULL,
			description VARCHAR(255) NOT NULL DEFAULT '',
			query_sql MEDIUMTEXT NOT NULL,
			rollback_sql MEDIUMTEXT NOT NULL,
			non_atomic_ddl BOOLEAN NOT NULL DEFAULT FALSE,
			checksum VARCHAR(64) NOT NULL,
			status VARCHAR(32) NOT NULL,
			created_by INT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			reviewed_by INT NULL,
			reviewed_at TIMESTAMP NULL,
			rejection_reason TEXT NULL,
			attempt_id BIGINT NULL
		)`
	} else {
		ddl = `CREATE TABLE IF NOT EXISTS ` + schema + `.proposals (
			id BIGSERIAL PRIMARY KEY,
			version INT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			query_sql TEXT NOT NULL,
			rollback_sql TEXT NOT NULL,
			non_atomic_ddl BOOLEAN NOT NULL DEFAULT FALSE,
			checksum VARCHAR(64) NOT NULL,
			status VARCHAR(32) NOT NULL,
			created_by INT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			reviewed_by INT NULL,
			reviewed_at TIMESTAMPTZ NULL,
			rejection_reason TEXT NULL,
			attempt_id BIGINT NULL
		)`
	}
	_, err := control.ExecContext(ctx, ddl)
	return err
}

func computeChecksum(version int, querySQL, rollbackSQL string, nonAtomic bool) string {
	raw := fmt.Sprintf("%d|%s|%s|%t", version, strings.TrimSpace(querySQL), strings.TrimSpace(rollbackSQL), nonAtomic)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SubmitProposal mengajukan migration baru ke status PENDING_APPROVAL.
func (e *Engine) SubmitProposal(ctx context.Context, dbID int, req MigrationRequest) (MigrationProposal, error) {
	var out MigrationProposal
	if err := e.access.RequireEdit(ctx, dbID); err != nil {
		return out, err
	}
	user, err := e.identity.VerifiedUserID(ctx)
	if err != nil || user <= 0 {
		return out, admin.ErrUnauthenticated
	}
	if req.Version <= 0 || req.Version > 2147483647 || len(req.QuerySQL) == 0 || len(req.QuerySQL) > 12288 {
		return out, &InputError{"versi atau ukuran query_sql tidak valid"}
	}
	if strings.TrimSpace(req.RollbackSQL) == "" || len(req.RollbackSQL) > 12288 {
		return out, ErrMissingRollbackSQL
	}

	cfg, err := e.pools.Configuration(ctx, dbID)
	if err != nil {
		return out, err
	}
	if _, err := database.Statements(req.QuerySQL, cfg.Driver, req.NonAtomicDDL); err != nil {
		return out, errors.Join(ErrInvalidMigration, err)
	}
	if _, err := database.Statements(req.RollbackSQL, cfg.Driver, req.NonAtomicDDL); err != nil {
		return out, errors.Join(ErrInvalidMigration, err)
	}
	if cfg.Driver == database.Postgres {
		if err = validatePostgresMigration(ctx, req.QuerySQL); err != nil {
			return out, errors.Join(ErrInvalidMigration, err)
		}
		if err = validatePostgresMigration(ctx, req.RollbackSQL); err != nil {
			return out, errors.Join(ErrInvalidMigration, err)
		}
	}

	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer audit.Close()

	control, err := audit.Conn(ctx)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer control.Close()

	if err := ensureWorkflowTables(ctx, control, cfg); err != nil {
		return out, ErrUnsafeTarget
	}

	checksum := computeChecksum(req.Version, req.QuerySQL, req.RollbackSQL, req.NonAtomicDDL)
	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	query := "INSERT INTO " + table + "(version,description,query_sql,rollback_sql,non_atomic_ddl,checksum,status,created_by) VALUES(" +
		cfg.Driver.Bind(1) + "," + cfg.Driver.Bind(2) + "," + cfg.Driver.Bind(3) + "," + cfg.Driver.Bind(4) + "," +
		cfg.Driver.Bind(5) + "," + cfg.Driver.Bind(6) + ",'PENDING_APPROVAL'," + cfg.Driver.Bind(7) + ")"

	var proposalID int64
	if cfg.Driver.IsMySQL() {
		res, err := control.ExecContext(ctx, query, req.Version, req.Description, req.QuerySQL, req.RollbackSQL, req.NonAtomicDDL, checksum, user)
		if err != nil {
			return out, ErrUnsafeTarget
		}
		proposalID, _ = res.LastInsertId()
	} else {
		if err := control.QueryRowContext(ctx, query+" RETURNING id", req.Version, req.Description, req.QuerySQL, req.RollbackSQL, req.NonAtomicDDL, checksum, user).Scan(&proposalID); err != nil {
			return out, ErrUnsafeTarget
		}
	}

	return e.GetProposal(ctx, dbID, proposalID)
}

// ApproveProposal menyetujui pengajuan migration (hanya oleh reviewer berbeda).
func (e *Engine) ApproveProposal(ctx context.Context, dbID int, proposalID int64) (MigrationProposal, error) {
	var out MigrationProposal
	if err := e.access.RequireEdit(ctx, dbID); err != nil {
		return out, err
	}
	reviewerID, err := e.identity.VerifiedUserID(ctx)
	if err != nil || reviewerID <= 0 {
		return out, admin.ErrUnauthenticated
	}

	prop, err := e.GetProposal(ctx, dbID, proposalID)
	if err != nil {
		return out, err
	}
	if prop.Status != StatusPendingApproval {
		return out, ErrInvalidState
	}
	if prop.CreatedBy == reviewerID {
		return out, ErrSelfApprovalBlocked
	}

	expectedChecksum := computeChecksum(prop.Version, prop.QuerySQL, prop.RollbackSQL, prop.NonAtomicDDL)
	if prop.Checksum != expectedChecksum {
		return out, ErrChecksumMismatch
	}

	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer audit.Close()

	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	q := "UPDATE " + table + " SET status='APPROVED', reviewed_by=" + cfg.Driver.Bind(1) + ", reviewed_at=CURRENT_TIMESTAMP WHERE id=" + cfg.Driver.Bind(2) + " AND status='PENDING_APPROVAL'"
	res, err := audit.ExecContext(ctx, q, reviewerID, proposalID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return out, ErrInvalidState
	}

	return e.GetProposal(ctx, dbID, proposalID)
}

// RejectProposal menolak pengajuan migration beserta alasan penolakan.
func (e *Engine) RejectProposal(ctx context.Context, dbID int, proposalID int64, reason string) (MigrationProposal, error) {
	var out MigrationProposal
	if err := e.access.RequireEdit(ctx, dbID); err != nil {
		return out, err
	}
	reviewerID, err := e.identity.VerifiedUserID(ctx)
	if err != nil || reviewerID <= 0 {
		return out, admin.ErrUnauthenticated
	}
	if strings.TrimSpace(reason) == "" {
		return out, &InputError{"alasan penolakan wajib diisi"}
	}

	prop, err := e.GetProposal(ctx, dbID, proposalID)
	if err != nil {
		return out, err
	}
	if prop.Status != StatusPendingApproval {
		return out, ErrInvalidState
	}

	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer audit.Close()

	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	q := "UPDATE " + table + " SET status='REJECTED', reviewed_by=" + cfg.Driver.Bind(1) + ", reviewed_at=CURRENT_TIMESTAMP, rejection_reason=" + cfg.Driver.Bind(2) + " WHERE id=" + cfg.Driver.Bind(3) + " AND status='PENDING_APPROVAL'"
	res, err := audit.ExecContext(ctx, q, reviewerID, reason, proposalID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return out, ErrInvalidState
	}

	return e.GetProposal(ctx, dbID, proposalID)
}

// QueueApprovedProposal mengirim proposal berstatus APPROVED ke async worker Nate (atau mengeksekusinya jika dispatcher nil).
func (e *Engine) QueueApprovedProposal(ctx context.Context, dbID int, proposalID int64) (MigrationProposal, error) {
	var out MigrationProposal
	if err := e.access.RequireEdit(ctx, dbID); err != nil {
		return out, err
	}

	prop, err := e.GetProposal(ctx, dbID, proposalID)
	if err != nil {
		return out, err
	}
	if prop.Status != StatusApproved {
		return out, ErrInvalidState
	}

	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return out, ErrUnsafeTarget
	}
	defer audit.Close()

	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	if e.dispatcher != nil {
		if _, err := audit.ExecContext(ctx, "UPDATE "+table+" SET status='QUEUED' WHERE id="+cfg.Driver.Bind(1), proposalID); err != nil {
			return out, ErrUnsafeTarget
		}
		if err := e.dispatcher.EnqueueMigration(ctx, dbID, proposalID, DirectionForward); err != nil {
			_, _ = audit.ExecContext(ctx, "UPDATE "+table+" SET status='APPROVED' WHERE id="+cfg.Driver.Bind(1), proposalID)
			return out, err
		}
		return e.GetProposal(ctx, dbID, proposalID)
	}

	// Fallback eksekusi langsung jika worker async belum terpasang
	res, runErr := e.Run(ctx, dbID, MigrationRequest{
		Version:      prop.Version,
		Description:  prop.Description,
		QuerySQL:     prop.QuerySQL,
		RollbackSQL:  prop.RollbackSQL,
		NonAtomicDDL: prop.NonAtomicDDL,
	})
	finalStatus := StatusExecuted
	if runErr != nil {
		finalStatus = StatusFailed
	}
	_, _ = audit.ExecContext(ctx, "UPDATE "+table+" SET status="+cfg.Driver.Bind(1)+", attempt_id="+cfg.Driver.Bind(2)+" WHERE id="+cfg.Driver.Bind(3), string(finalStatus), res.AttemptID, proposalID)

	updated, _ := e.GetProposal(ctx, dbID, proposalID)
	return updated, runErr
}

func (e *Engine) GetProposal(ctx context.Context, dbID int, proposalID int64) (MigrationProposal, error) {
	var p MigrationProposal
	if err := e.access.RequireRead(ctx, dbID); err != nil {
		return p, err
	}
	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return p, ErrUnsafeTarget
	}
	defer audit.Close()

	control, err := audit.Conn(ctx)
	if err != nil {
		return p, ErrUnsafeTarget
	}
	defer control.Close()
	_ = ensureWorkflowTables(ctx, control, cfg)

	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	q := "SELECT id,version,description,query_sql,rollback_sql,non_atomic_ddl,checksum,status,created_by,created_at,reviewed_by,reviewed_at,COALESCE(rejection_reason,''),attempt_id FROM " + table + " WHERE id=" + cfg.Driver.Bind(1)
	var statusStr string
	err = control.QueryRowContext(ctx, q, proposalID).Scan(
		&p.ID, &p.Version, &p.Description, &p.QuerySQL, &p.RollbackSQL,
		&p.NonAtomicDDL, &p.Checksum, &statusStr, &p.CreatedBy, &p.CreatedAt,
		&p.ReviewedBy, &p.ReviewedAt, &p.RejectionReason, &p.AttemptID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrProposalNotFound
	}
	if err != nil {
		return p, ErrUnsafeTarget
	}
	p.DatabaseID = dbID
	p.Status = ProposalStatus(statusStr)
	return p, nil
}

func (e *Engine) ListProposals(ctx context.Context, dbID int, statusFilter string, limit, offset int) ([]MigrationProposal, error) {
	if err := e.access.RequireRead(ctx, dbID); err != nil {
		return nil, err
	}
	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	defer audit.Close()

	control, err := audit.Conn(ctx)
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	defer control.Close()
	_ = ensureWorkflowTables(ctx, control, cfg)

	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	var rows *sql.Rows
	if statusFilter != "" {
		q := "SELECT id,version,description,query_sql,rollback_sql,non_atomic_ddl,checksum,status,created_by,created_at,reviewed_by,reviewed_at,COALESCE(rejection_reason,''),attempt_id FROM " +
			table + " WHERE status=" + cfg.Driver.Bind(1) + " ORDER BY id DESC LIMIT " + cfg.Driver.Bind(2) + " OFFSET " + cfg.Driver.Bind(3)
		rows, err = control.QueryContext(ctx, q, statusFilter, limit, offset)
	} else {
		q := "SELECT id,version,description,query_sql,rollback_sql,non_atomic_ddl,checksum,status,created_by,created_at,reviewed_by,reviewed_at,COALESCE(rejection_reason,''),attempt_id FROM " +
			table + " ORDER BY id DESC LIMIT " + cfg.Driver.Bind(1) + " OFFSET " + cfg.Driver.Bind(2)
		rows, err = control.QueryContext(ctx, q, limit, offset)
	}
	if err != nil {
		return nil, ErrUnsafeTarget
	}
	defer rows.Close()

	out := []MigrationProposal{}
	for rows.Next() {
		var p MigrationProposal
		var statusStr string
		if err := rows.Scan(
			&p.ID, &p.Version, &p.Description, &p.QuerySQL, &p.RollbackSQL,
			&p.NonAtomicDDL, &p.Checksum, &statusStr, &p.CreatedBy, &p.CreatedAt,
			&p.ReviewedBy, &p.ReviewedAt, &p.RejectionReason, &p.AttemptID,
		); err != nil {
			return nil, ErrUnsafeTarget
		}
		p.DatabaseID = dbID
		p.Status = ProposalStatus(statusStr)
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkProposalOutcome dipanggil oleh worker Nate setelah eksekusi async selesai.
func (e *Engine) MarkProposalOutcome(ctx context.Context, dbID int, proposalID int64, status ProposalStatus, attemptID int64) error {
	audit, cfg, err := e.pools.OpenAudit(ctx, dbID)
	if err != nil {
		return ErrUnsafeTarget
	}
	defer audit.Close()
	table := cfg.Driver.Quote(HistorySchema(cfg.Driver, cfg.DatabaseName)) + ".proposals"
	_, err = audit.ExecContext(ctx, "UPDATE "+table+" SET status="+cfg.Driver.Bind(1)+", attempt_id="+cfg.Driver.Bind(2)+" WHERE id="+cfg.Driver.Bind(3), string(status), attemptID, proposalID)
	return err
}

// Dummy time reference guard if needed
var _ = time.Now