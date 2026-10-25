package queryanalysis

import (
	"context"
	"errors"
	"testing"
)

func TestAnalysisASTPolicies(t *testing.T) {
	for _, tc := range []struct {
		query       string
		analyze, ok bool
	}{{"SELECT 1", true, true}, {"SELECT * FROM public.items WHERE id=1", true, true}, {"SELECT * FROM items", true, false}, {"SELECT pg_sleep(1)", true, false}, {"WITH x AS(SELECT 1) SELECT * FROM x", true, false}, {"SELECT 1::integer", true, false}, {"SELECT * FROM public.items FOR UPDATE", true, false}, {"INSERT INTO public.items VALUES(1)", false, true}, {"DELETE FROM public.items", true, false}, {"MERGE INTO items USING x ON true WHEN MATCHED THEN DELETE", false, true}, {"SELECT 1;SELECT 2", false, false}, {"CREATE TABLE x(id INT)", false, false}, {"-- only comment", false, false}} {
		_, err := validateAnalysis(context.Background(), tc.query, tc.analyze)
		if (err == nil) != tc.ok {
			t.Errorf("kebijakan query tidak sesuai: analyze=%v query=%s err=%v", tc.analyze, tc.query, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validateAnalysis(ctx, "SELECT 1", false); !errors.Is(err, context.Canceled) {
		t.Fatal("pembatalan diabaikan", err)
	}
}
func TestPlanDecodingKeepsPrecisionAndTree(t *testing.T) {
	r, err := decodeQueryPlan(`[{"Plan":{"Node Type":"Hash Join","Total Cost":123456789012345.123,"Plan Rows":9007199254740993,"Plans":[{"Node Type":"Index Scan","Index Name":"pk_items","Actual Rows":2}]},"Execution Time":1.25}]`)
	if err != nil || r.Plan == nil || *r.Plan.EstimatedRows != "9007199254740993" || *r.Plan.TotalCost != "123456789012345.123" || len(r.Plan.Children) != 1 || r.Plan.Children[0].Index != "pk_items" || *r.ExecutionTime != "1.25" {
		t.Fatal("plan berubah", err)
	}
}
