package migration

// Pelaksana dan izin tidak menjadi bagian payload karena berasal dari session backend.
type MigrationRequest struct {
	Version      int    `json:"version"`
	QuerySQL     string `json:"query_sql"`
	NonAtomicDDL bool   `json:"non_atomic_ddl"`
}
