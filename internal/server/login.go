package server

import "net/http"

func loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html lang="id"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Database Platform · Login</title><link rel="stylesheet" href="/platform/assets/dashboard.css"><script src="/auth.js" defer></script></head><body class="login-screen"><main class="login-page"><div class="card"><h1>Database Management Platform</h1><p>Masuk ke akun Anda</p><p id="status" role="status" aria-live="polite">Memeriksa session…</p><form id="login" hidden><p><label>Username <input name="username" autocomplete="username" required maxlength="64"></label></p><p><label>Password <input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label></p><button class="primary" type="submit">Masuk</button></form><section id="account" hidden><h2>Akun aktif</h2><pre id="identity"></pre><button id="logout" type="button">Keluar</button></section></div></main></body></html>`))
}
func loginScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`"use strict";
let csrf = "";
const status = document.querySelector("#status"), form = document.querySelector("#login"), account = document.querySelector("#account");
async function request(path, options = {}) {
  const response = await fetch(path, { credentials: "same-origin", ...options });
  const data = response.status === 204 ? {} : await response.json();
  if (!response.ok) throw new Error(data.error?.message || data.error || "Request gagal");
  return data;
}
async function token() { csrf = (await request("/api/auth/csrf")).csrf_token; }
function show(user) { form.hidden = !!user; account.hidden = !user; document.querySelector("#identity").textContent = user ? JSON.stringify(user, null, 2) : ""; status.textContent = user ? "Login berhasil." : "Silakan masuk."; }
form.addEventListener("submit", async event => {
  event.preventDefault(); const button = form.querySelector("button"); button.disabled = true;
  try {
    await token();
    const user = await request("/api/auth/login", {method:"POST", headers:{"Content-Type":"application/json","X-CSRF-Token":csrf}, body:JSON.stringify({username:form.elements.username.value,password:form.elements.password.value})});
    csrf = user.csrf_token; window.location.assign("/platform/");
  } catch (error) { status.textContent = error.message; }
  finally { form.elements.password.value = ""; button.disabled = false; }
});
document.querySelector("#logout").addEventListener("click", async () => {
  try { await token(); await request("/api/auth/logout", {method:"POST",headers:{"X-CSRF-Token":csrf}}); csrf = ""; show(null); }
  catch(error) { status.textContent = error.message; }
});
(async () => { try { await token(); await request("/api/auth/me"); window.location.replace("/platform/"); } catch { show(null); } })();`))
}
