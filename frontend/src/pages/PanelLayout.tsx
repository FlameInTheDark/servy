import { useEffect, useMemo, useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { Menu } from "lucide-react";
import { GET, realtime, ServerDef } from "@/api/client";
import { useApi } from "@/hooks/useApi";
import { useAuth } from "@/hooks/AuthContext";
import { cn } from "@/lib/utils";
import Sidebar, { DrawerCloseButton } from "@/components/layout/Sidebar";

const SIDEBAR_KEY = "servy_sidebar";
/** Rail width in px — the aside and the main area agree on it. */
const EXPANDED_W = 240;
const COLLAPSED_W = 68;

/** Server id when the current route is a server page (not the create wizard). */
function activeServerFromPath(pathname: string) {
  const match = pathname.match(/^\/servers\/([^/]+)/);
  if (!match || match[1] === "new") return "";
  return match[1];
}

export default function PanelLayout() {
  const { user, logout } = useAuth();
  const nav = useNavigate();
  const location = useLocation();
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem(SIDEBAR_KEY) === "1");
  // The drawer is open only for the route it was opened on: navigating (a link,
  // browser back/forward) closes it without an effect fighting the router.
  const [drawerPath, setDrawerPath] = useState<string | null>(null);
  const mobileOpen = drawerPath === location.pathname;
  const openDrawer = () => setDrawerPath(location.pathname);
  const closeDrawer = () => setDrawerPath(null);
  const { data: servers, setData: setServers } = useApi<ServerDef[]>("/servers", { pollMs: 10000 });

  useEffect(() => {
    localStorage.setItem(SIDEBAR_KEY, collapsed ? "1" : "0");
  }, [collapsed]);

  // Ctrl/Cmd+B toggles the rail; ignored while typing in a field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "b") return;
      const el = e.target as HTMLElement | null;
      const tag = el?.tagName?.toLowerCase();
      if (tag === "input" || tag === "textarea" || el?.isContentEditable) return;
      e.preventDefault();
      setCollapsed((c) => !c);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Status pushes: the server list in the rail follows lifecycle events.
  useEffect(() => {
    const off = realtime().on((msg) => {
      if (msg.event === "send_start_reload") {
        GET<ServerDef[]>("/servers").then(setServers).catch(() => {});
      }
    });
    return () => off();
  }, [setServers]);

  // Drawer behaviour: Escape closes it and the page behind it does not scroll.
  useEffect(() => {
    if (!mobileOpen) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setDrawerPath(null);
    window.addEventListener("keydown", onKey);
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = previous;
    };
  }, [mobileOpen]);

  const runningCount = useMemo(() => servers?.filter((s) => s.running).length ?? 0, [servers]);
  const activeServerID = activeServerFromPath(location.pathname);
  const isSuper = !!user?.superuser;

  const doLogout = () => {
    logout();
    nav("/login");
  };

  const toggle = () => setCollapsed((c) => !c);

  const sidebarProps = {
    servers,
    runningCount,
    activeServerID,
    isSuper,
    user,
    onToggle: toggle,
    onLogout: doLogout,
  };

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      {/* mobile top bar */}
      <div className="fixed inset-x-0 top-0 z-30 flex h-12 items-center gap-2 border-b bg-background/95 px-2 backdrop-blur md:hidden">
        <button
          type="button"
          onClick={openDrawer}
          aria-label="Open menu"
          className="flex h-9 w-9 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <Menu className="h-5 w-5" />
        </button>
        <span className="flex items-center gap-2 font-semibold">
          <span className="flex h-6 w-6 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground">
            S
          </span>
          Servy
        </span>
      </div>

      {/* desktop rail */}
      <aside
        className="relative hidden shrink-0 border-r bg-card/40 transition-[width] duration-200 ease-out md:block"
        style={{ width: collapsed ? COLLAPSED_W : EXPANDED_W }}
      >
        <Sidebar {...sidebarProps} collapsed={collapsed} />
      </aside>

      {/* mobile drawer */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <div
            className="absolute inset-0 bg-black/60 backdrop-blur-sm duration-200 animate-in fade-in-0"
            onClick={closeDrawer}
          />
          <aside
            className={cn(
              "absolute inset-y-0 left-0 w-72 border-r bg-card shadow-xl duration-200 animate-in slide-in-from-left-2 fade-in-0"
            )}
          >
            <Sidebar
              {...sidebarProps}
              collapsed={false}
              onNavigate={closeDrawer}
              headerAction={<DrawerCloseButton onClose={closeDrawer} />}
            />
          </aside>
        </div>
      )}

      {/* page */}
      <main className="flex-1 overflow-y-auto pt-12 md:pt-0">
        <Outlet />
      </main>
    </div>
  );
}
