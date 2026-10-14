import { Link, NavLink, useLocation, useMatch, useNavigate } from "react-router-dom";
import type { LucideIcon } from "lucide-react";
import {
  Activity, ChevronsLeft, Gamepad2, LayoutDashboard, LogOut, Network, Plus, ScrollText,
  Server, Settings, Shield, UserCircle, Users, X,
} from "lucide-react";
import type { ServerDef, UserView } from "@/api/client";
import { cn } from "@/lib/utils";
import { serverState, stateDotClass } from "@/lib/server-state";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
  Tooltip, TooltipContent, TooltipTrigger,
} from "@/components/ui/overlays";

/* ------------------------------- navigation -------------------------------- */

interface NavItemDef {
  to: string;
  label: string;
  icon: LucideIcon;
  end?: boolean;
  superuser?: boolean;
  /**
   * Replaces the default "route starts with to" test. The Servers entry uses
   * it so that exactly one row is ever highlighted: on a server page the
   * server's own row is the selection, not the section link above it.
   */
  activeWhen?: (pathname: string) => boolean;
}

const NAV_SECTIONS: { id: string; title: string; items: NavItemDef[] }[] = [
  {
    id: "overview",
    title: "Overview",
    items: [
      { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
      { to: "/servers", label: "Servers", icon: Server, activeWhen: (p) => p === "/servers" || p === "/servers/new" },
    ],
  },
  {
    id: "administration",
    title: "Administration",
    items: [
      { to: "/users", label: "Users", icon: Users, superuser: true },
      { to: "/roles", label: "Roles", icon: Shield, superuser: true },
      { to: "/nodes", label: "Cluster", icon: Network, superuser: true },
      { to: "/game-catalog", label: "Game manifests", icon: Gamepad2, superuser: true },
      { to: "/config", label: "Panel settings", icon: Settings, superuser: true },
    ],
  },
  {
    id: "system",
    title: "System",
    items: [{ to: "/logs", label: "Panel logs", icon: ScrollText }],
  },
];

/* --------------------------------- helpers --------------------------------- */

/** Hover label for the rail; expanded mode shows real labels instead. */
function RailHint({ label, side = "right", children }: { label: React.ReactNode; side?: "right" | "bottom"; children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent side={side} sideOffset={side === "bottom" ? 8 : 10}>
        {label}
      </TooltipContent>
    </Tooltip>
  );
}

function BrandMark() {
  return (
    <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary text-sm font-bold text-primary-foreground">
      S
    </span>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-2.5 pb-1.5 pt-5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/60">
      {children}
    </div>
  );
}

function Divider() {
  return <div className="mx-2 my-2 h-px bg-border" />;
}

const iconButton = cn(
  "flex items-center justify-center rounded-lg text-muted-foreground outline-none transition-colors",
  "hover:bg-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
);

/* -------------------------------- nav items -------------------------------- */

function NavItem({ item, collapsed, onNavigate }: { item: NavItemDef; collapsed: boolean; onNavigate?: () => void }) {
  const Icon = item.icon;
  // Resolved directly instead of NavLink's className-as-function: inside the
  // rail the link is a Radix tooltip trigger (a slot), and React Router's
  // function form is not resolved there — it lands in the class attribute as
  // its own source text, which silently dropped the row's flex/h-10 classes.
  const matched = !!useMatch({ path: item.to, end: !!item.end });
  const pathname = useLocation().pathname;
  const isActive = item.activeWhen ? item.activeWhen(pathname) : matched;
  const link = (
    <Link
      to={item.to}
      onClick={onNavigate}
      aria-label={collapsed ? item.label : undefined}
      aria-current={isActive ? "page" : undefined}
      className={cn(
        "flex h-10 items-center rounded-lg text-sm text-muted-foreground outline-none transition-colors",
        "hover:bg-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
        collapsed ? "mx-auto w-10 justify-center" : "w-full gap-3 px-2.5",
        isActive && "bg-accent font-medium text-foreground"
      )}
    >
      <Icon className={cn("h-[18px] w-[18px] shrink-0", isActive && "text-primary")} />
      {!collapsed && <span className="truncate">{item.label}</span>}
      {!collapsed && isActive && <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-primary" />}
    </Link>
  );
  return collapsed ? <RailHint label={item.label}>{link}</RailHint> : link;
}

/* ------------------------------ server entries ----------------------------- */

function ServerEntry({
  server, active, collapsed, onSelect,
}: {
  server: ServerDef;
  active: boolean;
  collapsed: boolean;
  onSelect: () => void;
}) {
  const state = serverState(server);
  const label = `${server.server_name} — ${state}`;
  if (collapsed) {
    const initials = server.server_name.trim().slice(0, 2).toUpperCase() || "?";
    return (
      <RailHint label={label}>
        <button
          type="button"
          onClick={onSelect}
          aria-label={label}
          className={cn(iconButton, "relative mx-auto h-10 w-10 text-xs font-semibold", active && "bg-accent text-foreground")}
        >
          {initials}
          <span className={cn("absolute right-1 top-1 h-1.5 w-1.5 rounded-full", stateDotClass(state))} />
        </button>
      </RailHint>
    );
  }
  return (
    <button
      type="button"
      onClick={onSelect}
      title={label}
      aria-current={active ? "page" : undefined}
      className={cn(
        iconButton,
        "h-9 w-full justify-start gap-2.5 px-2.5 text-sm",
        active && "bg-accent font-medium text-foreground"
      )}
    >
      <span className={cn("h-2 w-2 shrink-0 rounded-full", stateDotClass(state))} />
      <span className="truncate">{server.server_name}</span>
      {active && <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-primary" />}
    </button>
  );
}

/* ---------------------------------- footer --------------------------------- */

function AccountButton({
  user, isSuper, collapsed, onNavigate, onLogout,
}: {
  user: UserView | null;
  isSuper: boolean;
  collapsed: boolean;
  onNavigate?: () => void;
  onLogout: () => void;
}) {
  const username = user?.username || "Profile";
  const role = isSuper ? "administrator" : "user";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Account menu"
          className={cn(iconButton, collapsed ? "mx-auto h-10 w-10" : "h-11 w-full gap-2.5 px-2")}
        >
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted text-[11px] font-semibold uppercase">
            {username.slice(0, 2)}
          </span>
          {!collapsed && (
            <span className="min-w-0 flex-1 text-left leading-tight">
              <span className="block truncate text-xs font-medium text-foreground">{username}</span>
              <span className="block text-[10px]">{role}</span>
            </span>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side={collapsed ? "right" : "top"} align="start" sideOffset={8} className="w-52">
        <div className="px-2 py-1.5">
          <div className="truncate text-sm font-medium">{username}</div>
          <div className="text-xs text-muted-foreground">{role}</div>
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <NavLink to="/profile" onClick={onNavigate} className="flex w-full cursor-default items-center gap-2 [&_svg]:size-4">
            <UserCircle /> Profile
          </NavLink>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={onLogout}
          className="text-destructive focus:bg-destructive/10 focus:text-destructive"
        >
          <LogOut /> Logout
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/* --------------------------------- sidebar --------------------------------- */

export interface SidebarProps {
  collapsed: boolean;
  servers?: ServerDef[] | null;
  runningCount: number;
  activeServerID: string;
  isSuper: boolean;
  user: UserView | null;
  onToggle: () => void;
  onLogout: () => void;
  /** Called after a navigation that should close the mobile drawer. */
  onNavigate?: () => void;
  /** Replaces the header action on mobile (drawer close button). */
  headerAction?: React.ReactNode;
}

export default function Sidebar({
  collapsed, servers, runningCount, activeServerID, isSuper, user, onToggle, onLogout, onNavigate, headerAction,
}: SidebarProps) {
  const nav = useNavigate();
  const sections = NAV_SECTIONS.map((section) => ({
    ...section,
    items: section.items.filter((item) => !item.superuser || isSuper),
  })).filter((section) => section.items.length > 0);
  const serverList = servers || [];

  const openServer = (id: string) => {
    nav(`/servers/${id}`);
    onNavigate?.();
  };

  return (
    <div className="flex h-full flex-col">
      {/* brand + the sidebar's primary control, in the same slot in both states */}
      <div className={cn("flex h-14 shrink-0 items-center border-b", collapsed ? "justify-center px-2" : "gap-2.5 px-3")}>
        {collapsed ? (
          <RailHint side="bottom" label={<>Expand sidebar <Shortcut /></>}>
            <button type="button" onClick={onToggle} aria-label="Expand sidebar" className={cn(iconButton, "h-9 w-9")}>
              <BrandMark />
            </button>
          </RailHint>
        ) : (
          <>
            <NavLink
              to="/"
              onClick={onNavigate}
              aria-label="Servy"
              className="flex min-w-0 items-center gap-2.5 rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <BrandMark />
              <span className="min-w-0 leading-tight">
                <span className="block truncate text-sm font-semibold tracking-tight">Servy</span>
                <span className="block truncate text-[10px] text-muted-foreground">game server panel</span>
              </span>
            </NavLink>
            <div className="ml-auto flex items-center">{headerAction ?? <CollapseButton onToggle={onToggle} />}</div>
          </>
        )}
      </div>

      {/* navigation */}
      <nav className={cn("flex-1 overflow-y-auto overflow-x-hidden py-2", collapsed ? "px-2" : "px-2.5")}>
        {sections.map((section, index) => (
          <div key={section.id}>
            {index > 0 && collapsed && <Divider />}
            {!collapsed && <SectionLabel>{section.title}</SectionLabel>}
            <div className="space-y-0.5">
              {section.items.map((item) => (
                <NavItem key={item.to} item={item} collapsed={collapsed} onNavigate={onNavigate} />
              ))}
            </div>
          </div>
        ))}

        {/* server quick list */}
        {serverList.length > 0 && (
          <div>
            {collapsed ? <Divider /> : (
              <div className="flex items-center justify-between px-2.5 pb-1.5 pt-5">
                <span className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/60">
                  <Activity className="h-3 w-3" /> Servers
                </span>
                <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-muted-foreground">
                  {runningCount}/{serverList.length}
                </span>
              </div>
            )}
            <div className={cn("space-y-0.5 overflow-y-auto", collapsed ? "max-h-72" : "max-h-64 pr-0.5")}>
              {serverList.map((server) => (
                <ServerEntry
                  key={server.server_id}
                  server={server}
                  collapsed={collapsed}
                  active={server.server_id === activeServerID}
                  onSelect={() => openServer(server.server_id)}
                />
              ))}
            </div>
            {collapsed ? (
              <RailHint label="New server">
                <NavLink
                  to="/servers/new"
                  onClick={onNavigate}
                  aria-label="New server"
                  className={cn(iconButton, "mx-auto mt-1 h-10 w-10 border border-dashed")}
                >
                  <Plus className="h-4 w-4" />
                </NavLink>
              </RailHint>
            ) : (
              <NavLink
                to="/servers/new"
                onClick={onNavigate}
                className={cn(iconButton, "mt-1 h-9 w-full justify-start gap-2.5 border border-dashed px-2.5 text-xs")}
              >
                <Plus className="h-3.5 w-3.5" /> New server
              </NavLink>
            )}
          </div>
        )}
      </nav>

      {/* account */}
      <div className={cn("shrink-0 border-t py-2", collapsed ? "px-2" : "px-2.5")}>
        <AccountButton
          user={user}
          isSuper={isSuper}
          collapsed={collapsed}
          onNavigate={onNavigate}
          onLogout={onLogout}
        />
      </div>
    </div>
  );
}

/* --------------------------------- controls -------------------------------- */

function Shortcut() {
  return <kbd className="ml-1.5 rounded bg-muted px-1 font-sans text-[10px] text-muted-foreground">Ctrl B</kbd>;
}

export function CollapseButton({ onToggle }: { onToggle: () => void }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" onClick={onToggle} aria-label="Collapse sidebar" className={cn(iconButton, "h-8 w-8")}>
          <ChevronsLeft className="h-4 w-4" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="bottom" sideOffset={8}>
        Collapse sidebar <Shortcut />
      </TooltipContent>
    </Tooltip>
  );
}

export function DrawerCloseButton({ onClose }: { onClose: () => void }) {
  return (
    <button type="button" onClick={onClose} aria-label="Close menu" className={cn(iconButton, "h-8 w-8")}>
      <X className="h-4 w-4" />
    </button>
  );
}
