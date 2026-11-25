package migration

import "time"

type ProposalStatus string

const (
	StatusPendingApproval ProposalStatus = "PENDING_APPROVAL"
	StatusApproved        ProposalStatus = "APPROVED"
	StatusRejected        ProposalStatus = "REJECTED"
	StatusQueued          ProposalStatus = "QUEUED"
	StatusExecuted        ProposalStatus = "EXECUTED"
	StatusRolledBack      ProposalStatus = "ROLLED_BACK"
	StatusFailed          ProposalStatus = "FAILED"
)

type MigrationDirection string

const (
	DirectionForward  MigrationDirection = "FORWARD"
	DirectionRollback MigrationDirection = "ROLLBACK"
)

// Pelaksana dan izin tidak menjadi bagian payload karena berasal dari session backend.
type MigrationRequest struct {
	Version      int    `json:"version"`
	Description  string `json:"description"`
	QuerySQL     string `json:"query_sql"`
	RollbackSQL  string `json:"rollback_sql"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}

type ApprovalActionRequest struct {
	Reason string `json:"reason,omitempty"`
}

type RollbackRequest struct {
	AttemptID    int64  `json:"attempt_id"`
	RollbackSQL  string `json:"rollback_sql,omitempty"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}

type MigrationProposal struct {
	ID              int64          `json:"id"`
	DatabaseID      int            `json:"database_id"`
	Version         int            `json:"version"`
	Description     string         `json:"description"`
	QuerySQL        string         `json:"query_sql"`
	RollbackSQL     string         `json:"rollback_sql"`
	NonAtomicDDL    bool           `json:"non_atomic_ddl"`
	Checksum        string         `json:"checksum"`
	Status          ProposalStatus `json:"status"`
	CreatedBy       int            `json:"created_by"`
	CreatedAt       time.Time      `json:"created_at"`
	ReviewedBy      *int           `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time     `json:"reviewed_at,omitempty"`
	RejectionReason string         `json:"rejection_reason,omitempty"`
	AttemptID       *int64         `json:"attempt_id,omitempty"`
}