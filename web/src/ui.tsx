import { useState, type ReactNode } from "react";
import { NavLink, useLocation, useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, type AccessibleDatabase, type CellValue, type JobStatus } from "./api";
import { useSession } from "./auth";

// ---------- Kerangka halaman ----------

type ShellProps = {
  area: string;
  links: { to: string; label: string }[];
  switchTo?: { to: string; label: string };
  children: ReactNode;
};

/** Header + navigasi responsif yang dipakai Workspace dan Admin Console dengan menu berbeda. */
export function Shell({ area, links, switchTo, children }: ShellProps) {
  const { me, logout } = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const [open, setOpen] = useState(false);
  const [openedAt, setOpenedAt] = useState(location.pathname);
  if (open && openedAt !== location.pathname) setOpen(false); // tutup menu mobile setelah berpindah halaman
  return (
    <div className={`shell shell-${area.toLowerCase().replace(/\s+/g, "-")}`}>
      <a className="skip" href="#content">Lewati navigasi</a>
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">A</span>
          <span>Atlas</span>
          <span className="brand-area">{area}</span>
        </div>
        <button
          type="button"
          className="menu-toggle"
          aria-expanded={open}
          aria-controls="primary-nav"
          onClick={() => {
            setOpen(!open);
            setOpenedAt(location.pathname);
          }}
        >
          Menu
        </button>
        <nav id="primary-nav" className={open ? "nav open" : "nav"} aria-label={`Navigasi ${area}`}>
          {links.map(link => (
            <NavLink key={link.to} to={link.to}>
              {link.label}
            </NavLink>
          ))}
          {switchTo && (
            <NavLink className="nav-switch" to={switchTo.to}>
              {switchTo.label} →
            </NavLink>
          )}
          <div className="account">
            <span className="account-name">
              {me?.username}
              <small>{me?.is_platform_admin ? "Platform Admin" : "Pengguna"}</small>
            </span>
            <button
              type="button"
              onClick={async () => {
                await logout();
                navigate("/login", { replace: true });
              }}
            >
              Keluar
            </button>
          </div>
        </nav>
      </header>
      <main id="content" tabIndex={-1} className="content">
        {children}
      </main>
    </div>
  );
}

export function Splash({ text }: { text: string }) {
  return (
    <main className="splash" role="status" aria-live="polite">
      <span className="brand-mark" aria-hidden="true">A</span>
      <p>{text}</p>
    </main>
  );
}

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {description && <p className="muted">{description}</p>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}

export function Notice({ tone = "info", children }: { tone?: "info" | "error" | "success" | "warning"; children: ReactNode }) {
  return (
    <div className={`notice notice-${tone}`} role={tone === "error" ? "alert" : "status"}>
      {children}
    </div>
  );
}

export function ErrorNotice({ error }: { error: unknown }) {
  if (!error) return null;
  return <Notice tone="error">{error instanceof Error ? error.message : String(error)}</Notice>;
}

export function Card({ title, actions, children, className = "" }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || actions) && (
        <div className="card-head">
          {title && <h2>{title}</h2>}
          {actions && <div className="actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

// ---------- Tabel & pagination ----------

export type Column<T> = { label: string; render: (row: T) => ReactNode; className?: string };

/** Tabel lebar di-scroll di dalam komponennya sendiri sehingga halaman tidak melebar di mobile. */
export function DataTable<T>({ columns, rows, empty = "Belum ada data.", rowKey, label }: { columns: Column<T>[]; rows: T[]; empty?: string; rowKey: (row: T, i: number) => string | number; label?: string }) {
  if (!rows.length) return <p className="muted empty">{empty}</p>;
  return (
    <div className="table-scroll" tabIndex={0} role="region" aria-label={label ?? "Tabel"}>
      <table>
        <thead>
          <tr>
            {columns.map(c => (
              <th key={c.label} scope="col" className={c.className}>
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={rowKey(row, i)}>
              {columns.map(c => (
                <td key={c.label} data-label={c.label} className={c.className}>
                  {c.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Pager({ offset, size, count, hasMore, onChange }: { offset: number; size: number; count: number; hasMore?: boolean; onChange: (offset: number) => void }) {
  const more = hasMore ?? count >= size;
  return (
    <div className="pager">
      <span className="muted">{count ? `Baris ${offset + 1}–${offset + count}` : "Tidak ada baris pada halaman ini"}</span>
      <div className="actions">
        <button type="button" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - size))}>
          Sebelumnya
        </button>
        <button type="button" disabled={!more} onClick={() => onChange(offset + size)}>
          Berikutnya
        </button>
      </div>
    </div>
  );
}

// ---------- Nilai & status ----------

/** Menampilkan nilai bertipe dari backend (NULL, numeric string, biner Base64, teks panjang). */
export function CellView({ value }: { value: CellValue }) {
  if (value === null || value === undefined) return <span className="null">NULL</span>;
  let text: string;
  if (typeof value === "object" && value && "type" in value && "value" in value) {
    const typed = value as { type: string; value: string };
    text = typed.type === "binary" ? `Base64 · ${typed.value || "(kosong)"}` : String(typed.value);
  } else {
    text = typeof value === "object" ? JSON.stringify(value, null, 2) : String(value);
  }
  if (text.length < 120 && !text.includes("\n")) return <span>{text}</span>;
  return (
    <details className="long-value">
      <summary>{text.slice(0, 80)}…</summary>
      <pre>{text}</pre>
    </details>
  );
}

const statusTone: Record<string, string> = {
  SUCCESS: "ok",
  REVERTED: "ok",
  FAILED: "bad",
  NEEDS_REVIEW: "warn",
  PARTIAL: "warn",
  PARTIALLY_REVERTED: "warn",
  BLOCKED: "warn",
  QUEUED: "info",
  RUNNING: "run",
  READY: "run",
  RECOVERING: "run",
  INTERRUPTED: "bad",
};

export const statusLabel: Record<string, string> = {
  QUEUED: "Menunggu approval/antrean",
  RUNNING: "Berjalan",
  READY: "Siap",
  RECOVERING: "Pemulihan terjadwal",
  BLOCKED: "Tertahan",
  SUCCESS: "Sukses",
  FAILED: "Gagal",
  PARTIAL: "Sebagian committed",
  NEEDS_REVIEW: "Perlu ditinjau",
  PARTIALLY_REVERTED: "Sebagian dibatalkan",
  REVERTED: "Dibatalkan",
};

export function StatusBadge({ status }: { status: JobStatus | string }) {
  return <span className={`badge badge-${statusTone[status] ?? "info"}`}>{status}</span>;
}

export function AccessBadge({ level }: { level?: string }) {
  if (!level) return null;
  return <span className={`badge ${level === "View + Edit" ? "badge-run" : "badge-info"}`}>{level}</span>;
}

export const formatTime = (iso?: string | null) => (iso ? new Date(iso).toLocaleString("id-ID", { dateStyle: "medium", timeStyle: "medium" }) : "—");

// ---------- Pemilihan database ----------

export function useDatabases() {
  return useQuery({ queryKey: ["databases"], queryFn: api.databases, staleTime: 30_000 });
}

/** Pemilih database yang menyimpan pilihan di URL agar halaman dapat di-refresh dan dibagikan. */
export function DatabasePicker({ basePath, selected, filter }: { basePath: string; selected?: number; filter?: (db: AccessibleDatabase) => boolean }) {
  const navigate = useNavigate();
  const { data = [], isPending, error } = useDatabases();
  const rows = filter ? data.filter(filter) : data;
  const current = data.find(d => d.database_id === selected);
  return (
    <div className="picker">
      <label>
        Database
        <select value={selected ?? ""} disabled={isPending || rows.length === 0} onChange={e => navigate(e.target.value ? `${basePath}/${e.target.value}` : basePath)}>
          <option value="">{isPending ? "Memuat…" : rows.length ? "Pilih database" : "Tidak ada database yang dapat diakses"}</option>
          {rows.map(d => (
            <option key={d.database_id} value={d.database_id}>
              {d.name} · {d.driver}
            </option>
          ))}
        </select>
      </label>
      <AccessBadge level={current?.access_level} />
      <ErrorNotice error={error} />
    </div>
  );
}

export function useSelectedDatabase(dbId?: string) {
  const id = dbId ? Number(dbId) : undefined;
  const { data } = useDatabases();
  const database = data?.find(d => d.database_id === id);
  return { id: id && Number.isSafeInteger(id) && id > 0 ? id : undefined, database, canEdit: database?.access_level === "View + Edit" };
}
