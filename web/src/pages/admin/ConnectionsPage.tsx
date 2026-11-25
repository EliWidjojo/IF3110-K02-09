import { useState, type FormEvent } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Connection } from "../../api";
import { Card, DataTable, ErrorNotice, Notice, PageHeader, Pager } from "../../ui";

const SIZE = 20;
const TLS_MODES = ["require", "verify-full", "verify-ca", "prefer", "allow", "disable"];

export function ConnectionsPage() {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [editing, setEditing] = useState<Connection | null>(null);
  const [version, setVersion] = useState(0);
  const [driver, setDriver] = useState("postgres");
  const [message, setMessage] = useState("");
  const list = useQuery({ queryKey: ["admin-connections", offset], queryFn: () => api.connections(SIZE, offset), placeholderData: keepPreviousData });
  const done = (text: string) => {
    setMessage(text);
    setEditing(null);
    setVersion(v => v + 1);
    client.invalidateQueries({ queryKey: ["admin-connections"] });
    client.invalidateQueries({ queryKey: ["databases"] });
  };

  const save = useMutation({
    mutationFn: (form: FormData) => {
      const text = (name: string) => String(form.get(name) ?? "").trim();
      const body: Record<string, unknown> = {
        name: text("name"),
        driver: text("driver"),
        host: text("host"),
        port: Number(text("port")),
        database_name: text("database_name"),
        username: text("username"),
        audit_username: text("audit_username"),
        ssl_mode: text("ssl_mode"),
        ssl_root_cert: text("ssl_root_cert"),
      };
      if (body.ssl_root_cert === "system" && body.ssl_mode !== "verify-full") throw new Error("Root certificate system memerlukan TLS verify-full.");
      // Saat edit, password kosong berarti dipertahankan; pengosongan harus dipilih eksplisit.
      for (const [field, clear] of [["password", "clear_password"], ["audit_password", "clear_audit_password"]] as const) {
        const value = String(form.get(field) ?? "");
        const clearing = form.get(clear) === "on";
        if (value && clearing) throw new Error("Pilih password baru atau pengosongan eksplisit, bukan keduanya.");
        if (!editing || value || clearing) body[field] = clearing ? "" : value;
      }
      return editing ? api.updateConnection(editing.id, body) : api.createConnection(body);
    },
    onSuccess: () => done(editing ? "Konfigurasi diperbarui dan pool terkait diinvalidasi." : "Koneksi ditambahkan."),
  });
  const remove = useMutation({ mutationFn: (c: Connection) => api.deleteConnection(c.id), onSuccess: () => done("Konfigurasi dan akses group terkait dihapus. Data dan history pada target tetap ada.") });
  const check = useMutation({ mutationFn: (c: Connection) => api.checkConnection(c.id), onSuccess: (_, c) => setMessage(`Koneksi ${c.name} berhasil.`) });

  const startEdit = (c: Connection | null) => {
    setEditing(c);
    setDriver(c?.driver ?? "postgres");
    setVersion(v => v + 1);
  };

  return (
    <>
      <PageHeader title="Koneksi database" description="Kredensial disimpan terenkripsi dan tidak pernah dikirim kembali ke browser. Gunakan akun executor dan pencatat history yang terpisah." />
      {message && <Notice tone="success">{message}</Notice>}
      <Card title={editing ? `Edit koneksi #${editing.id}` : "Tambah koneksi"}>
        <form
          key={`${editing?.id ?? "new"}-${version}`}
          className="form-grid"
          onSubmit={(e: FormEvent<HTMLFormElement>) => {
            e.preventDefault();
            save.mutate(new FormData(e.currentTarget));
          }}
        >
          <label>
            Nama tampilan
            <input name="name" required maxLength={256} defaultValue={editing?.name} />
          </label>
          <label>
            DBMS
            <select name="driver" value={driver} onChange={e => setDriver(e.target.value)}>
              <option value="postgres">PostgreSQL</option>
              <option value="mariadb">MariaDB</option>
              <option value="mysql">MySQL (belum diklaim teruji)</option>
            </select>
          </label>
          <label>
            Host
            <input name="host" required maxLength={256} defaultValue={editing?.host} placeholder="127.0.0.1" />
          </label>
          <label>
            Port
            <input key={driver} name="port" type="number" min={1} max={65535} required defaultValue={editing?.driver === driver ? editing.port : driver === "postgres" ? 5432 : 3306} />
          </label>
          <label>
            Nama database
            <input name="database_name" required maxLength={256} defaultValue={editing?.database_name} />
          </label>
          <label>
            Mode TLS
            <select name="ssl_mode" defaultValue={editing?.ssl_mode ?? "require"}>
              {TLS_MODES.map(m => (
                <option key={m}>{m}</option>
              ))}
            </select>
          </label>
          <label>
            Username executor
            <input name="username" required maxLength={256} autoComplete="off" defaultValue={editing?.username} />
          </label>
          <label>
            Password executor
            <input name="password" type="password" maxLength={4096} autoComplete="new-password" placeholder={editing ? "Kosong: pertahankan password lama" : ""} />
          </label>
          <label>
            Username pencatat history
            <input name="audit_username" required maxLength={256} autoComplete="off" defaultValue={editing?.audit_username} />
          </label>
          <label>
            Password pencatat
            <input name="audit_password" type="password" maxLength={4096} autoComplete="new-password" placeholder={editing ? "Kosong: pertahankan password lama" : ""} />
          </label>
          {editing && (
            <>
              <label className="check">
                <input name="clear_password" type="checkbox" />
                Kosongkan password executor secara eksplisit
              </label>
              <label className="check">
                <input name="clear_audit_password" type="checkbox" />
                Kosongkan password pencatat secara eksplisit
              </label>
            </>
          )}
          <label className="span-2">
            Root certificate pada server
            <input name="ssl_root_cert" maxLength={4096} defaultValue={editing?.ssl_root_cert} placeholder="Path CA, atau system (verify-full)" />
          </label>
          <ErrorNotice error={save.error} />
          <div className="actions span-2">
            <button className="primary" type="submit" disabled={save.isPending}>
              Simpan koneksi
            </button>
            {editing && (
              <button type="button" onClick={() => startEdit(null)}>
                Batal
              </button>
            )}
          </div>
        </form>
      </Card>
      <Card title="Daftar koneksi">
        <ErrorNotice error={list.error ?? remove.error ?? check.error} />
        {list.data && (
          <>
            <DataTable
              label="Daftar koneksi"
              rowKey={c => c.id}
              rows={list.data}
              empty="Belum ada koneksi database."
              columns={[
                { label: "ID / nama", render: c => `#${c.id} · ${c.name}` },
                { label: "DBMS", render: c => c.driver },
                { label: "Target", render: c => `${c.host}:${c.port} / ${c.database_name}` },
                { label: "Akun", render: c => `${c.username} · pencatat ${c.audit_username}` },
                { label: "TLS", render: c => c.ssl_mode },
                { label: "Password", render: c => (c.password_configured ? "Dikonfigurasi" : "Belum") },
                {
                  label: "Tindakan",
                  render: c => (
                    <div className="actions">
                      <button type="button" onClick={() => startEdit(c)}>
                        Edit
                      </button>
                      <button type="button" disabled={check.isPending} onClick={() => check.mutate(c)}>
                        Periksa
                      </button>
                      <button type="button" className="danger" onClick={() => confirm(`Hapus konfigurasi ${c.name} beserta akses group terkait?`) && remove.mutate(c)}>
                        Hapus
                      </button>
                    </div>
                  ),
                },
              ]}
            />
            <Pager offset={offset} size={SIZE} count={list.data.length} onChange={setOffset} />
          </>
        )}
      </Card>
    </>
  );
}
