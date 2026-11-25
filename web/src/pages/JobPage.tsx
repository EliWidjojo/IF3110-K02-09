import { Link, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ACTIVE_JOB_STATUSES, api, type Job } from "../api";
import { useSession } from "../auth";
import { Card, DataTable, ErrorNotice, Notice, PageHeader, StatusBadge, formatTime, statusLabel, useSelectedDatabase } from "../ui";

export function JobPage() {
  const params = useParams();
  const db = Number(params.dbId);
  const jobId = Number(params.jobId);
  const { me } = useSession();
  const { database, canEdit } = useSelectedDatabase(params.dbId);
  const client = useQueryClient();

  // Status dipantau berkala selama job masih bergerak; berhenti otomatis setelah status akhir.
  const job = useQuery({
    queryKey: ["job", db, jobId],
    queryFn: () => api.job(db, jobId),
    refetchInterval: q => (q.state.data && ACTIVE_JOB_STATUSES.includes(q.state.data.status) ? 1500 : false),
  });
  const approvals = useQuery({
    queryKey: ["approvals", db, jobId, job.data?.retry_count],
    queryFn: () => api.approvals(db, jobId),
    enabled: !!job.data,
    refetchInterval: job.data?.status === "QUEUED" || job.data?.cancel_waiting ? 3000 : false,
  });
  const refresh = (updated?: Job) => {
    if (updated) client.setQueryData(["job", db, jobId], updated);
    client.invalidateQueries({ queryKey: ["job", db, jobId] });
    client.invalidateQueries({ queryKey: ["approvals", db, jobId] });
    client.invalidateQueries({ queryKey: ["jobs", db] });
    client.invalidateQueries({ queryKey: ["history", db] });
  };
  const approve = useMutation({ mutationFn: (group: number) => api.approve(db, jobId, group, job.data!.retry_count), onSuccess: () => refresh() });
  const retry = useMutation({ mutationFn: () => api.retryBatch(db, jobId, job.data!.retry_count + 1), onSuccess: refresh });
  const cancel = useMutation({ mutationFn: () => api.cancelBatch(db, jobId, job.data!.retry_count + 1), onSuccess: refresh });
  const recover = useMutation({ mutationFn: () => api.recover(db, jobId), onSuccess: refresh });

  const j = job.data;
  const leads = new Set((me?.leader_groups ?? []).map(g => g.id));
  const awaitingApproval = !!j && (j.status === "QUEUED" || !!j.cancel_waiting);
  const batch = !!j && canEdit && !!j.submission.batch_size && !!j.attempt_id;
  const canRetry = batch && ["PARTIAL", "FAILED"].includes(j.status) && (!j.cancel_origin || (j.status === "PARTIAL" && !j.reverted_batches));
  const canCancel =
    batch && !!j.submission.down_sql && j.committed_batches > 0 &&
    (["SUCCESS", "PARTIAL", "PARTIALLY_REVERTED"].includes(j.status) || (!j.cancel_origin && j.status === "RUNNING") || (!!j.cancel_origin && j.status === "NEEDS_REVIEW"));
  const canRecover = !!j && canEdit && j.status === "NEEDS_REVIEW" && !!j.attempt_id && j.error_code !== "target_changed";
  const actionError = approve.error ?? retry.error ?? cancel.error ?? recover.error;

  return (
    <>
      <PageHeader
        title={`Job #${jobId}`}
        description={database ? `${database.name} · ${database.access_level}` : undefined}
        actions={<Link className="button" to={`/migrations/${db}`}>← Kembali ke migration</Link>}
      />
      <ErrorNotice error={job.error} />
      {j && (
        <>
          <Card>
            <div className="job-summary">
              <div>
                <StatusBadge status={j.status} />
                <p className="lead">{statusLabel[j.status] ?? j.status}</p>
                <p className="muted small">{describe(j)}</p>
              </div>
              <ol className="flow" aria-label="Alur job">
                <li className="done">Draft</li>
                <li className={j.status === "QUEUED" ? "current" : "done"}>Review &amp; approval</li>
                <li className={stepClass(j, "RUNNING")}>Worker berjalan</li>
                <li className={stepClass(j, "SUCCESS")}>Selesai</li>
              </ol>
            </div>
            {!!j.submission.batch_size && (
              <div className="progress-block">
                <progress aria-label="Progres batch" {...(ACTIVE_JOB_STATUSES.includes(j.status) ? {} : { value: 1, max: 1 })} />
                <p>
                  <strong>{j.committed_batches.toLocaleString("id-ID")}</strong> batch · <strong>{j.committed_rows.toLocaleString("id-ID")}</strong> row committed
                  {j.status === "PARTIAL" && " — sebagian, dapat dilanjutkan dari checkpoint"}
                  {!!j.cancel_origin && ` · dipulihkan ${j.reverted_batches} batch, ${j.reverted_rows} row`}
                </p>
              </div>
            )}
            <dl className="facts">
              <div><dt>Operasi</dt><dd>{j.submission.operation ?? "apply"}{j.submission.source_job ? ` (sumber job #${j.submission.source_job})` : ""}</dd></div>
              <div><dt>Versi</dt><dd>v{j.submission.version}</dd></div>
              <div><dt>Mode</dt><dd>{j.submission.batch_size ? `Per batch · ${j.submission.batch_size.toLocaleString("id-ID")} row` : "Migration biasa"}</dd></div>
              <div><dt>Execution</dt><dd>{j.attempt_id ? `E${j.attempt_id}` : "Belum dieksekusi"}</dd></div>
              <div><dt>Diajukan</dt><dd>User #{j.submitted_by} · {formatTime(j.created_at)}</dd></div>
              <div><dt>Mulai / selesai</dt><dd>{formatTime(j.started_at)} / {formatTime(j.finished_at)}</dd></div>
              <div><dt>Worker</dt><dd>{j.worker_id ?? "—"}</dd></div>
              <div><dt>Retry / pemulihan</dt><dd>{j.retry_count} / {j.recovery_count}</dd></div>
            </dl>
          </Card>

          <Card title="Approval leader group">
            <p className="muted small">Setiap group yang memiliki akses (View Only maupun View + Edit) ke database ini harus disetujui leader-nya sebelum worker menjalankan job.</p>
            <ErrorNotice error={approvals.error ?? actionError} />
            {approvals.data && (
              <DataTable
                label="Approval"
                rowKey={a => a.group_id}
                rows={approvals.data}
                empty="Tidak ada group yang memiliki akses."
                columns={[
                  { label: "Group", render: a => `${a.group_name} (#${a.group_id})` },
                  { label: "Leader", render: a => a.leader_username ?? <span className="muted">Belum ditetapkan admin</span> },
                  { label: "Status", render: a => (a.approved ? <span className="badge badge-ok">Disetujui · {formatTime(a.approved_at)}</span> : <span className="badge badge-info">Menunggu</span>) },
                  {
                    label: "Tindakan",
                    render: a =>
                      awaitingApproval && !a.approved && leads.has(a.group_id) ? (
                        <button className="primary" type="button" disabled={approve.isPending} onClick={() => approve.mutate(a.group_id)}>
                          Setujui sebagai leader
                        </button>
                      ) : (
                        "—"
                      ),
                  },
                ]}
              />
            )}
            {awaitingApproval && approvals.data?.some(a => !a.leader_id) && (
              <Notice tone="warning">Ada group tanpa leader. Minta Platform Admin menetapkan leader agar job dapat berjalan.</Notice>
            )}
          </Card>

          {(canRetry || canCancel || canRecover) && (
            <Card title="Tindakan lanjutan">
              <div className="actions">
                {canRetry && (
                  <button type="button" onClick={() => retry.mutate()} disabled={retry.isPending}>
                    Lanjutkan batch dari checkpoint
                  </button>
                )}
                {canCancel && (
                  <button
                    type="button"
                    className="danger"
                    disabled={cancel.isPending}
                    onClick={() => confirm("Ajukan pembatalan batch committed? SQL down dijalankan per batch dalam urutan terbalik dan memerlukan approval ulang.") && cancel.mutate()}
                  >
                    {j.reverted_batches ? "Lanjutkan pembatalan batch" : "Batalkan batch committed"}
                  </button>
                )}
                {canRecover && (
                  <button type="button" onClick={() => recover.mutate()} disabled={recover.isPending}>
                    Periksa ulang riwayat target
                  </button>
                )}
              </div>
              <p className="muted small">Retry dan pembatalan membuat putaran approval baru dan tetap mengikuti permission serta validasi.</p>
            </Card>
          )}

          <Card title="Snapshot pengajuan">
            <p className="muted small">SQL yang disetujui bersifat immutable; perbaikan dilakukan dengan migration baru.</p>
            <h3>SQL up</h3>
            <pre className="code">{j.submission.query_sql}</pre>
            <h3>SQL down</h3>
            <pre className="code">{j.submission.down_sql || "— (irreversible)"}</pre>
          </Card>
        </>
      )}
      {job.isPending && <p className="muted" role="status">Memuat job…</p>}
    </>
  );
}

function stepClass(j: Job, step: "RUNNING" | "SUCCESS"): string {
  if (step === "RUNNING") {
    if (["RUNNING", "READY", "RECOVERING", "BLOCKED"].includes(j.status)) return "current";
    return j.status === "QUEUED" ? "" : "done";
  }
  if (j.status === "SUCCESS" || j.status === "REVERTED") return "done";
  if (["FAILED", "NEEDS_REVIEW", "PARTIAL", "PARTIALLY_REVERTED"].includes(j.status)) return "failed";
  return "";
}

function describe(j: Job): string {
  const parts: string[] = [];
  if (j.status === "QUEUED") parts.push("Menunggu semua leader menyetujui; worker mengambil job setelah approval lengkap.");
  if (j.status === "BLOCKED") parts.push("Ditahan karena migration lain pada database ini belum tuntas atau perlu ditinjau.");
  if (j.status === "RECOVERING" && j.next_attempt_at)
    parts.push(`${j.error_code === "target_busy" ? "Menunggu lock database" : `Pemulihan ke-${j.recovery_count}`} dijadwalkan ${formatTime(j.next_attempt_at)}.`);
  if (j.status === "NEEDS_REVIEW" && j.error_code === "outcome_unknown") parts.push("Hasil commit belum pasti; periksa riwayat target sebelum melanjutkan.");
  if (j.error_code === "unsafe_revert") parts.push("Pembatalan tidak aman; ajukan migration perbaikan.");
  if (j.error_code && !parts.length) parts.push(`Kode: ${j.error_code}`);
  return parts.join(" ");
}
