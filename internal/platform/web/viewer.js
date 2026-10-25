"use strict";
(() => {
  const app = window.PlatformB, $ = app.$;
  let databases = [], offset = 0, generation = 0, controller = null;
  function requestScope() { if (controller) controller.abort(); controller = new AbortController(); return { generation: ++generation, signal: controller.signal }; }
  const current = scope => scope.generation === generation;
  function clearResult() { $("viewer-result").hidden = true; ["rows", "columns", "constraints", "indexes"].forEach(name => $(`viewer-${name}`).replaceChildren()); }
  function clearSelection(select, label) { app.options(select, [], row => row, row => row, label); }
  function status(text) { $("viewer-status").textContent = text; }
  function guarded(callback) { return app.action(async () => { try { await callback(); } catch (error) { if (error.name !== "AbortError") { clearResult(); if (error.status === 403) { databases = []; clearSelection($("viewer-db"), "Akses ditolak; muat ulang"); } status(error.message); throw error; } } }); }
  async function reload() {
    const scope = requestScope(); offset = 0; clearResult(); clearSelection($("viewer-schema"), "Pilih schema"); clearSelection($("viewer-table"), "Pilih table"); status("Memuat database yang boleh diakses…");
    const rows = await app.api("/api/viewer/databases", { signal: scope.signal }); if (!current(scope)) return;
    databases = rows; app.options($("viewer-db"), rows, row => row.database_id, row => row.name, "Pilih database"); $("viewer-access").textContent = "Belum dipilih"; status(rows.length ? "Pilih database untuk melihat schema." : "Belum ada database yang dapat diakses melalui group.");
    window.dispatchEvent(new CustomEvent("platform:databases", { detail: rows }));
  }
  async function selectDatabase() {
    const scope = requestScope(), id = $("viewer-db").value; clearResult(); offset = 0; clearSelection($("viewer-schema"), "Pilih schema"); clearSelection($("viewer-table"), "Pilih table");
    const database = databases.find(row => String(row.database_id) === id); $("viewer-access").textContent = database?.access_level || "Belum dipilih";
    if (!id) { status("Pilih database."); return; } status("Memuat schema…");
    const rows = await app.api(`/api/viewer/databases/${encodeURIComponent(id)}/schemas`, { signal: scope.signal }); if (!current(scope)) return;
    app.options($("viewer-schema"), rows, row => row.name, row => row.name, "Pilih schema"); status(rows.length ? "Pilih schema." : "Tidak ada schema pengguna yang ditampilkan pada database ini.");
  }
  async function selectSchema() {
    const scope = requestScope(), id = $("viewer-db").value, schema = $("viewer-schema").value; clearResult(); offset = 0; clearSelection($("viewer-table"), "Pilih table");
    if (!schema) { status("Pilih schema."); return; } status("Memuat table…");
    const rows = await app.api(`/api/viewer/databases/${encodeURIComponent(id)}/schemas/${encodeURIComponent(schema)}/tables`, { signal: scope.signal }); if (!current(scope)) return;
    app.options($("viewer-table"), rows, row => row.name, row => `${row.name} · ${row.type}`, "Pilih table"); status(rows.length ? "Pilih table." : "Schema ini belum mempunyai table yang didukung viewer.");
  }
  async function readTable() {
    const scope = requestScope(), id = $("viewer-db").value, schema = $("viewer-schema").value, table = $("viewer-table").value;
    clearResult(); if (!table) { status("Pilih table."); return; } status("Memuat struktur dan satu halaman data…");
    const base = app.endpoint(id, schema, table), limit = Number($("viewer-limit").value);
    const [metadata, data] = await Promise.all([app.api(base, { signal: scope.signal }), app.api(`${base}/data?limit=${limit}&offset=${offset}`, { signal: scope.signal })]);
    if (!current(scope)) return;
    const columns = data.columns.map((column, index) => ({ label: `${column.name} · ${column.data_type}`, value: row => row[index] }));
    app.table($("viewer-rows"), columns, data.rows, "Table ini tidak mempunyai data pada halaman yang dipilih.");
    $("viewer-page").textContent = data.pagination.returned_rows ? `Baris ${data.pagination.offset + 1}–${data.pagination.offset + data.pagination.returned_rows}` : "Halaman kosong";
    $("viewer-prev").disabled = offset === 0; $("viewer-next").disabled = !data.pagination.has_more;
    $("viewer-order").textContent = data.stable_order ? `Urutan primary key: ${data.order_by.join(", ")}. Perubahan data dapat menggeser halaman.` : "Table tidak memiliki primary key yang sesuai; urutan antarpanggilan tidak dijamin.";
    app.table($("viewer-columns"), [{ label: "Urutan", value: c => c.ordinal_position }, { label: "Nama", value: c => c.name }, { label: "Tipe", value: c => c.data_type }, { label: "Nullable", value: c => c.nullable ? "Ya" : "Tidak" }, { label: "Default", value: c => c.default }, { label: "Identity / generated", value: c => [c.identity, c.generated, c.generation_expression].filter(Boolean).join(" · ") || "—" }], metadata.columns);
    app.table($("viewer-constraints"), [{ label: "Nama", value: c => c.name }, { label: "Jenis", value: c => c.type }, { label: "Kolom", value: c => c.columns.join(", ") }, { label: "Referensi", value: c => c.reference ? `${c.reference.schema}.${c.reference.table} (${c.reference.columns.join(", ")})` : "—" }, { label: "Definisi", value: c => c.definition }, { label: "Valid", value: c => c.validated ? "Ya" : "Belum divalidasi" }], metadata.constraints, "Table ada dan tidak memiliki constraint yang ditampilkan.");
    app.table($("viewer-indexes"), [{ label: "Nama", value: i => i.name }, { label: "Metode", value: i => i.method }, { label: "Unique", value: i => i.unique ? "Ya" : "Tidak" }, { label: "Primary", value: i => i.primary ? "Ya" : "Tidak" }, { label: "Valid / ready", value: i => `${i.valid ? "Valid" : "Invalid"} / ${i.ready ? "Ready" : "Belum ready"}` }, { label: "Definisi", value: i => i.definition }, { label: "Predicate", value: i => i.predicate }], metadata.indexes, "Table ada dan tidak memiliki index.");
    $("viewer-result").hidden = false; status(`${schema}.${table} · ${data.pagination.returned_rows} baris pada halaman ini.`);
  }
  document.addEventListener("DOMContentLoaded", () => {
    $("viewer-refresh").addEventListener("click", () => guarded(reload)); $("viewer-db").addEventListener("change", () => guarded(selectDatabase)); $("viewer-schema").addEventListener("change", () => guarded(selectSchema)); $("viewer-table").addEventListener("change", () => { offset = 0; guarded(readTable); });
    $("viewer-limit").addEventListener("change", () => { offset = 0; guarded(readTable); }); $("viewer-prev").addEventListener("click", () => { offset = Math.max(0, offset - Number($("viewer-limit").value)); guarded(readTable); }); $("viewer-next").addEventListener("click", () => { offset += Number($("viewer-limit").value); guarded(readTable); });
    document.querySelectorAll("[data-view]").forEach(button => button.addEventListener("click", () => { document.querySelectorAll("[data-view]").forEach(other => other.setAttribute("aria-pressed", String(other === button))); ["data", "columns", "constraints", "indexes"].forEach(name => $(`viewer-${name}`).hidden = name !== button.dataset.view); }));
  });
  window.addEventListener("platform:ready", () => guarded(reload));
  window.addEventListener("platform:invalidate", () => { requestScope(); clearResult(); databases = []; clearSelection($("viewer-db"), "Perbarui akses"); clearSelection($("viewer-schema"), "Pilih schema"); clearSelection($("viewer-table"), "Pilih table"); $("viewer-access").textContent = "Akses perlu diperiksa ulang"; status("Data dibersihkan; perbarui akses atau login kembali."); });
})();
