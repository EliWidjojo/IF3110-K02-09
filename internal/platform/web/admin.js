"use strict";
(() => {
  const app = window.PlatformB, $ = app.$, size = 20, offsets = { users: 0, groups: 0, connections: 0, members: 0, grants: 0 };
  let selectedGroup = null, epoch = 0;
  const requests = {};
  const path = { users: "/api/admin/users", groups: "/api/admin/groups", connections: "/api/admin/databases" };
  const actions = buttons => { const node = app.element("div", "", { class: "actions" }); node.append(...buttons); return node; };
  const success = text => app.notice(text);
  function pager(kind, count) { $(`${kind}-page`).textContent = count ? `Baris ${offsets[kind] + 1}–${offsets[kind] + count}` : "Tidak ada hasil pada halaman ini"; $(`${kind}-prev`).disabled = offsets[kind] === 0; $(`${kind}-next`).disabled = count < size; }
  function resetUser() { $("user-form").reset(); $("user-id").value = ""; $("user-password-label").hidden = false; $("user-password").required = true; $("user-password").disabled = false; $("user-active-label").hidden = true; $("user-form-title").textContent = "Buat user"; }
  function editUser(user) { resetUser(); $("user-id").value = user.id; $("user-name").value = user.username; $("user-admin").checked = user.is_platform_admin; $("user-active").checked = user.is_active; $("user-active-label").hidden = false; $("user-password-label").hidden = true; $("user-password").required = false; $("user-password").disabled = true; $("user-form-title").textContent = `Edit user #${user.id}`; $("user-name").focus(); }
  async function load(kind) {
    if (!app.user?.is_platform_admin) return;
    const ticket = epoch, request = requests[kind] = (requests[kind] || 0) + 1;
    let url = path[kind]; if (kind === "members" || kind === "grants") { if (!selectedGroup) return; url = `${path.groups}/${selectedGroup.id}/${kind === "members" ? "members" : "database-access"}`; }
    const container = $(`${kind}-list`); container.replaceChildren(app.element("p", "Memuat…", { role: "status" }));
    let rows;
    try { rows = await app.api(`${url}?limit=${size}&offset=${offsets[kind]}`); }
    catch (error) { if (ticket === epoch && request === requests[kind]) container.replaceChildren(app.element("p", error.message, { role: "status" })); throw error; }
    if (ticket !== epoch || request !== requests[kind] || !app.user?.is_platform_admin) return;
    if (kind === "users") {
      app.table(container, [{ label: "ID", value: u => u.id }, { label: "Username", value: u => u.username }, { label: "Status", value: u => u.is_active ? "Aktif" : "Nonaktif" }, { label: "Peran", value: u => u.is_platform_admin ? "Platform Admin" : "Pengguna" }, { label: "Tindakan", value: u => actions([app.button("Edit", () => editUser(u)), app.button("Nonaktifkan", async () => { if (!confirm(`Nonaktifkan user ${u.username}? Session user akan dicabut.`)) return; await app.api(`${path.users}/${u.id}/deactivate`, { method: "POST" }); await load("users"); success("User dinonaktifkan dan pencabutan session diminta."); }, true)]) }], rows);
      $("user-options").replaceChildren(...rows.map(u => app.element("option", u.username, { value: u.id })));
    } else if (kind === "groups") {
      app.table(container, [{ label: "ID", value: g => g.id }, { label: "Nama group", value: g => g.name }, { label: "Tindakan", value: g => actions([app.button("Anggota & akses", () => selectGroup(g)), app.button("Edit", () => { $("group-id").value = g.id; $("group-name").value = g.name; $("group-name").focus(); }), app.button("Hapus", async () => { if (!confirm(`Hapus group ${g.name}, seluruh keanggotaan, dan akses group?`)) return; await app.api(`${path.groups}/${g.id}`, { method: "DELETE" }); if (selectedGroup?.id === g.id) { selectedGroup = null; epoch++; $("group-detail").hidden = true; } await load("groups"); success("Group dan relasinya dihapus."); }, true)]) }], rows);
    } else if (kind === "connections") {
      app.table(container, [{ label: "ID / nama", value: c => `${c.id} · ${c.name}` }, { label: "Target", value: c => `${c.host}:${c.port} / ${c.database_name}` }, { label: "TLS", value: c => c.ssl_mode }, { label: "Password", value: c => c.password_configured ? "Dikonfigurasi" : "Belum tersedia" }, { label: "Tindakan", value: c => actions([app.button("Edit", () => editConnection(c)), app.button("Periksa", async () => { app.notice(`Memeriksa koneksi ${c.name}…`); await app.api(`${path.connections}/${c.id}/check-connection`, { method: "POST" }); success(`Koneksi ${c.name} berhasil.`); }), app.button("Hapus", async () => { if (!confirm(`Hapus konfigurasi ${c.name} dan akses group terkait? Riwayat migration pada target tetap dipertahankan.`)) return; await app.api(`${path.connections}/${c.id}`, { method: "DELETE" }); await load("connections"); success("Konfigurasi dihapus dan pool diinvalidasi."); }, true)]) }], rows);
      $("database-options").replaceChildren(...rows.map(c => app.element("option", c.name, { value: c.id })));
    } else if (kind === "members") {
      const group = selectedGroup;
      app.table(container, [{ label: "ID", value: u => u.id }, { label: "Anggota", value: u => u.username }, { label: "Status", value: u => u.is_active ? "Aktif" : "Nonaktif" }, { label: "Tindakan", value: u => app.button("Keluarkan", async () => { if (!confirm(`Keluarkan ${u.username} dari ${group.name}?`)) return; await app.api(`${path.groups}/${group.id}/members/${u.id}`, { method: "DELETE" }); await load("members"); success("Keanggotaan dicabut; izin diperbarui pada pemeriksaan berikutnya."); }, true) }], rows, "Group belum mempunyai anggota.");
    } else {
      const group = selectedGroup;
      app.table(container, [{ label: "Database ID", value: g => g.database_id }, { label: "Akses", value: g => g.access_level }, { label: "Tindakan", value: g => actions([app.button("Ubah", () => { $("grant-database").value = g.database_id; $("grant-level").value = g.access_level; $("grant-level").focus(); }), app.button("Cabut", async () => { if (!confirm(`Cabut akses group ${group.name} ke database #${g.database_id}?`)) return; await app.api(`${path.groups}/${group.id}/database-access/${g.database_id}`, { method: "DELETE" }); await load("grants"); success("Pemberian akses dicabut."); }, true)]) }], rows, "Group belum mempunyai akses database.");
    }
    pager(kind, rows.length);
  }
  async function selectGroup(group) { epoch++; selectedGroup = group; offsets.members = offsets.grants = 0; $("group-detail").hidden = false; $("group-detail-name").textContent = `${group.name} · #${group.id}`; await Promise.all([load("members"), load("grants")]); }
  function resetConnection() { $("connection-form").reset(); $("connection-id").value = ""; $("connection-form-title").textContent = "Tambah koneksi"; }
  function editConnection(c) { resetConnection(); $("connection-id").value = c.id; $("connection-name").value = c.name; $("connection-host").value = c.host; $("connection-port").value = c.port; $("connection-database").value = c.database_name; $("connection-username").value = c.username; $("connection-tls").value = c.ssl_mode; $("connection-cert").value = c.ssl_root_cert; $("connection-form-title").textContent = `Edit koneksi #${c.id}`; $("connection-name").focus(); }
  function form(id, callback) { $(id).addEventListener("submit", event => { event.preventDefault(); app.action(() => callback(), event.submitter); }); }
  document.addEventListener("DOMContentLoaded", () => {
    for (const kind of Object.keys(offsets)) for (const direction of ["prev", "next"]) $(`${kind}-${direction}`).addEventListener("click", () => { offsets[kind] = Math.max(0, offsets[kind] + (direction === "next" ? size : -size)); app.action(() => load(kind)); });
    $("user-cancel").addEventListener("click", resetUser); $("group-cancel").addEventListener("click", () => { $("group-form").reset(); $("group-id").value = ""; }); $("connection-cancel").addEventListener("click", resetConnection);
    form("user-form", async () => { const id = $("user-id").value; const body = { username: $("user-name").value, is_platform_admin: $("user-admin").checked }; if (id) { body.is_active = $("user-active").checked; if (!body.is_active && !confirm("Simpan user sebagai nonaktif dan cabut session miliknya?")) return; } else body.password = $("user-password").value; try { await app.api(id ? `${path.users}/${id}` : path.users, { method: id ? "PATCH" : "POST", body }); resetUser(); await load("users"); success("User disimpan."); } finally { $("user-password").value = ""; } });
    form("group-form", async () => { const id = $("group-id").value; await app.api(id ? `${path.groups}/${id}` : path.groups, { method: id ? "PATCH" : "POST", body: { name: $("group-name").value } }); $("group-form").reset(); $("group-id").value = ""; await load("groups"); success("Group disimpan."); });
    form("member-form", async () => { if (!selectedGroup) throw new Error("Pilih group terlebih dahulu."); await app.api(`${path.groups}/${selectedGroup.id}/members`, { method: "POST", body: { user_id: Number($("member-user").value) } }); $("member-form").reset(); await load("members"); success("Anggota ditambahkan."); });
    form("grant-form", async () => { if (!selectedGroup) throw new Error("Pilih group terlebih dahulu."); await app.api(`${path.groups}/${selectedGroup.id}/database-access/${encodeURIComponent($("grant-database").value)}`, { method: "PUT", body: { access_level: $("grant-level").value } }); await load("grants"); success("Akses group disimpan."); });
    form("connection-form", async () => {
      const id = $("connection-id").value, password = $("connection-password").value;
      const body = { name: $("connection-name").value, host: $("connection-host").value, port: Number($("connection-port").value), database_name: $("connection-database").value, username: $("connection-username").value, ssl_mode: $("connection-tls").value, ssl_root_cert: $("connection-cert").value };
      if (body.ssl_root_cert === "system" && body.ssl_mode !== "verify-full") throw new Error("Root certificate system memerlukan TLS verify-full.");
      if (password && $("connection-clear-password").checked) throw new Error("Pilih password baru atau pengosongan password, bukan keduanya.");
      // Password edit yang kosong tidak dikirim kecuali pengguna secara eksplisit memilih penggantian menjadi kosong.
      if (!id || password || $("connection-clear-password").checked) body.password = $("connection-clear-password").checked ? "" : password;
      try { await app.api(id ? `${path.connections}/${id}` : path.connections, { method: id ? "PATCH" : "POST", body }); resetConnection(); await load("connections"); success("Konfigurasi disimpan dan pool terkait diinvalidasi."); } finally { $("connection-password").value = ""; }
    });
  });
  window.addEventListener("platform:panel", event => { if (["users", "groups", "connections"].includes(event.detail)) app.action(() => load(event.detail)); });
  window.addEventListener("platform:invalidate", () => { epoch++; selectedGroup = null; ["users", "groups", "connections", "members", "grants"].forEach(kind => $(`${kind}-list`).replaceChildren()); $("group-detail").hidden = true; resetUser(); resetConnection(); $("group-form").reset(); $("group-id").value = ""; $("member-form").reset(); $("grant-form").reset(); });
})();
