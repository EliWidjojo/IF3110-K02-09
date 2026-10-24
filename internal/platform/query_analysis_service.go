package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

type QueryAnalysisInput struct {
	Query   string `json:"query"`
	Analyze bool   `json:"analyze"`
}

func (QueryAnalysisInput) String() string   { return "[SQL analisis disembunyikan]" }
func (QueryAnalysisInput) GoString() string { return "[SQL analisis disembunyikan]" }

type QueryPlanNode struct {
	NodeType      string          `json:"node_type"`
	Relation      string          `json:"relation,omitempty"`
	Index         string          `json:"index,omitempty"`
	Join          string          `json:"join,omitempty"`
	StartupCost   *string         `json:"startup_cost"`
	TotalCost     *string         `json:"total_cost"`
	EstimatedRows *string         `json:"estimated_rows"`
	ActualRows    *string         `json:"actual_rows"`
	ActualTime    *string         `json:"actual_time_ms"`
	Children      []QueryPlanNode `json:"children"`
}
type QueryAnalysisResult struct {
	Analyze       bool           `json:"analyze"`
	StatementType string         `json:"statement_type"`
	Plan          *QueryPlanNode `json:"plan"`
	PlanningTime  *string        `json:"planning_time_ms"`
	ExecutionTime *string        `json:"execution_time_ms"`
	RawJSON       string         `json:"raw_json"`
}
type QueryAnalysisService struct {
	access DatabasePermissionChecker
	pools  *TargetConnectionManager
}

func NewQueryAnalysisService(access DatabasePermissionChecker, pools *TargetConnectionManager) *QueryAnalysisService {
	return &QueryAnalysisService{access, pools}
}

func (s *QueryAnalysisService) Analyze(ctx context.Context, databaseID int, input QueryAnalysisInput) (QueryAnalysisResult, error) {
	if databaseID <= 0 {
		return QueryAnalysisResult{}, ErrInvalidViewerInput
	}
	if s == nil || s.access == nil || s.pools == nil {
		return QueryAnalysisResult{}, ErrViewerDependencyUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.access.RequireEdit(ctx, databaseID); err != nil {
		return QueryAnalysisResult{}, err
	}
	validated, err := validateAnalysis(ctx, input.Query, input.Analyze)
	if err != nil {
		return QueryAnalysisResult{}, err
	}
	db, err := s.pools.Get(ctx, databaseID)
	if err != nil {
		return QueryAnalysisResult{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueryAnalysisResult{}, analysisTargetError(ctx, err)
	}
	// Analisis tidak pernah commit dan tidak menulis riwayat migration, termasuk ketika pembacaan hasil gagal.
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_catalog.set_config('statement_timeout','8000',true), pg_catalog.set_config('lock_timeout','1500',true), pg_catalog.set_config('idle_in_transaction_session_timeout','10000',true), pg_catalog.set_config('search_path','pg_catalog,public',true)`); err != nil {
		return QueryAnalysisResult{}, analysisTargetError(ctx, err)
	}
	if input.Analyze {
		for _, relation := range validated.relations {
			if err := validateAnalyzeRelation(ctx, tx, relation); err != nil {
				return QueryAnalysisResult{}, err
			}
		}
	}
	options := "EXPLAIN (FORMAT JSON, COSTS TRUE) "
	if input.Analyze {
		options = "EXPLAIN (ANALYZE TRUE, FORMAT JSON, COSTS TRUE, TIMING TRUE, BUFFERS TRUE) "
	}
	// SQL hanya diteruskan setelah AST membuktikan ada satu statement yang diizinkan dan hak edit sudah diperiksa.
	var raw string
	if err = tx.QueryRowContext(ctx, options+input.Query).Scan(&raw); err != nil {
		return QueryAnalysisResult{}, analysisTargetError(ctx, err)
	}
	result, err := decodeQueryPlan(raw)
	if err != nil {
		return QueryAnalysisResult{}, ErrAnalysisQuery
	}
	result.Analyze = input.Analyze
	result.StatementType = strings.TrimSuffix(validated.kind, "Stmt")
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return QueryAnalysisResult{}, analysisTargetError(ctx, err)
	}
	return result, nil
}

// ANALYZE menolak objek yang dapat menyembunyikan eksekusi fungsi, foreign access, atau aturan pada saat SELECT.
func validateAnalyzeRelation(ctx context.Context, tx *sql.Tx, relation analysisRelation) error {
	// Lock identitas relasi selama pemeriksaan dan ANALYZE agar DROP atau penggantian menjadi view tidak menyelinap di antaranya.
	if _, err := tx.ExecContext(ctx, "LOCK TABLE ONLY "+pq.QuoteIdentifier(relation.schema)+"."+pq.QuoteIdentifier(relation.table)+" IN ACCESS SHARE MODE"); err != nil {
		return analysisTargetError(ctx, err)
	}
	const query = `SELECT c.relkind='r' AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity
		AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_inherits WHERE inhparent=c.oid)
		AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite WHERE ev_class=c.oid)
		AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid=c.oid AND (indexprs IS NOT NULL OR indpred IS NOT NULL))
		AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_type t ON t.oid=a.atttypid JOIN pg_catalog.pg_namespace tn ON tn.oid=t.typnamespace
		 WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND (a.attgenerated<>'' OR t.typtype<>'b' OR tn.nspname<>'pg_catalog' OR t.typname NOT IN ('int2','int4','int8','numeric','float4','float8','bool','text','varchar','bpchar','date','time','timetz','timestamp','timestamptz','uuid','json','jsonb','bytea')))
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`
	var safe bool
	if err := tx.QueryRowContext(ctx, query, relation.schema, relation.table).Scan(&safe); errors.Is(err, sql.ErrNoRows) {
		return ErrViewerTableNotFound
	} else if err != nil {
		return analysisTargetError(ctx, err)
	}
	if !safe {
		return ErrUnsafeAnalyze
	}
	return nil
}

func analysisTargetError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	safe := targetConnectionError("menganalisis rencana query", err)
	var target *TargetConnectionError
	if errors.As(safe, &target) && (target.SQLState == "57014" || target.SQLState == "55P03") {
		return context.DeadlineExceeded
	}
	return errors.Join(ErrAnalysisQuery, safe)
}

func decodeQueryPlan(raw string) (QueryAnalysisResult, error) {
	var roots []map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&roots); err != nil || len(roots) == 0 {
		return QueryAnalysisResult{}, ErrAnalysisQuery
	}
	result := QueryAnalysisResult{RawJSON: raw, PlanningTime: planNumber(roots[0], "Planning Time"), ExecutionTime: planNumber(roots[0], "Execution Time")}
	var convert func(map[string]any, int) *QueryPlanNode
	convert = func(node map[string]any, depth int) *QueryPlanNode {
		if depth > 128 {
			return nil
		}
		text := func(key string) string { value, _ := node[key].(string); return value }
		result := &QueryPlanNode{NodeType: text("Node Type"), Relation: text("Relation Name"), Index: text("Index Name"), Join: text("Join Type"), StartupCost: planNumber(node, "Startup Cost"), TotalCost: planNumber(node, "Total Cost"), EstimatedRows: planNumber(node, "Plan Rows"), ActualRows: planNumber(node, "Actual Rows"), ActualTime: planNumber(node, "Actual Total Time"), Children: []QueryPlanNode{}}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			if data, ok := child.(map[string]any); ok {
				if converted := convert(data, depth+1); converted != nil {
					result.Children = append(result.Children, *converted)
				}
			}
		}
		return result
	}
	if plan, ok := roots[0]["Plan"].(map[string]any); ok {
		result.Plan = convert(plan, 0)
	}
	return result, nil
}
func planNumber(data map[string]any, key string) *string {
	value, ok := data[key].(json.Number)
	if !ok {
		return nil
	}
	text := value.String()
	return &text
}
