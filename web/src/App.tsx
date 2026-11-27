import { lazy, Suspense, type ReactNode } from "react";
import { Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import { homeFor, useSession } from "./auth";
import { Shell, Splash, Notice } from "./ui";
import { LoginPage } from "./pages/LoginPage";
import { DatabasesPage } from "./pages/DatabasesPage";
import { MigrationsPage } from "./pages/MigrationsPage";
import { JobPage } from "./pages/JobPage";
import { AnalysisPage } from "./pages/AnalysisPage";

// Admin Console dimuat sebagai chunk terpisah: user biasa tidak pernah mengunduh kode halaman admin.
const AdminConsole = lazy(() => import("./pages/admin/AdminConsole"));

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireSession />}>
        <Route index element={<RoleHome />} />
        <Route element={<WorkspaceLayout />}>
          <Route path="databases/:dbId?" element={<DatabasesPage />} />
          <Route path="migrations/:dbId?" element={<MigrationsPage />} />
          <Route path="migrations/:dbId/jobs/:jobId" element={<JobPage />} />
          <Route path="analysis/:dbId?" element={<AnalysisPage />} />
        </Route>
        <Route
          path="admin/*"
          element={
            <RequireAdmin>
              <Suspense fallback={<Splash text="Memuat Admin Console…" />}>
                <AdminConsole />
              </Suspense>
            </RequireAdmin>
          }
        />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
  );
}

function RequireSession() {
  const { me, loading, error } = useSession();
  const location = useLocation();
  if (loading) return <Splash text="Memeriksa session…" />;
  if (error && !me) return <Splash text={`Backend tidak dapat dihubungi: ${error.message}`} />;
  if (!me) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  return <Outlet />;
}

function RequireAdmin({ children }: { children: ReactNode }) {
  const { me } = useSession();
  if (!me?.is_platform_admin) return <Navigate to="/databases" replace state={{ notice: "Halaman tersebut khusus Platform Admin." }} />;
  return <>{children}</>;
}

function RoleHome() {
  const { me } = useSession();
  return <Navigate to={homeFor(me!)} replace />;
}

function WorkspaceLayout() {
  const { me } = useSession();
  const location = useLocation();
  const notice = (location.state as { notice?: string } | null)?.notice;
  return (
    <Shell
      area="Workspace"
      links={[
        { to: "/databases", label: "Database" },
        { to: "/migrations", label: "Migration" },
        { to: "/analysis", label: "Analisis query" },
      ]}
      switchTo={me?.is_platform_admin ? { to: "/admin/users", label: "Admin Console" } : undefined}
    >
      {notice && <Notice tone="error">{notice}</Notice>}
      <Outlet />
    </Shell>
  );
}

function NotFound() {
  return (
    <main className="splash">
      <h1>Halaman tidak ditemukan</h1>
      <a href="/">Kembali ke beranda</a>
    </main>
  );
}
