import { useEffect, useState, useRef } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import {
  LayoutDashboard, Server, Users, Shield, Settings, ScrollText, Network,
  LogOut, ChevronsLeft, ChevronsRight, UserCircle, Plus, Menu, X, Activity, ChevronDown, Gamepad2,
} from "lucide-react";
import { useAuth } from "@/hooks/AuthContext";
import { useApi } from "@/hooks/useApi";
import { GET, ServerDef, realtime } from "@/api/client";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/primitives";

/* --------------------------------- pieces --------------------------------- */

function SidebarLink({ to, icon: Icon, label, collapsed, end }: any) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          "group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
          isActive && "bg-accent text-foreground font-medium"
        )
      }
      title={collapsed ? label : undefined}
    >
      {({ isActive }: { isActive: boolean }) => (
        <>
          <span
            className={cn(
              "absolute left-0 top-1/2 h-4 w-0.5 -translate-y-1/2 rounded-full bg-foreground transition-opacity",
              isActive ? "opacity-100" : "opacity-0"
            )}
          />
          <Icon className="h-4 w-4 shrink-0" />
          {!collapsed && <span className="truncate">{label}</span>}
        </>
      )}
    </NavLink>
  );
}

function NavSection({ title, children, collapsed }: any) {
  if (collapsed) return <div className="space-y-1">{children}</div>;
  return (
    <div className="space-y-1">
      <div className="px-3 pb-1 pt-4 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70">
        {title}
      </div>
      {children}
    </div>
  );
}

function ServerStatusDot({ state }: { state: string }) {
  const color =
    state === "running" ? "bg-success" : state === "crashed" ? "bg-destructive" : state === "updating" ? "bg-warning" : "bg-muted-foreground/40";
  return <span className={cn("h-2 w-2 shrink-0 rounded-full", color, state === "running" && "animate-pulse-dot")} />;
}

function stateBadge(s: ServerDef) {
  if (s.importing) return "importing";
  if (s.updating) return "updating";
  return s.state;
}

/* --------------------------------- layout --------------------------------- */

export default function PanelLayout() {
  const { user, logout } = useAuth();
  const nav = useNavigate();
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem("servy_sidebar") === "1");
  const [mobileOpen, setMobileOpen] = useState(false);
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const userMenuRef = useRef<HTMLDivElement>(null);
  const { data: servers, setData: setServers } = useApi<ServerDef[]>("/servers", { pollMs: 10000 });

  useEffect(() => {
    localStorage.setItem("servy_sidebar", collapsed ? "1" : "0");
  }, [collapsed]);

  // live status updates over WS
  useEffect(() => {
    const rt = realtime();
    const off = rt.on((msg) => {
      if (msg.event === "send_start_reload" && msg.data?.server_id && servers) {
        GET<ServerDef[]>("/servers").then(setServers).catch(() => {});
      }
    });
    return () => {
      off();
    };
  }, [servers]);

  const runningCount = servers?.filter((s) => s.running).length ?? 0;
  const isSuper = !!user?.superuser;

  const sidebar = (
    <>
      {/* brand */}
      <div className="flex h-14 shrink-0 items-center gap-2.5 border-b px-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground font-bold">
          S
        </div>
        {!collapsed && (
          <div className="leading-tight">
            <div className="text-sm font-semibold tracking-tight">Servy</div>
            <div className="text-[10px] text-muted-foreground">game server panel</div>
          </div>
        )}
      </div>

      {/* nav */}
      <nav className="flex-1 space-y-1 overflow-y-auto p-2">
        <NavSection title="Overview" collapsed={collapsed}>
          <SidebarLink to="/" icon={LayoutDashboard} label="Dashboard" collapsed={collapsed} end />
          <SidebarLink to="/servers" icon={Server} label="Servers" collapsed={collapsed} />
        </NavSection>

        {isSuper && (
          <NavSection title="Administration" collapsed={collapsed}>
            <SidebarLink to="/users" icon={Users} label="Users" collapsed={collapsed} />
            <SidebarLink to="/roles" icon={Shield} label="Roles" collapsed={collapsed} />
            <SidebarLink to="/nodes" icon={Network} label="Cluster" collapsed={collapsed} />
            <SidebarLink to="/game-catalog" icon={Gamepad2} label="Game manifests" collapsed={collapsed} />
            <SidebarLink to="/config" icon={Settings} label="Panel Settings" collapsed={collapsed} />
          </NavSection>
        )}

        <NavSection title="System" collapsed={collapsed}>
          <SidebarLink to="/logs" icon={ScrollText} label="Panel Logs" collapsed={collapsed} />
        </NavSection>

        {/* servers quick list */}
        {!collapsed && servers && servers.length > 0 && (
          <div className="border-t pt-2">
            <div className="flex items-center justify-between px-3 pb-1 pt-2">
              <span className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70">
                <Activity className="h-3 w-3" />
                Servers
              </span>
              <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-muted-foreground">
                {runningCount}/{servers.length}
              </span>
            </div>
            <div className="max-h-56 space-y-0.5 overflow-y-auto px-1">
              {servers.map((s) => (
                <button
                  key={s.server_id}
                  onClick={() => {
                    setMobileOpen(false);
                    nav(`/servers/${s.server_id}`);
                  }}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                >
                  <ServerStatusDot state={stateBadge(s)} />
                  <span className="truncate">{s.server_name}</span>
                </button>
              ))}
            </div>
            <button
              onClick={() => {
                setMobileOpen(false);
                nav("/servers/new");
              }}
              className="mt-1 flex w-full items-center gap-2 rounded-md border border-dashed px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:border-border hover:bg-accent hover:text-foreground"
            >
              <Plus className="h-3.5 w-3.5" /> New server
            </button>
          </div>
        )}
      </nav>

      {/* footer */}
      <div className="shrink-0 border-t p-2">
        <div className="relative">
          <button
            onClick={() => setUserMenuOpen(!userMenuOpen)}
            className={cn("w-full flex items-center gap-2 rounded-md px-2 py-1.5", collapsed && "justify-center")}
          >
            <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold uppercase">
              {(user?.username || "?").slice(0, 2)}
            </div>
            {!collapsed && (
              <div className="min-w-0 leading-tight flex-1 text-left">
                <div className="truncate text-xs font-medium">{user?.username || "Profile"}</div>
                <div className="text-[10px] text-muted-foreground">{isSuper ? "administrator" : "user"}</div>
              </div>
            )}
            {!collapsed && <ChevronDown className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", userMenuOpen && "rotate-180")} />}
          </button>
          
          {/* User menu dropdown */}
          {!collapsed && userMenuOpen && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setUserMenuOpen(false)} />
              <div className="absolute z-50 bottom-full left-0 right-0 mb-2 animate-in slide-in-from-bottom-2 fade-in-0 duration-100">
                <div className="rounded-md border bg-popover p-1 shadow-lg min-w-[180px]">
                  <SidebarLink to="/profile" icon={UserCircle} label="Profile" collapsed={false} end />
                  <button
                    onClick={() => {
                      logout();
                      nav("/login");
                    }}
                    className={cn(
                      "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                    )}
                  >
                    <LogOut className="h-4 w-4 shrink-0" />
                    <span>Logout</span>
                  </button>
                </div>
              </div>
            </>
          )}
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          className="mt-1 w-full"
          onClick={() => setCollapsed(!collapsed)}
        >
          {collapsed ? <ChevronsRight className="h-4 w-4" /> : <ChevronsLeft className="h-4 w-4" />}
        </Button>
      </div>
    </>
  );

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      {/* mobile top bar */}
      <div className="fixed inset-x-0 top-0 z-30 flex h-12 items-center justify-between border-b bg-background/95 px-3 backdrop-blur md:hidden">
        <button onClick={() => setMobileOpen(true)} className="rounded-md p-2 text-muted-foreground hover:bg-accent hover:text-foreground">
          <Menu className="h-5 w-5" />
        </button>
        <div className="flex items-center gap-2 font-semibold">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-primary text-xs font-bold text-primary-foreground">S</div>
          Servy
        </div>
        <div className="w-9" />
      </div>

      {/* desktop sidebar */}
      <aside
        className={cn(
          "hidden flex-col border-r bg-card/40 transition-all duration-200 md:flex",
          collapsed ? "w-16" : "w-60"
        )}
      >
        {sidebar}
      </aside>

      {/* mobile drawer */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <div className="absolute inset-0 bg-black/60" onClick={() => setMobileOpen(false)} />
          <aside className="absolute inset-y-0 left-0 flex w-64 flex-col border-r bg-card shadow-xl">
            <button
              onClick={() => setMobileOpen(false)}
              className="absolute right-2 top-3 rounded-md p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <X className="h-4 w-4" />
            </button>
            {sidebar}
          </aside>
        </div>
      )}

      {/* Main */}
      <main className="flex-1 overflow-y-auto pt-12 md:pt-0">
        <Outlet />
      </main>
    </div>
  );
}
