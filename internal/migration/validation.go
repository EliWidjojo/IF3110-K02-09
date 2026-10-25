package migration

import (
	"context"
	"encoding/json"
	"errors"
	pgparser "github.com/wasilibs/go-pgquery/parser"
	"strings"
)

var parserGate = make(chan struct{}, 1)

func validatePostgresMigration(ctx context.Context, query string) error {
	select {
	case parserGate <- struct{}{}:
		defer func() { <-parserGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	raw, err := pgparser.ParseToJSON(query)
	if err != nil {
		return errors.New("sintaks PostgreSQL tidak valid")
	}
	var tree struct {
		Stmts []struct {
			Stmt map[string]any `json:"stmt"`
		} `json:"stmts"`
	}
	if json.Unmarshal([]byte(raw), &tree) != nil || len(tree.Stmts) == 0 {
		return errors.New("SQL migration kosong")
	}
	allowed := map[string]bool{"CreateStmt": true, "AlterTableStmt": true, "IndexStmt": true, "DropStmt": true, "TruncateStmt": true, "CommentStmt": true, "RenameStmt": true, "InsertStmt": true, "UpdateStmt": true, "DeleteStmt": true, "CreateSeqStmt": true, "AlterSeqStmt": true, "CreateEnumStmt": true, "AlterEnumStmt": true, "ViewStmt": true}
	for _, stmt := range tree.Stmts {
		for kind := range stmt.Stmt {
			if !allowed[kind] {
				return errors.New("jenis statement migration tidak didukung")
			}
		}
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if k == "FuncCall" {
					m, _ := c.(map[string]any)
					parts, _ := m["funcname"].([]any)
					for _, part := range parts {
						p, _ := part.(map[string]any)
						node, _ := p["String"].(map[string]any)
						name, _ := node["sval"].(string)
						switch strings.ToLower(name) {
						case "set_config", "complete_attempt", "pg_terminate_backend", "pg_cancel_backend":
							return false
						}
					}
				}
				if !walk(c) {
					return false
				}
			}
		case []any:
			for _, c := range x {
				if !walk(c) {
					return false
				}
			}
		}
		return true
	}
	// Pohon JSON diperiksa tanpa mengubah SQL asli yang disimpan dan dieksekusi.
	for _, stmt := range tree.Stmts {
		if !walk(stmt.Stmt) {
			return errors.New("fungsi pengaturan session atau pencatat internal tidak didukung")
		}
	}
	return nil
}
