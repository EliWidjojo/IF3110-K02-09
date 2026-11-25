import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type AccessLevel, type Group } from "../../api";
import { AccessBadge, Card, DataTable, ErrorNotice, Notice, PageHeader, Pager } from "../../ui";

const SIZE = 20;

export function GroupsPage() {
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [editing, setEditing] = useState<Group | null>(null);
  const [version, setVersion] = useState(0);
  const groups = useQuery({ queryKey: ["admin-groups", offset], queryFn: () => api.groups(SIZE, offset), placeholderData: keepPreviousData });
  const refresh = () => {
    setEditing(null);
    setVersion(v => v + 1);
    client.invalidateQueries({ queryKey: ["admin-groups"] });
  };
  const save = useMutation({ mutationFn: (name: string) => (editing ? api.renameGroup(editing.id, name) : api.createGroup(name)), onSuccess: refresh });
  const remove = useMutation({ mutationFn: (g: Group) => api.deleteGroup(g.id), onSuccess: refresh });

  return (
    <>
      <PageHeader title="Group & akses" description="Akses database diberikan ke group. Setiap group memiliki satu leader yang wajib menyetujui perubahan pada database yang dapat diakses group tersebut." />
      <Card title={editing ? `Ubah nama group #${editing.id}` : "Buat group"}>
        <form
          key={`${editing?.id ?? "new"}-${version}`}
          className="inline-form"
          onSubmit={(e: FormEvent<HTMLFormElement>) => {
            e.preventDefault();
            save.mutate(String(new FormData(e.currentTarget).get("name")).trim());
          }}
        >
          <label>
            Nama group
            <input name="name" required maxLength={128} defaultValue={editing?.name} />
          </label>
          <div className="actions">
            <button className="primary" type="submit" disabled={save.isPending}>
              Simpan group
            </button>
            {editing && (
              <button type="button" onClick={() => setEditing(null)}>
                Batal
              </button>
            )}
          </div>
        </form>
        <ErrorNotice error={save.error} />
      </Card>
      <Card title="Daftar group">
        <ErrorNotice error={groups.error ?? remove.error} />
        {groups.data && (
          <>
            <DataTable
              label="Daftar group"
              rowKey={g => g.id}
              rows={groups.data}
              empty="Belum ada group."
              columns={[
                { label: "ID", render: g => g.id },
                { label: "Nama", render: g => <Link to={`/admin/groups/${g.id}`}>{g.name}</Link> },
                {
                  label: "Tindakan",
                  render: g => (
                    <div className="actions">
                      <Link className="button" to={`/admin/groups/${g.id}`}>
                        Anggota, leader & akses
                      </Link>
                      <button type="button" onClick={() => setEditing(g)}>
                        Ubah nama
                      </button>
                      <button type="button" className="danger" onClick={() => confirm(`Hapus group ${g.name} beserta keanggotaan dan aksesnya?`) && remove.mutate(g)}>
                        Hapus
                      </button>
                    </div>
                  ),
                },
              ]}
            />
            <Pager offset={offset} size={SIZE} count={groups.data.length} onChange={setOffset} />
          </>
        )}
      </Card>
    </>
  );
}

export function GroupDetailPage() {
  const id = Number(useParams().groupId);
  const navigate = useNavigate();
  const client = useQueryClient();
  const [memberOffset, setMemberOffset] = useState(0);
  const [grantOffset, setGrantOffset] = useState(0);
  const [message, setMessage] = useState("");
  const group = useQuery({ queryKey: ["admin-group", id], queryFn: () => api.group(id) });
  const members = useQuery({ queryKey: ["admin-members", id, memberOffset], queryFn: () => api.members(id, SIZE, memberOffset), placeholderData: keepPreviousData });
  const leader = useQuery({ queryKey: ["admin-leader", id], queryFn: () => api.leader(id) });
  const grants = useQuery({ queryKey: ["admin-grants", id, grantOffset], queryFn: () => api.grants(id, SIZE, grantOffset), placeholderData: keepPreviousData });
  const allUsers = useQuery({ queryKey: ["admin-users", "options"], queryFn: () => api.users(100, 0) });
  const connections = useQuery({ queryKey: ["admin-connections", "options"], queryFn: () => api.connections(100, 0) });
  const connectionName = (db: number) => connections.data?.find(c => c.id === db)?.name ?? `#${db}`;

  const after = (text: string) => () => {
    setMessage(text);
    for (const key of ["admin-members", "admin-leader", "admin-grants"]) client.invalidateQueries({ queryKey: [key, id] });
  };
  const addMember = useMutation({ mutationFn: (userId: number) => api.addMember(id, userId), onSuccess: after("Anggota ditambahkan.") });
  const removeMember = useMutation({ mutationFn: (userId: number) => api.removeMember(id, userId), onSuccess: after("Anggota dikeluarkan; session terkait diperbarui pada request berikutnya.") });
  const setLeader = useMutation({ mutationFn: (userId: number) => api.setLeader(id, userId), onSuccess: after("Leader group ditetapkan.") });
  const setGrant = useMutation({ mutationFn: ({ db, level }: { db: number; level: AccessLevel }) => api.setGrant(id, db, level), onSuccess: after("Akses database disimpan.") });
  const revoke = useMutation({ mutationFn: (db: number) => api.revokeGrant(id, db), onSuccess: after("Akses database dicabut.") });
  const error = addMember.error ?? removeMember.error ?? setLeader.error ?? setGrant.error ?? revoke.error;
  const memberIds = new Set(members.data?.map(m => m.id));

  return (
    <>
      <PageHeader
        title={group.data ? `Group: ${group.data.name}` : `Group #${id}`}
        description="Kelola anggota, leader approval, dan tingkat akses database untuk group ini."
        actions={
          <button type="button" onClick={() => navigate("/admin/groups")}>
            ← Semua group
          </button>
        }
      />
      {message && <Notice tone="success">{message}</Notice>}
      <ErrorNotice error={group.error ?? error} />
      <div className="split">
        <Card title="Anggota & leader">
          <form
            className="inline-form"
            onSubmit={(e: FormEvent<HTMLFormElement>) => {
              e.preventDefault();
              addMember.mutate(Number(new FormData(e.currentTarget).get("user")));
            }}
          >
            <label>
              Tambah anggota
              <select name="user" required>
                <option value="">Pilih user</option>
                {allUsers.data
                  ?.filter(u => u.is_active && !memberIds.has(u.id))
                  .map(u => (
                    <option key={u.id} value={u.id}>
                      {u.username} (#{u.id})
                    </option>
                  ))}
              </select>
            </label>
            <button className="primary" type="submit" disabled={addMember.isPending}>
              Tambahkan
            </button>
          </form>
          <p className="muted small">
            Leader saat ini: <strong>{leader.data?.user_id ? (members.data?.find(m => m.id === leader.data!.user_id)?.username ?? `User #${leader.data.user_id}`) : "belum ditetapkan"}</strong>
          </p>
          {members.data && (
            <>
              <DataTable
                label="Anggota group"
                rowKey={m => m.id}
                rows={members.data}
                empty="Group belum mempunyai anggota."
                columns={[
                  { label: "User", render: m => `${m.username} (#${m.id})` },
                  { label: "Status", render: m => (m.is_active ? "Aktif" : "Nonaktif") },
                  {
                    label: "Tindakan",
                    render: m => (
                      <div className="actions">
                        {leader.data?.user_id === m.id ? (
                          <span className="badge badge-run">Leader</span>
                        ) : (
                          <button type="button" disabled={!m.is_active || setLeader.isPending} onClick={() => setLeader.mutate(m.id)}>
                            Jadikan leader
                          </button>
                        )}
                        <button type="button" className="danger" onClick={() => confirm(`Keluarkan ${m.username} dari group?`) && removeMember.mutate(m.id)}>
                          Keluarkan
                        </button>
                      </div>
                    ),
                  },
                ]}
              />
              <Pager offset={memberOffset} size={SIZE} count={members.data.length} onChange={setMemberOffset} />
            </>
          )}
        </Card>
        <Card title="Akses database">
          <form
            className="inline-form"
            onSubmit={(e: FormEvent<HTMLFormElement>) => {
              e.preventDefault();
              const form = new FormData(e.currentTarget);
              setGrant.mutate({ db: Number(form.get("database")), level: form.get("level") as AccessLevel });
            }}
          >
            <label>
              Database
              <select name="database" required>
                <option value="">Pilih database</option>
                {connections.data?.map(c => (
                  <option key={c.id} value={c.id}>
                    {c.name} (#{c.id})
                  </option>
                ))}
              </select>
            </label>
            <label>
              Tingkat akses
              <select name="level">
                <option>View Only</option>
                <option>View + Edit</option>
              </select>
            </label>
            <button className="primary" type="submit" disabled={setGrant.isPending}>
              Simpan akses
            </button>
          </form>
          {grants.data && (
            <>
              <DataTable
                label="Akses database group"
                rowKey={g => g.database_id}
                rows={grants.data}
                empty="Group belum mempunyai akses database."
                columns={[
                  { label: "Database", render: g => connectionName(g.database_id) },
                  { label: "Akses", render: g => <AccessBadge level={g.access_level} /> },
                  {
                    label: "Tindakan",
                    render: g => (
                      <div className="actions">
                        <button type="button" onClick={() => setGrant.mutate({ db: g.database_id, level: g.access_level === "View Only" ? "View + Edit" : "View Only" })}>
                          Ubah ke {g.access_level === "View Only" ? "View + Edit" : "View Only"}
                        </button>
                        <button type="button" className="danger" onClick={() => confirm(`Cabut akses ke ${connectionName(g.database_id)}?`) && revoke.mutate(g.database_id)}>
                          Cabut
                        </button>
                      </div>
                    ),
                  },
                ]}
              />
              <Pager offset={grantOffset} size={SIZE} count={grants.data.length} onChange={setGrantOffset} />
            </>
          )}
        </Card>
      </div>
    </>
  );
}
