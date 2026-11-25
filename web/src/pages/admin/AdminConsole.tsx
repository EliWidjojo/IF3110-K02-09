import { Navigate, Route, Routes } from "react-router";
import { Shell } from "../../ui";
import { UsersPage } from "./UsersPage";
import { GroupsPage, GroupDetailPage } from "./GroupsPage";
import { ConnectionsPage } from "./ConnectionsPage";

// Admin Console memiliki layout, navigasi, dan halaman sendiri; hanya dimuat untuk Platform Admin.
export default function AdminConsole() {
  return (
    <Shell
      area="Admin Console"
      links={[
        { to: "/admin/users", label: "User" },
        { to: "/admin/groups", label: "Group & akses" },
        { to: "/admin/connections", label: "Koneksi database" },
      ]}
      switchTo={{ to: "/databases", label: "Workspace" }}
    >
      <Routes>
        <Route index element={<Navigate to="users" replace />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="groups" element={<GroupsPage />} />
        <Route path="groups/:groupId" element={<GroupDetailPage />} />
        <Route path="connections" element={<ConnectionsPage />} />
        <Route path="*" element={<Navigate to="users" replace />} />
      </Routes>
    </Shell>
  );
}
