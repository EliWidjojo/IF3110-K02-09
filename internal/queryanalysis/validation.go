package queryanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"IF3110-K02-09/internal/viewer"

	pgparser "github.com/wasilibs/go-pgquery/parser"
)

var (
	ErrInvalidAnalysis     = errors.New("SQL harus berupa satu statement PostgreSQL yang valid")
	ErrUnsupportedAnalysis = errors.New("EXPLAIN mendukung SELECT, INSERT, UPDATE, DELETE, dan MERGE; DDL serta kontrol transaksi tidak didukung")
	ErrUnsafeAnalyze       = errors.New("ANALYZE hanya mendukung SELECT sederhana tanpa fungsi, cast, CTE, atau penguncian pada tabel biasa yang memenuhi kebijakan")
	ErrAnalysisQuery       = errors.New("analisis gagal; periksa SQL, nama objek, izin akun target, dan batas waktu")
)

const maxAnalysisSQLBytes = 12 * 1024

var analysisParserGate = make(chan struct{}, 1)

type analysisRelation struct{ schema, table string }
type validatedAnalysis struct {
	kind      string
	relations []analysisRelation
}

// Parser PostgreSQL asli berbasis Wasm membedakan statement, literal, komentar, dan dollar-quote tanpa pemisahan titik koma buatan.
func validateAnalysis(ctx context.Context, query string, analyze bool) (validatedAnalysis, error) {
	if len(query) == 0 || len(query) > maxAnalysisSQLBytes || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return validatedAnalysis{}, ErrInvalidAnalysis
	}
	select {
	case analysisParserGate <- struct{}{}:
		defer func() { <-analysisParserGate }()
	case <-ctx.Done():
		return validatedAnalysis{}, ctx.Err()
	}
	encoded, err := pgparser.ParseToJSON(query)
	if err != nil {
		return validatedAnalysis{}, ErrInvalidAnalysis
	}
	if err := ctx.Err(); err != nil {
		return validatedAnalysis{}, err
	}
	var tree struct {
		Stmts []struct {
			Stmt map[string]any `json:"stmt"`
		} `json:"stmts"`
	}
	if json.Unmarshal([]byte(encoded), &tree) != nil || len(tree.Stmts) != 1 {
		return validatedAnalysis{}, ErrInvalidAnalysis
	}
	result := validatedAnalysis{}
	for key := range tree.Stmts[0].Stmt {
		result.kind = key
	}
	switch result.kind {
	case "SelectStmt", "InsertStmt", "UpdateStmt", "DeleteStmt", "MergeStmt":
	default:
		return validatedAnalysis{}, ErrUnsupportedAnalysis
	}
	if !analyze {
		return result, nil
	}
	if result.kind != "SelectStmt" {
		return validatedAnalysis{}, ErrUnsafeAnalyze
	}
	allowed := map[string]bool{"SelectStmt": true, "ResTarget": true, "RangeVar": true, "ColumnRef": true, "String": true, "A_Const": true, "A_Star": true, "A_Expr": true, "BoolExpr": true, "NullTest": true, "BooleanTest": true, "JoinExpr": true, "SortBy": true, "List": true, "Integer": true, "Float": true, "Boolean": true, "RangeSubselect": true, "SubLink": true}
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth > 128 {
			return ErrUnsafeAnalyze
		}
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, child := range item {
				if len(key) > 0 && key[0] >= 'A' && key[0] <= 'Z' && !allowed[key] {
					return ErrUnsafeAnalyze
				}
				if key == "intoClause" || key == "lockingClause" || key == "withClause" {
					return ErrUnsafeAnalyze
				}
				if key == "RangeVar" {
					rangeVar, ok := child.(map[string]any)
					if !ok {
						return ErrUnsafeAnalyze
					}
					schema, _ := rangeVar["schemaname"].(string)
					table, _ := rangeVar["relname"].(string)
					if !viewer.ValidIdentifier(schema) || !viewer.ValidIdentifier(table) || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
						return ErrUnsafeAnalyze
					}
					result.relations = append(result.relations, analysisRelation{schema, table})
				}
				if key == "A_Expr" {
					expr, ok := child.(map[string]any)
					if !ok {
						return ErrUnsafeAnalyze
					}
					kind, _ := expr["kind"].(string)
					if kind != "AEXPR_OP" && kind != "AEXPR_IN" && kind != "AEXPR_BETWEEN" && kind != "AEXPR_NOT_BETWEEN" {
						return ErrUnsafeAnalyze
					}
					names, _ := expr["name"].([]any)
					if len(names) != 1 {
						return ErrUnsafeAnalyze
					}
					name, _ := names[0].(map[string]any)
					node, _ := name["String"].(map[string]any)
					operator, _ := node["sval"].(string)
					switch operator {
					case "=", "<", ">", "<=", ">=", "<>", "!=", "+", "-", "*", "/", "%", "~~", "!~~", "~~*", "!~~*":
					default:
						return ErrUnsafeAnalyze
					}
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(tree.Stmts[0].Stmt, 0); err != nil {
		return validatedAnalysis{}, err
	}
	return result, nil
}
