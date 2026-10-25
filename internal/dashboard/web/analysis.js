"use strict";
(() => {
  const app = window.PlatformB, $ = app.$;
  let generation = 0, controller = null, databases = [];
  function clear() { generation++; controller?.abort(); $("analysis-result").hidden = true; $("analysis-tree").replaceChildren(); $("analysis-raw").textContent = ""; $("analysis-summary").replaceChildren(); }
  const available = value => value === null || value === undefined ? "Tidak tersedia" : value;
  function render(result) {
    $("analysis-summary").replaceChildren(app.element("h2", `${result.analyze ? "EXPLAIN ANALYZE" : "EXPLAIN"} · ${result.statement_type}`), app.element("p", `Planning: ${available(result.planning_time_ms)} ms · Execution: ${available(result.execution_time_ms)} ms`));
    $("analysis-raw").textContent = result.raw_json;
    let count = 0;
    function node(plan, depth = 0) {
      if (!plan || depth > 64 || ++count > 1000) return app.element("p", "Tampilan tree dibatasi; lihat JSON mentah untuk hasil lengkap.");
      const detail = app.element("details", "", { class: "plan-node" }); detail.open = depth < 3;
      detail.append(app.element("summary", [plan.node_type || "Node", plan.relation, plan.index ? `index ${plan.index}` : "", plan.join ? `join ${plan.join}` : ""].filter(Boolean).join(" · ")));
      const list = app.element("dl");
      for (const [label, value] of [["Startup cost", plan.startup_cost], ["Total cost", plan.total_cost], ["Estimated rows", plan.estimated_rows], ["Actual rows / loop", plan.actual_rows], ["Actual time / loop (ms)", plan.actual_time_ms]]) { const group = app.element("div"); group.append(app.element("dt", label), app.element("dd", available(value))); list.append(group); }
      detail.append(list); for (const child of plan.children || []) detail.append(node(child, depth + 1)); return detail;
    }
    $("analysis-tree").replaceChildren(result.plan ? node(result.plan) : app.element("p", "Bentuk plan ini tidak menyediakan node utama. Hasil JSON mentah tetap tersedia.")); $("analysis-result").hidden = false;
  }
  async function run() {
    clear(); const ticket = generation, id = $("analysis-db").value;
    if (!databases.some(db => String(db.database_id) === id && db.access_level === "View + Edit" && db.driver === "postgres")) throw new Error("Pilih database dengan akses View + Edit.");
    const query = $("analysis-sql").value; if (new TextEncoder().encode(query).length > 12288) throw new Error("SQL maksimum 12 KiB.");
    controller = new AbortController(); $("analysis-status").textContent = "Menganalisis query…";
    try { const result = await app.api(`/api/viewer/databases/${encodeURIComponent(id)}/explain`, { method: "POST", body: { query, analyze: $("analysis-analyze").checked }, signal: controller.signal }); if (ticket !== generation) return; render(result); $("analysis-status").textContent = "Analisis selesai. Tidak ada migration yang dieksekusi atau dicatat."; }
    catch (error) { if (error.name !== "AbortError") { $("analysis-status").textContent = error.message; throw error; } }
  }
  document.addEventListener("DOMContentLoaded", () => { $("analysis-form").addEventListener("submit", event => { event.preventDefault(); app.action(run, event.submitter); }); $("analysis-db").addEventListener("change", clear); });
  window.addEventListener("platform:databases", event => { databases = event.detail; app.options($("analysis-db"), databases.filter(db => db.access_level === "View + Edit" && db.driver === "postgres"), db => db.database_id, db => db.name, "Pilih database dengan View + Edit"); clear(); });
  window.addEventListener("platform:invalidate", () => { clear(); databases = []; app.options($("analysis-db"), [], db => db.database_id, db => db.name, "Akses perlu diperiksa ulang"); });
  // Editor C mengirim SQL ke panel tanpa mengeksekusinya otomatis atau menyerahkan identitas pengguna melalui event.
  window.PlatformAnalysis = { open({ databaseId, sql }) { app.show("analysis"); clear(); if (!databases.some(db => String(db.database_id) === String(databaseId) && db.access_level === "View + Edit" && db.driver === "postgres")) { app.notice("Database editor tidak tersedia dengan akses View + Edit. Muat ulang akses.", true); return; } $("analysis-db").value = String(databaseId); $("analysis-sql").value = String(sql); $("analysis-analyze").checked = false; $("analysis-sql").focus(); } };
  window.addEventListener("platform:analyze", event => { if (event.detail && typeof event.detail.sql === "string") window.PlatformAnalysis.open(event.detail); });
})();
