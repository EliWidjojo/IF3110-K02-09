import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "./auth";
import { App } from "./App";
import { hiddenRollbackPairs } from "./pages/MigrationsPage";
import type { Attempt, Job } from "./api";

const attempt = (id: number, version: number, status = "SUCCESS"): Attempt => ({
  id, version, status, executed_by: 1, executed_at: "2026-11-25T00:00:00Z", finished_at: null, query_sql: "", sql_state: "", error_code: "",
});
const job = (id: number, attemptId: number, operation: "apply" | "rollback" | "forward", source = 0, status: Job["status"] = "SUCCESS") =>
  ({ id, attempt_id: attemptId, status, submission: { version: id, query_sql: "", operation, source_job: source, non_atomic_ddl: false } }) as Job;

describe("hide rollback", () => {
  // Contoh spesifikasi: A[E1], B[E2], B (rollback E2)[E3], B (forward)[E4], C[E5] → E2 dan E3 disembunyikan.
  it("menyembunyikan pasangan eksekusi dan rollback suksesnya berdasarkan execution ID", () => {
    const attempts = [1, 2, 3, 4, 5].map(id => attempt(id, id));
    const jobs = [job(10, 1, "apply"), job(11, 2, "apply"), job(12, 3, "rollback", 11), job(13, 4, "forward", 12), job(14, 5, "apply")];
    expect([...hiddenRollbackPairs(attempts, jobs)].sort()).toEqual([2, 3]);
  });

  it("tidak menyembunyikan rollback yang gagal", () => {
    const attempts = [attempt(1, 1), attempt(2, 2, "FAILED")];
    const jobs = [job(10, 1, "apply"), job(11, 2, "rollback", 10, "FAILED")];
    expect(hiddenRollbackPairs(attempts, jobs).size).toBe(0);
  });

  it("memakai field pasangan dari history bila backend menyediakannya", () => {
    const attempts = [attempt(1, 1), { ...attempt(2, 2), operation: "rollback" as const, reverts_attempt_id: 1 }];
    expect([...hiddenRollbackPairs(attempts, [])].sort()).toEqual([1, 2]);
  });
});

function renderAt(path: string, me: Record<string, unknown>) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const body = url.startsWith("/api/platform/me") ? { data: me } : { data: [] };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <SessionProvider>
          <App />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("pemisahan halaman per role", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("user biasa yang membuka /admin diarahkan ke Workspace tanpa menu admin", async () => {
    renderAt("/admin/users", { id: 2, username: "alice", is_active: true, is_platform_admin: false, leader_groups: [] });
    expect(await screen.findByText("Database viewer")).toBeTruthy();
    expect(screen.getByText("Halaman tersebut khusus Platform Admin.")).toBeTruthy();
    expect(screen.queryByText("Admin Console")).toBeNull();
    expect(screen.queryByText("Koneksi database")).toBeNull();
  });

  it("Platform Admin mendarat di Admin Console dengan navigasi sendiri", async () => {
    renderAt("/", { id: 1, username: "admin", is_active: true, is_platform_admin: true, leader_groups: [] });
    expect(await screen.findByRole("heading", { name: "User" })).toBeTruthy();
    expect(screen.getByText("Admin Console")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Koneksi database" })).toBeTruthy();
  });

  // Regresi: login sukses harus berpindah halaman tanpa refresh (dulu client.clear() memutus observer session).
  it("login sukses langsung masuk ke halaman sesuai role", async () => {
    let loggedIn = false;
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (url === "/api/auth/csrf") return json({ csrf_token: "t" });
      if (url === "/api/auth/login") {
        loggedIn = true;
        return json({ data: { csrf_token: "t2" } });
      }
      if (url === "/api/platform/me") return loggedIn ? json({ data: { id: 2, username: "alice", is_active: true, is_platform_admin: false, leader_groups: [] } }) : json({ error: { message: "authentication required" } }, 401);
      return json({ data: [] });
    }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/login"]}>
          <SessionProvider>
            <App />
          </SessionProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await screen.findByRole("heading", { name: "Masuk ke Database Platform" });
    const form = container.querySelector("form")!;
    (form.elements.namedItem("username") as HTMLInputElement).value = "alice";
    (form.elements.namedItem("password") as HTMLInputElement).value = "rahasia-uji";
    form.requestSubmit();
    expect(await screen.findByText("Database viewer")).toBeTruthy();
  });

  it("tanpa session diarahkan ke halaman login", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { message: "authentication required" } }), { status: 401 })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/migrations/1"]}>
          <SessionProvider>
            <App />
          </SessionProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("heading", { name: "Masuk ke Database Platform" })).toBeTruthy();
  });
});
