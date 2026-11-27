import { useState, type FormEvent } from "react";
import { useParams, useSearchParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, type PlanNode } from "../api";
import { Card, DatabasePicker, ErrorNotice, Notice, PageHeader, useSelectedDatabase } from "../ui";

export function AnalysisPage() {
  const { dbId } = useParams();
  const [params] = useSearchParams();
  const { id, database, canEdit } = useSelectedDatabase(dbId);
  const [sql, setSql] = useState(params.get("sql") ?? "");
  const [analyze, setAnalyze] = useState(false);
  const run = useMutation({ mutationFn: () => api.explain(id!, sql, analyze) });
  const supported = canEdit && database?.driver === "postgres";

  return (
    <>
      <PageHeader title="Analisis execution plan" description="Tinjau rencana eksekusi PostgreSQL sebelum mengajukan migration. Memerlukan View + Edit." />
      <Card>
        <DatabasePicker basePath="/analysis" selected={id} filter={d => d.driver === "postgres"} />
        {database && !supported && <Notice tone="warning">Analisis hanya tersedia untuk database PostgreSQL dengan akses View + Edit.</Notice>}
        <form
          className="stack"
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            run.mutate();
          }}
        >
          <label>
            SQL
            <textarea className="code" rows={8} spellCheck={false} required value={sql} onChange={e => setSql(e.target.value)} placeholder="SELECT * FROM public.nama_table WHERE id = 1" />
          </label>
          <label className="check">
            <input type="checkbox" checked={analyze} onChange={e => setAnalyze(e.target.checked)} />
            Sertakan EXPLAIN ANALYZE (benar-benar menjalankan SELECT terbatas)
          </label>
          <p className="muted small">EXPLAIN mendukung satu SELECT/INSERT/UPDATE/DELETE/MERGE, maksimum 12 KiB. DDL tidak didukung.</p>
          <div className="actions">
            <button className="primary" type="submit" disabled={!supported || run.isPending}>
              {run.isPending ? "Menganalisis…" : "Analisis query"}
            </button>
          </div>
        </form>
        <ErrorNotice error={run.error} />
      </Card>
      {run.data && (
        <Card title={`Plan ${run.data.statement_type}${run.data.analyze ? " · ANALYZE" : ""}`}>
          <p className="muted">
            Planning: {run.data.planning_time_ms ?? "—"} ms{run.data.execution_time_ms ? ` · Eksekusi: ${run.data.execution_time_ms} ms` : ""}
          </p>
          {run.data.plan && (
            <ul className="plan-tree">
              <PlanItem node={run.data.plan} />
            </ul>
          )}
          <details>
            <summary>JSON mentah</summary>
            <pre className="code">{run.data.raw_json}</pre>
          </details>
        </Card>
      )}
    </>
  );
}

function PlanItem({ node }: { node: PlanNode }) {
  const target = [node.relation && `on ${node.relation}`, node.index && `index ${node.index}`, node.join && `${node.join} join`].filter(Boolean).join(" · ");
  return (
    <li>
      <div className="plan-node">
        <strong>{node.node_type}</strong> {target && <span className="muted">{target}</span>}
        <span className="plan-metrics">
          cost {node.startup_cost ?? "?"}..{node.total_cost ?? "?"} · rows {node.estimated_rows ?? "?"}
          {node.actual_rows !== null && ` · actual rows ${node.actual_rows}`}
          {node.actual_time_ms !== null && ` · ${node.actual_time_ms} ms`}
        </span>
      </div>
      {!!node.children?.length && (
        <ul>
          {node.children.map((child, i) => (
            <PlanItem key={i} node={child} />
          ))}
        </ul>
      )}
    </li>
  );
}
