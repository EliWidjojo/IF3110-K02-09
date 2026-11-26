import { useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ACTIVE_JOB_STATUSES, api, type Attempt, type Job } from "../api";
import { Card, DataTable, DatabasePicker, ErrorNotice, Notice, PageHeader, Pager, StatusBadge, formatTime, useSelectedDatabase } from "../ui";

const HISTORY_PAGE = 25;
const JOB_PAGE = 10;

export function MigrationsPage() {
  const { dbId } = useParams();
  const { id, database, canEdit } = useSelectedDatabase(dbId);
  // "Salin ke editor" mengirim SQL lewat ?sql=; key membuat form diisi ulang dengan SQL tersebut.
  const [params] = useSearchParams();
  return (
    <>
      <PageHeader
        title="Migration"
        description="Ajukan perubahan, pantau job asynchronous, dan tinjau riwayat eksekusi. Setiap perubahan, termasuk rollback dan forward, memerlukan approval leader dari setiap group yang memiliki akses."
      />
      <Card>
        <DatabasePicker basePath="/migrations" selected={id} />
        {database && !canEdit && <p className="muted">Akses View Only: Anda dapat melihat job dan riwayat, tetapi tidak dapat mengajukan migration.</p>}
      </Card>
      {id && database && (
        <>
          {canEdit && <SubmitForm key={params.get("sql") ?? ""} db={id} driver={database.driver} />}
          <JobList db={id} />
          <History db={id} canEdit={canEdit} />
          {canEdit && <SchemaDiff db={id} />}
        </>
      )}
    </>
  );
}

// ---------- Pengajuan job ----------

function SubmitForm({ db, driver }: { db: number; driver: string }) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const [params] = useSearchParams();
  const [operation, setOperation] = useState<"apply" | "rollback" | "forward">("apply");
  const [sql, setSql] = useState(params.get("sql") ?? "");
  const [downSql, setDownSql] = useState("");
  const [batchSize, setBatchSize] = useState("");
  const [prepareCancel, setPrepareCancel] = useState(false);
  const [nonAtomic, setNonAtomic] = useState(false);
  const [source, setSource] = useState("");
  const [version, setVersion] = useState("");
  const retry = useRef<{ fingerprint: string; key: string } | null>(null);
  const postgres = driver === "postgres";
  const batched = postgres && operation === "apply" && Number(batchSize) > 0;

  const history = useQuery({ queryKey: ["history", db, 0], queryFn: () => api.history(db, HISTORY_PAGE, 0) });
  const jobs = useQuery({ queryKey: ["jobs", db, "sources"], queryFn: () => api.jobs(db, 100, 0), enabled: operation !== "apply" });
  const nextVersion = (history.data?.latest_version ?? 0) + 1;
  // Rollback/forward hanya berlaku untuk eksekusi sukses terakhir (posisi aktif), sesuai aturan A → B → C.
  const latest = (jobs.data ?? []).find(j => j.status === "SUCCESS");
  const sources = latest && !latest.submission.batch_size &&
    (operation === "rollback" ? latest.submission.operation !== "rollback" && !!latest.submission.down_sql : latest.submission.operation === "rollback")
    ? [latest]
    : [];

  const submit = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { operation, version: Number(version || nextVersion) };
      if (operation === "apply") {
        body.query_sql = sql;
        body.batch_size = batched ? Number(batchSize) : 0;
        if (batched && prepareCancel) body.prepare_cancellation = true;
        if (!batched && downSql.trim()) body.down_sql = downSql;
        body.non_atomic_ddl = !postgres && nonAtomic;
      } else {
        body.source_job = Number(source);
      }
      // Kunci idempotensi yang sama dipakai ulang bila isi pengajuan tidak berubah (mis. retry setelah jaringan putus).
      const fingerprint = JSON.stringify(body);
      if (retry.current?.fingerprint !== fingerprint) retry.current = { fingerprint, key: crypto.randomUUID() };
      return api.submitJob(db, body, retry.current.key);
    },
    onSuccess: job => {
      retry.current = null;
      client.invalidateQueries({ queryKey: ["jobs", db] });
      navigate(`/migrations/${db}/jobs/${job.id}`);
    },
  });

  return (
    <Card title="Ajukan perubahan">
      <form
        className="form-grid"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          submit.mutate();
        }}
      >
        <label>
          Operasi
          <select value={operation} onChange={e => setOperation(e.target.value as typeof operation)}>
            <option value="apply">Migration baru</option>
            <option value="rollback">Rollback job sukses terakhir</option>
            <option value="forward">Forward (terapkan ulang setelah rollback)</option>
          </select>
        </label>
        <label>
          Versi eksekusi
          <input type="number" min={1} max={2147483647} value={version} placeholder={String(nextVersion)} onChange={e => setVersion(e.target.value)} />
          <small className="muted">Harus lebih besar dari versi sukses terakhir ({history.data?.latest_version ?? "…"}).</small>
        </label>
        {operation === "apply" ? (
          <>
            <label className="span-2">
              SQL up
              <textarea className="code" rows={10} spellCheck={false} maxLength={12288} required value={sql} onChange={e => setSql(e.target.value)} placeholder="CREATE TABLE public.contoh (id BIGINT PRIMARY KEY, nama TEXT);" />
            </label>
            {postgres && (
              <label>
                Ukuran batch (kosong = migration biasa)
                <input type="number" min={1} max={100000} value={batchSize} onChange={e => setBatchSize(e.target.value)} placeholder="Contoh: 10000" />
                {batched && <small className="muted">Format batch: UPDATE public.tabel SET kolom = TRUE/FALSE pada table dengan primary key integer id.</small>}
              </label>
            )}
            {batched ? (
              <label className="check">
                <input type="checkbox" checked={prepareCancel} onChange={e => setPrepareCancel(e.target.checked)} />
                Simpan nilai awal tiap row agar batch committed dapat dibatalkan
              </label>
            ) : (
              <label className="span-2">
                SQL down untuk rollback (kosong = irreversible)
                <textarea className="code" rows={5} spellCheck={false} maxLength={12288} value={downSql} onChange={e => setDownSql(e.target.value)} placeholder="DROP TABLE public.contoh;" />
              </label>
            )}
            {!postgres && (
              <label className="check span-2">
                <input type="checkbox" checked={nonAtomic} onChange={e => setNonAtomic(e.target.checked)} />
                Satu DDL MariaDB/MySQL yang tidak dapat di-rollback (implicit commit; hasil meragukan menjadi NEEDS_REVIEW)
              </label>
            )}
          </>
        ) : (
          <label className="span-2">
            Job sumber
            <select required value={source} onChange={e => setSource(e.target.value)}>
              <option value="">
                {jobs.isPending ? "Memuat job…" : sources.length ? "Pilih job" : operation === "rollback" ? "Job sukses terakhir tidak memiliki SQL down" : "Job sukses terakhir bukan rollback"}
              </option>
              {sources.map(j => (
                <option key={j.id} value={j.id}>
                  Job #{j.id} · v{j.submission.version} · {j.submission.operation ?? "apply"} · {formatTime(j.finished_at)}
                </option>
              ))}
            </select>
            <small className="muted">
              {operation === "rollback"
                ? "Rollback menjalankan SQL down asli dari job sukses terkini; harus job aktif terakhir."
                : "Forward menjalankan kembali SQL up dari job yang telah di-rollback."}
            </small>
          </label>
        )}
        <ErrorNotice error={submit.error} />
        <div className="actions span-2">
          <button className="primary" type="submit" disabled={submit.isPending}>
            {submit.isPending ? "Mengirim…" : "Ajukan untuk review"}
          </button>
          {operation === "apply" && postgres && (
            <Link className="button" to={`/analysis/${db}?sql=${encodeURIComponent(sql)}`}>
              Analisis SQL ini
            </Link>
          )}
        </div>
        <p className="muted small span-2">Job masuk antrean berstatus QUEUED dan baru dijalankan worker setelah semua leader menyetujui. SQL maksimum 12 KiB.</p>
      </form>
    </Card>
  );
}

// ---------- Daftar job ----------

function JobList({ db }: { db: number }) {
  const [offset, setOffset] = useState(0);
  const jobs = useQuery({
    queryKey: ["jobs", db, offset],
    queryFn: () => api.jobs(db, JOB_PAGE, offset),
    placeholderData: keepPreviousData,
    // Pantau otomatis selama ada job yang masih bergerak.
    refetchInterval: q => (q.state.data?.some(j => ACTIVE_JOB_STATUSES.includes(j.status)) ? 2000 : false),
  });
  return (
    <Card title="Job migration" actions={<button type="button" onClick={() => jobs.refetch()}>Muat ulang</button>}>
      <ErrorNotice error={jobs.error} />
      {jobs.data && (
        <>
          <DataTable
            label="Job migration"
            rowKey={j => j.id}
            rows={jobs.data}
            empty="Belum ada job pada database ini."
            columns={[
              { label: "Job", render: j => <Link to={`/migrations/${db}/jobs/${j.id}`}>#{j.id}</Link> },
              { label: "Operasi", render: j => `${j.submission.operation ?? "apply"}${j.submission.source_job ? ` ← #${j.submission.source_job}` : ""}` },
              { label: "Versi", render: j => `v${j.submission.version}` },
              { label: "Status", render: j => <StatusBadge status={j.status} /> },
              { label: "Progres", render: j => (j.submission.batch_size ? `${j.committed_batches} batch · ${j.committed_rows.toLocaleString("id-ID")} row` : "—") },
              { label: "Diajukan", render: j => `User #${j.submitted_by} · ${formatTime(j.created_at)}` },
            ]}
          />
          <Pager offset={offset} size={JOB_PAGE} count={jobs.data.length} onChange={setOffset} />
        </>
      )}
    </Card>
  );
}

// ---------- Riwayat & hide rollback ----------

/**
 * Pasangan disembunyikan berdasarkan execution ID: attempt yang dibatalkan dan attempt rollback sukses
 * yang membatalkannya. Data pasangan diambil dari field history bila backend menyediakannya,
 * atau dari job (operation, source_job, attempt_id).
 */
export function hiddenRollbackPairs(attempts: Attempt[], jobs: Job[]): Set<number> {
  const hidden = new Set<number>();
  const byStatus = new Map(attempts.map(a => [a.id, a.status]));
  for (const a of attempts) {
    if (a.operation === "rollback" && a.status === "SUCCESS" && a.reverts_attempt_id) {
      hidden.add(a.id);
      hidden.add(a.reverts_attempt_id);
    }
  }
  const jobById = new Map(jobs.map(j => [j.id, j]));
  for (const j of jobs) {
    if (j.submission.operation !== "rollback" || j.status !== "SUCCESS" || !j.attempt_id) continue;
    if (byStatus.get(j.attempt_id) !== undefined && byStatus.get(j.attempt_id) !== "SUCCESS") continue;
    const source = jobById.get(j.submission.source_job ?? 0);
    if (!source?.attempt_id) continue;
    hidden.add(j.attempt_id);
    hidden.add(source.attempt_id);
  }
  return hidden;
}

function History({ db, canEdit }: { db: number; canEdit: boolean }) {
  const [offset, setOffset] = useState(0);
  const [hideRollback, setHideRollback] = useState(false);
  const history = useQuery({ queryKey: ["history", db, offset], queryFn: () => api.history(db, HISTORY_PAGE, offset), placeholderData: keepPreviousData });
  const jobs = useQuery({ queryKey: ["jobs", db, "pairs"], queryFn: () => api.jobs(db, 100, 0) });
  const navigate = useNavigate();
  const jobByAttempt = useMemo(() => new Map((jobs.data ?? []).filter(j => j.attempt_id).map(j => [j.attempt_id!, j])), [jobs.data]);
  const hidden = useMemo(() => hiddenRollbackPairs(history.data?.attempts ?? [], jobs.data ?? []), [history.data, jobs.data]);
  const rows = (history.data?.attempts ?? []).filter(a => !hideRollback || !hidden.has(a.id));
  return (
    <Card
      title="Riwayat eksekusi"
      actions={
        <>
          <label className="check">
            <input type="checkbox" checked={hideRollback} onChange={e => setHideRollback(e.target.checked)} />
            Hide rollback
          </label>
          <button type="button" onClick={() => history.refetch()}>
            Muat ulang
          </button>
        </>
      }
    >
      <p className="muted small">
        Semua attempt tercatat, termasuk yang gagal. Hide rollback hanya menyembunyikan pasangan eksekusi yang dibatalkan dan rollback suksesnya; data riwayat tidak dihapus.
        {history.data && ` Versi sukses terakhir: ${history.data.latest_version}.`}
      </p>
      <ErrorNotice error={history.error} />
      {hideRollback && hidden.size > 0 && <Notice>{hidden.size} attempt disembunyikan pada halaman ini.</Notice>}
      {history.data && (
        <>
          <DataTable
            label="Riwayat eksekusi"
            rowKey={a => a.id}
            rows={rows}
            empty="Belum ada attempt migration."
            columns={[
              { label: "Execution", render: a => `E${a.id}` },
              { label: "Versi", render: a => `v${a.version}` },
              {
                label: "Operasi",
                render: a => {
                  const job = jobByAttempt.get(a.id);
                  const op = a.operation ?? job?.submission.operation ?? "apply";
                  return job ? <Link to={`/migrations/${db}/jobs/${job.id}`}>{op} · job #{job.id}</Link> : op;
                },
              },
              { label: "Status", render: a => <span><StatusBadge status={a.status} /> {[a.sql_state, a.error_code].filter(Boolean).join(" · ")}</span> },
              { label: "Pelaksana", render: a => `User #${a.executed_by}` },
              { label: "Waktu", render: a => formatTime(a.executed_at) },
              {
                label: "SQL",
                render: a => (
                  <details>
                    <summary>Lihat SQL</summary>
                    <pre className="code">{a.query_sql}</pre>
                    {canEdit && (
                      <button type="button" onClick={() => navigate(`/migrations/${db}?sql=${encodeURIComponent(a.query_sql)}`)}>
                        Salin ke editor
                      </button>
                    )}
                  </details>
                ),
              },
            ]}
          />
          <Pager offset={offset} size={HISTORY_PAGE} count={history.data.attempts.length} onChange={setOffset} />
        </>
      )}
    </Card>
  );
}

// ---------- Schema diff ----------

const DIFF_EXAMPLE = JSON.stringify(
  { schema: "public", tables: [{ name: "contoh", columns: [{ name: "id", type: "bigint", nullable: false }, { name: "nama", type: "text", nullable: true }] }] },
  null,
  2,
);

function SchemaDiff({ db }: { db: number }) {
  const navigate = useNavigate();
  const [input, setInput] = useState(DIFF_EXAMPLE);
  const diff = useMutation({
    mutationFn: () => {
      let desired: unknown;
      try {
        desired = JSON.parse(input);
      } catch {
        throw new Error("Schema tujuan harus berupa JSON valid.");
      }
      return api.diff(db, desired);
    },
  });
  return (
    <details className="card">
      <summary>Schema diff &amp; kandidat migration</summary>
      <p className="muted">Masukkan schema tujuan sebagai JSON. Kandidat SQL tidak dijalankan otomatis dan tetap harus ditinjau.</p>
      <label>
        Schema tujuan
        <textarea className="code" rows={10} spellCheck={false} value={input} onChange={e => setInput(e.target.value)} />
      </label>
      <div className="actions">
        <button type="button" onClick={() => diff.mutate()} disabled={diff.isPending}>
          Bandingkan schema
        </button>
        {diff.data?.sql && (
          <button type="button" onClick={() => navigate(`/migrations/${db}?sql=${encodeURIComponent(diff.data!.sql)}`)}>
            Salin kandidat ke editor
          </button>
        )}
      </div>
      <ErrorNotice error={diff.error} />
      {diff.data && (
        <>
          <DataTable
            label="Perbedaan schema"
            rowKey={(_, i) => i}
            rows={diff.data.differences}
            empty="Tidak ada perbedaan pada cakupan yang didukung."
            columns={[
              { label: "Objek", render: d => d.object },
              { label: "Perbedaan", render: d => d.kind },
              { label: "Keterangan", render: d => `${d.requires_decision ? "Perlu keputusan: " : ""}${d.message}` },
            ]}
          />
          <pre className="code">{diff.data.sql || "Tidak ada SQL yang dapat dihasilkan otomatis."}</pre>
          <p className="muted small">{diff.data.limitations}</p>
        </>
      )}
    </details>
  );
}
