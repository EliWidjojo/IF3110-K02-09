"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const app = window.PlatformB = { $, user: null, panel: "viewer" };
  app.notice = (message, error = false) => { $("notice").textContent = message; $("notice").classList.toggle("error", error); $("notice").setAttribute("role", error ? "alert" : "status"); };
  app.element = (tag, text = "", attrs = {}) => { const node = document.createElement(tag); node.textContent = text; for (const [name, value] of Object.entries(attrs)) node.setAttribute(name, value); return node; };
  app.button = (text, callback, danger = false) => { const button = app.element("button", text, { type: "button" }); if (danger) button.className = "danger"; button.addEventListener("click", () => app.action(() => callback(button), button)); return button; };
  app.action = async (callback, button) => { if (button) button.disabled = true; try { return await callback(); } catch (error) { if (error.name !== "AbortError") app.notice(error.message || "Operasi gagal. Silakan coba kembali.", true); return undefined; } finally { if (button) button.disabled = false; } };
  app.invalidate = () => { window.dispatchEvent(new Event("platform:invalidate")); };
  async function fetchJSON(path, options = {}) {
    const response = await fetch(path, { credentials: "same-origin", cache: "no-store", ...options });
    let data;
    try { data = response.status === 204 ? {} : await response.json(); } catch { throw new Error("Respons server tidak dapat dibaca. Periksa pemasangan route backend."); }
    if (!response.ok) {
      if (response.status === 401 || response.status === 403) { app.invalidate(); app.user = null; syncAdmin(); $("identity").textContent = response.status === 401 ? "Session tidak berlaku. Silakan login." : "Perbarui identitas dan hak akses."; }
      const error = new Error(typeof data.error === "string" ? data.error : data.error?.message || `Operasi ditolak (${response.status}).`); error.status = response.status; error.code = data.error?.code; throw error;
    }
    return data;
  }
  app.api = async (path, options = {}) => {
    const method = options.method || "GET";
    const headers = { ...options.headers };
    if (method !== "GET") {
      // Token berasal dari endpoint A dan terikat cookie session yang dikirim browser pada origin yang sama.
      const token = await fetchJSON("/api/auth/csrf", { signal: options.signal });
      if (typeof token.csrf_token !== "string" || !token.csrf_token) throw new Error("Token CSRF modul authentication belum tersedia.");
      headers["X-CSRF-Token"] = token.csrf_token;
    }
    let body;
    if (options.body !== undefined) { headers["Content-Type"] = "application/json"; body = JSON.stringify(options.body); }
    const result = await fetchJSON(path, { ...options, headers, body });
    return result.data === undefined ? result : result.data;
  };
  app.valueText = value => {
    if (value === null || value === undefined) return "NULL";
    if (typeof value === "object" && "type" in value && "value" in value) return value.type === "binary" ? `Base64 · ${value.value || "(kosong)"}` : String(value.value);
    return typeof value === "object" ? JSON.stringify(value, null, 2) : String(value);
  };
  app.cell = value => {
    const text = app.valueText(value);
    if (text.length < 160 && !text.includes("\n")) return app.element("span", text, value === null ? { class: "muted" } : {});
    const detail = app.element("details"); detail.append(app.element("summary", `${text.slice(0, 90)}…`), app.element("pre", text)); return detail;
  };
  app.table = (container, columns, rows, empty = "Belum ada data.") => {
    container.replaceChildren(); if (!rows.length) { container.append(app.element("p", empty, { class: "muted" })); return; }
    const table = app.element("table"), head = app.element("thead"), header = app.element("tr"), body = app.element("tbody");
    columns.forEach(column => header.append(app.element("th", column.label, { scope: "col" }))); head.append(header);
    rows.forEach(row => { const tr = app.element("tr"); columns.forEach(column => { const td = app.element("td"), value = column.value(row); td.append(value instanceof Node ? value : app.cell(value)); tr.append(td); }); body.append(tr); });
    table.append(head, body); container.append(table);
  };
  app.options = (select, rows, value, label, placeholder = "Pilih") => { select.replaceChildren(app.element("option", placeholder, { value: "" })); rows.forEach(row => select.append(app.element("option", label(row), { value: value(row) }))); select.disabled = rows.length === 0; };
  app.endpoint = (id, schema, table) => `/api/viewer/databases/${encodeURIComponent(id)}/schemas/${encodeURIComponent(schema)}/tables/${encodeURIComponent(table)}`;
  app.show = panel => {
    if (["users", "groups", "connections"].includes(panel) && !app.user?.is_platform_admin) { app.notice("Akses Platform Admin diperlukan.", true); return; }
    app.panel = panel; document.querySelectorAll(".panel").forEach(node => node.hidden = node.id !== `panel-${panel}`);
    document.querySelectorAll("[data-panel]").forEach(button => { if (button.dataset.panel === panel) button.setAttribute("aria-current", "page"); else button.removeAttribute("aria-current"); });
    window.dispatchEvent(new CustomEvent("platform:panel", { detail: panel }));
  };
  function syncAdmin() { document.querySelectorAll("[data-admin]").forEach(node => node.hidden = !app.user?.is_platform_admin); if (!app.user?.is_platform_admin && ["users", "groups", "connections"].includes(app.panel)) app.show("viewer"); }
  async function identity() {
    app.invalidate(); app.user = null; syncAdmin(); $("identity").textContent = "Memeriksa identitas…";
    try { app.user = await app.api("/api/platform/me"); syncAdmin(); $("identity").textContent = `${app.user.username} · ${app.user.is_platform_admin ? "Platform Admin" : "Pengguna"}`; app.notice("Terhubung. Hak akses dibaca kembali pada setiap operasi."); window.dispatchEvent(new Event("platform:ready")); }
    catch (error) { $("identity").textContent = "Belum terhubung"; app.notice(error.message, true); }
  }
  document.addEventListener("DOMContentLoaded", () => { document.querySelectorAll("[data-panel]").forEach(button => button.addEventListener("click", () => app.show(button.dataset.panel))); $("refresh-session").addEventListener("click", identity); identity(); });
})();
