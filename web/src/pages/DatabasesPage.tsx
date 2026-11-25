import { useParams, useSearchParams } from "react-router";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "../api";
import { Card, CellView, DataTable, DatabasePicker, ErrorNotice, PageHeader, Pager, useSelectedDatabase } from "../ui";

const TABS = [
  { id: "data", label: "Data" },
  { id: "columns", label: "Column" },
  { id: "constraints", label: "Constraint" },
  { id: "indexes", label: "Index" },
] as const;

// Seluruh pilihan (schema, table, tab, halaman) disimpan di URL sehingga refresh dan tombol back tetap konsisten.
export function DatabasesPage() {
  const { dbId } = useParams();
  const { id } = useSelectedDatabase(dbId);
  const [params, setParams] = useSearchParams();
  const schema = params.get("schema") ?? "";
  const table = params.get("table") ?? "";
  const tab = params.get("tab") ?? "data";
  const limit = Number(params.get("limit") ?? 50);
  const offset = Number(params.get("offset") ?? 0);
  const update = (next: Record<string, string | number | null>) => {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) v === null || v === "" ? p.delete(k) : p.set(k, String(v));
    setParams(p);
  };

  const schemas = useQuery({ queryKey: ["schemas", id], queryFn: () => api.schemas(id!), enabled: !!id });
  const tables = useQuery({ queryKey: ["tables", id, schema], queryFn: () => api.tables(id!, schema), enabled: !!id && !!schema });
  const meta = useQuery({ queryKey: ["table", id, schema, table], queryFn: () => api.table(id!, schema, table), enabled: !!id && !!schema && !!table });
  const data = useQuery({
    queryKey: ["tableData", id, schema, table, limit, offset],
    queryFn: () => api.tableData(id!, schema, table, limit, offset),
    enabled: !!id && !!schema && !!table,
    placeholderData: keepPreviousData,
  });

  return (
    <>
      <PageHeader title="Database viewer" description="Jelajahi schema, struktur table, dan data database yang dapat Anda akses melalui group." />
      <Card>
        <div className="selectors">
          <DatabasePicker basePath="/databases" selected={id} />
          <label>
            Schema
            <select value={schema} disabled={!schemas.data?.length} onChange={e => update({ schema: e.target.value, table: null, offset: null })}>
              <option value="">{schemas.isFetching ? "Memuat…" : "Pilih schema"}</option>
              {schemas.data?.map(s => (
                <option key={s.name}>{s.name}</option>
              ))}
            </select>
          </label>
          <label>
            Table
            <select value={table} disabled={!tables.data?.length} onChange={e => update({ table: e.target.value, offset: null })}>
              <option value="">{tables.isFetching ? "Memuat…" : "Pilih table"}</option>
              {tables.data?.map(t => (
                <option key={t.name} value={t.name}>
                  {t.name} · {t.type}
                </option>
              ))}
            </select>
          </label>
        </div>
        <ErrorNotice error={schemas.error ?? tables.error} />
        {!id && <p className="muted">Pilih database untuk mulai menjelajah.</p>}
        {id && schemas.data && !schemas.data.length && <p className="muted">Tidak ada schema pengguna pada database ini.</p>}
      </Card>

      {table && (
        <Card title={`${schema}.${table}`}>
          <div className="tabs" role="tablist" aria-label="Informasi table">
            {TABS.map(t => (
              <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} onClick={() => update({ tab: t.id === "data" ? null : t.id })}>
                {t.label}
              </button>
            ))}
          </div>
          <ErrorNotice error={meta.error ?? data.error} />
          {tab === "data" && data.data && (
            <>
              <DataTable
                label="Data table"
                rowKey={(_, i) => i}
                columns={data.data.columns.map((c, index) => ({ label: `${c.name} · ${c.data_type}`, render: (row: unknown[]) => <CellView value={row[index]} /> }))}
                rows={data.data.rows}
                empty="Tidak ada baris pada halaman ini."
              />
              <div className="pager-row">
                <label className="inline">
                  Baris per halaman
                  <select value={limit} onChange={e => update({ limit: e.target.value, offset: null })}>
                    {[25, 50, 100].map(n => (
                      <option key={n}>{n}</option>
                    ))}
                  </select>
                </label>
                <Pager offset={offset} size={limit} count={data.data.pagination.returned_rows} hasMore={data.data.pagination.has_more} onChange={o => update({ offset: o || null })} />
              </div>
              <p className="muted small">
                {data.data.stable_order
                  ? `Urutan primary key: ${data.data.order_by.join(", ")}. Perubahan data bersamaan dapat menggeser halaman.`
                  : "Table tidak memiliki primary key yang sesuai; urutan antarhalaman tidak dijamin."}
              </p>
            </>
          )}
          {tab === "columns" && meta.data && (
            <DataTable
              label="Column"
              rowKey={c => c.name}
              rows={meta.data.columns}
              columns={[
                { label: "#", render: c => c.ordinal_position },
                { label: "Nama", render: c => <code>{c.name}</code> },
                { label: "Tipe", render: c => c.data_type },
                { label: "Nullable", render: c => (c.nullable ? "Ya" : "Tidak") },
                { label: "Default", render: c => <CellView value={c.default} /> },
                { label: "Identity / generated", render: c => [c.identity, c.generated, c.generation_expression].filter(Boolean).join(" · ") || "—" },
              ]}
            />
          )}
          {tab === "constraints" && meta.data && (
            <DataTable
              label="Constraint"
              rowKey={c => c.name}
              rows={meta.data.constraints}
              empty="Table tidak memiliki constraint."
              columns={[
                { label: "Nama", render: c => <code>{c.name}</code> },
                { label: "Jenis", render: c => c.type },
                { label: "Kolom", render: c => c.columns.join(", ") },
                { label: "Referensi", render: c => (c.reference ? `${c.reference.schema}.${c.reference.table} (${c.reference.columns.join(", ")})` : "—") },
                { label: "Definisi", render: c => <code className="wrap">{c.definition}</code> },
                { label: "Valid", render: c => (c.validated ? "Ya" : "Belum divalidasi") },
              ]}
            />
          )}
          {tab === "indexes" && meta.data && (
            <DataTable
              label="Index"
              rowKey={i => i.name}
              rows={meta.data.indexes}
              empty="Table tidak memiliki index."
              columns={[
                { label: "Nama", render: i => <code>{i.name}</code> },
                { label: "Metode", render: i => i.method },
                { label: "Unique", render: i => (i.unique ? "Ya" : "Tidak") },
                { label: "Primary", render: i => (i.primary ? "Ya" : "Tidak") },
                { label: "Status", render: i => `${i.valid ? "Valid" : "Invalid"} / ${i.ready ? "Ready" : "Belum ready"}` },
                { label: "Definisi", render: i => <code className="wrap">{i.definition}</code> },
              ]}
            />
          )}
          {(meta.isPending || data.isPending) && <p className="muted" role="status">Memuat…</p>}
        </Card>
      )}
    </>
  );
}
