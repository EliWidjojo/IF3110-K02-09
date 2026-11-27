import { useState, type FormEvent } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type User } from "../../api";
import { Card, DataTable, ErrorNotice, Notice, PageHeader, Pager } from "../../ui";

const SIZE = 20;

export function UsersPage() {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [editing, setEditing] = useState<User | null>(null);
  const [message, setMessage] = useState("");
  const [formVersion, setFormVersion] = useState(0); // remount form setelah simpan agar password terhapus dari DOM
  const users = useQuery({ queryKey: ["admin-users", offset], queryFn: () => api.users(SIZE, offset), placeholderData: keepPreviousData });
  const done = (text: string) => {
    setMessage(text);
    setEditing(null);
    setFormVersion(v => v + 1);
    client.invalidateQueries({ queryKey: ["admin-users"] });
  };
  const save = useMutation({
    mutationFn: (form: FormData) => {
      const username = String(form.get("username")).trim();
      const isAdmin = form.get("is_platform_admin") === "on";
      if (editing) return api.updateUser(editing.id, { username, is_platform_admin: isAdmin, is_active: form.get("is_active") === "on" });
      return api.createUser({ username, password: String(form.get("password")), is_platform_admin: isAdmin });
    },
    onSuccess: () => done(editing ? "Perubahan user disimpan." : "User dibuat."),
  });
  const deactivate = useMutation({ mutationFn: (u: User) => api.deactivateUser(u.id), onSuccess: () => done("User dinonaktifkan dan session-nya dicabut.") });

  return (
    <>
      <PageHeader title="User" description="Kelola identitas dan peran platform. Akses database tidak diberikan langsung ke user, melainkan melalui group." />
      {message && <Notice tone="success">{message}</Notice>}
      <Card title={editing ? `Edit user #${editing.id}` : "Buat user"}>
        <form
          key={`${editing?.id ?? "new"}-${formVersion}`}
          className="form-grid"
          onSubmit={(e: FormEvent<HTMLFormElement>) => {
            e.preventDefault();
            const form = new FormData(e.currentTarget);
            if (editing && editing.is_active && form.get("is_active") !== "on" && !confirm("Nonaktifkan user ini dan cabut session-nya?")) return;
            save.mutate(form);
          }}
        >
          <label>
            Username
            <input name="username" required maxLength={128} autoComplete="off" defaultValue={editing?.username} />
          </label>
          {!editing && (
            <label>
              Password awal
              <input name="password" type="password" required minLength={12} maxLength={1024} autoComplete="new-password" />
              <small className="muted">Minimal 12 karakter.</small>
            </label>
          )}
          <label className="check">
            <input name="is_platform_admin" type="checkbox" defaultChecked={editing?.is_platform_admin} />
            Platform Admin
          </label>
          {editing && (
            <label className="check">
              <input name="is_active" type="checkbox" defaultChecked={editing.is_active} />
              User aktif
            </label>
          )}
          <ErrorNotice error={save.error} />
          <div className="actions span-2">
            <button className="primary" type="submit" disabled={save.isPending}>
              Simpan user
            </button>
            {editing && (
              <button type="button" onClick={() => setEditing(null)}>
                Batal
              </button>
            )}
          </div>
        </form>
      </Card>
      <Card title="Daftar user">
        <ErrorNotice error={users.error ?? deactivate.error} />
        {users.data && (
          <>
            <DataTable
              label="Daftar user"
              rowKey={u => u.id}
              rows={users.data}
              columns={[
                { label: "ID", render: u => u.id },
                { label: "Username", render: u => u.username },
                { label: "Status", render: u => <span className={`badge ${u.is_active ? "badge-ok" : "badge-bad"}`}>{u.is_active ? "Aktif" : "Nonaktif"}</span> },
                { label: "Peran", render: u => (u.is_platform_admin ? "Platform Admin" : "Pengguna") },
                {
                  label: "Tindakan",
                  render: u => (
                    <div className="actions">
                      <button type="button" onClick={() => setEditing(u)}>
                        Edit
                      </button>
                      {u.is_active && (
                        <button type="button" className="danger" onClick={() => confirm(`Nonaktifkan ${u.username}? Session user akan dicabut.`) && deactivate.mutate(u)}>
                          Nonaktifkan
                        </button>
                      )}
                    </div>
                  ),
                },
              ]}
            />
            <Pager offset={offset} size={SIZE} count={users.data.length} onChange={setOffset} />
          </>
        )}
      </Card>
    </>
  );
}
