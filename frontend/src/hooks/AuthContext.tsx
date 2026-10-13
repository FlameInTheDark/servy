import { createContext, useContext, useEffect, useState, ReactNode } from "react";
import { api, setToken, getToken, GET, POST, UserView } from "@/api/client";

interface AuthCtx {
  user: UserView | null;
  serverIds: string[] | null;
  loading: boolean;
  needsSetup: boolean;
  login: (username: string, password: string, totp?: string) => Promise<void>;
  logout: () => void;
  refresh: () => Promise<void>;
}

const Ctx = createContext<AuthCtx>(null as any);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<UserView | null>(null);
  const [serverIds, setServerIds] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [needsSetup, setNeedsSetup] = useState(false);

  const refresh = async () => {
    if (!getToken()) {
      setLoading(false);
      return;
    }
    try {
      const me = await GET<any>("/users/@me");
      setUser(me);
      // fetch servers list for sidebar
      const servers = await GET<any[]>("/servers");
      setServerIds(servers.map((s) => s.server_id));
      // check if setup is still needed (after initial setup completes)
      const setupStatus = await GET<any>("/setup/status");
      setNeedsSetup(setupStatus.needs_setup);
    } catch {
      setToken(null);
      setUser(null);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    // check setup status first
    GET<any>("/setup/status")
      .then((s) => setNeedsSetup(s.needs_setup))
      .catch(() => {})
      .finally(() => refresh());
  }, []);

  const login = async (username: string, password: string, totp?: string) => {
    const res = await api<{ token: string; user: UserView; servers: string[] }>(
      "POST",
      "/auth/login",
      { username, password, totp_code: totp || "" }
    );
    setToken(res.token);
    setUser(res.user);
    setServerIds(res.servers);
    setNeedsSetup(false);
  };

  const logout = () => {
    POST("/auth/logout").catch(() => {});
    setToken(null);
    setUser(null);
  };

  return (
    <Ctx.Provider value={{ user, serverIds, loading, needsSetup, login, logout, refresh }}>
      {children}
    </Ctx.Provider>
  );
}

export const useAuth = () => useContext(Ctx);
