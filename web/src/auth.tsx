import { createContext, useContext, useEffect, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, UNAUTHORIZED_EVENT, type Me } from "./api";

type Session = {
  me: Me | null;
  loading: boolean;
  error: Error | null;
  refresh: () => Promise<unknown>;
  logout: () => Promise<void>;
};

const SessionContext = createContext<Session | null>(null);

// Identitas selalu dibaca dari backend; role di frontend hanya menentukan tampilan,
// otorisasi tetap diperiksa backend pada setiap API.
export function SessionProvider({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  const query = useQuery({
    queryKey: ["me"],
    queryFn: async ({ signal }) => {
      try {
        return await api.me(signal);
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
    retry: false,
    staleTime: 60_000,
  });

  useEffect(() => {
    // Session kedaluwarsa di tengah pemakaian: bersihkan seluruh cache data agar tidak tampil ke user berikutnya.
    const expire = () => {
      client.removeQueries({ predicate: q => q.queryKey[0] !== "me" });
      client.setQueryData(["me"], null);
    };
    window.addEventListener(UNAUTHORIZED_EVENT, expire);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, expire);
  }, [client]);

  const value: Session = {
    me: query.data ?? null,
    loading: query.isPending,
    error: query.error,
    refresh: () => client.invalidateQueries({ queryKey: ["me"] }),
    logout: async () => {
      try {
        await api.logout();
      } finally {
        client.clear();
        client.setQueryData(["me"], null);
      }
    },
  };
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): Session {
  const session = useContext(SessionContext);
  if (!session) throw new Error("SessionProvider belum dipasang");
  return session;
}

/** Halaman awal sesuai role: Platform Admin ke Admin Console, user lain ke Workspace. */
export function homeFor(me: Me): string {
  return me.is_platform_admin ? "/admin/users" : "/databases";
}
