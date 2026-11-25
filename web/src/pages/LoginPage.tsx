import { useState, type FormEvent } from "react";
import { Navigate, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { api, type Me } from "../api";
import { homeFor, useSession } from "../auth";
import { Notice, Splash } from "../ui";

export function LoginPage() {
  const { me, loading } = useSession();
  const client = useQueryClient();
  const [params] = useSearchParams();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (loading) return <Splash text="Memeriksa session…" />;
  const next = params.get("next");
  // Hanya path internal; parameter next tidak boleh mengarah ke origin lain.
  const safeNext = next && next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/login") ? next : null;
  if (me) return <Navigate to={safeNext ?? homeFor(me)} replace />;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    setError("");
    try {
      await api.login(String(data.get("username") ?? "").trim(), String(data.get("password") ?? ""));
      client.clear();
      // Setelah identitas terbaca, render ulang mengarahkan ke halaman sesuai role.
      await client.fetchQuery<Me | null>({ queryKey: ["me"], queryFn: ({ signal }) => api.me(signal) });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Login gagal");
    } finally {
      (form.elements.namedItem("password") as HTMLInputElement).value = "";
      setBusy(false);
    }
  }

  return (
    <main className="login">
      <section className="login-card" aria-labelledby="login-title">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">A</span>
          <span>Atlas</span>
        </div>
        <h1 id="login-title">Masuk ke Database Platform</h1>
        <p className="muted">Gunakan akun yang dibuat Platform Admin. Hak akses database ditentukan melalui group.</p>
        {error && <Notice tone="error">{error === "invalid username or password" ? "Username atau password salah." : error}</Notice>}
        <form onSubmit={submit} className="stack">
          <label>
            Username
            <input name="username" autoComplete="username" required maxLength={128} autoFocus />
          </label>
          <label>
            Password
            <input name="password" type="password" autoComplete="current-password" required maxLength={1024} />
          </label>
          <button className="primary" type="submit" disabled={busy}>
            {busy ? "Memproses…" : "Masuk"}
          </button>
        </form>
      </section>
    </main>
  );
}
