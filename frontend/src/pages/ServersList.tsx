import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import {
  Server, Plus, Search, Play, Square, RotateCw, Users, Cpu, MemoryStick,
  Network, Boxes, Clock, Loader2, WifiOff,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { api, ServerDef, realtime } from "@/api/client";
import { Button, Badge, Input, Skeleton } from "@/components/ui/primitives";
import { cn } from "@/lib/utils";
import { isTransitional, serverState, stateVariant } from "@/lib/server-state";

/* ------------------------------- helpers ---------------------------------- */

const TYPE_LABELS: Record<string, string> = {
  "minecraft-java": "Minecraft",
  "minecraft-bedrock": "Bedrock",
  steam_cmd: "Steam",
  hytale: "Hytale",
  custom: "Custom",
};

function typeLabel(t: string) {
  return TYPE_LABELS[t] || t;
}

function uptimeOf(s: ServerDef) {
  if (!s.running || !s.started_at) return null;
  const ms = Date.now() - new Date(s.started_at).getTime();
  if (Number.isNaN(ms) || ms < 0) return null;
  const mins = Math.floor(ms / 60000);
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  if (h >= 24) return `${Math.floor(h / 24)}d ${h % 24}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function fmtMem(bytes: number) {
  if (!bytes) return "—";
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${Math.round(bytes / (1024 * 1024))} MB`;
}

/* ------------------------------ server card ------------------------------- */

function ServerCard({ s, node, onAction }: {
  s: ServerDef;
  node?: { name: string; online: boolean };
  onAction: (a: "start" | "stop" | "restart") => void;
}) {
  const state = serverState(s);
  const busy = isTransitional(state);
  const uptime = uptimeOf(s);
  const remote = !!s.node_id && s.node_id !== "local";
  const nodeOffline = remote && node?.online === false;

  return (
    <div className="group rounded-xl border bg-card p-4 transition-all hover:border-border hover:shadow-md">
      <Link
        to={`/servers/${s.server_id}`}
        aria-label={`Manage ${s.server_name}`}
        className="block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <Server className="h-4 w-4 text-muted-foreground" />
          </div>
          <div className="min-w-0">
            <div className="truncate font-medium leading-tight">{s.server_name}</div>
            <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
              <span>{typeLabel(s.type)}</span>
              <span className="text-muted-foreground/40">·</span>
              <span className="font-mono">:{s.server_port}</span>
            </div>
          </div>
        </div>
        <Badge variant={stateVariant(state)} className="shrink-0 capitalize">
          {state === "running" && <span className="mr-1 h-1.5 w-1.5 rounded-full bg-success animate-pulse-dot" />}
          {state}
        </Badge>
      </div>

      <div className="mt-3 grid grid-cols-4 gap-2 border-t pt-3 text-xs">
        <div>
          <div className="flex items-center gap-1 text-muted-foreground"><Users className="h-3 w-3" /> players</div>
          <div className="mt-0.5 font-medium tabular-nums">{s.running ? `${s.online ?? 0}/${s.max ?? 0}` : "—"}</div>
        </div>
        <div>
          <div className="flex items-center gap-1 text-muted-foreground"><Cpu className="h-3 w-3" /> cpu</div>
          <div className="mt-0.5 font-medium tabular-nums">{s.running ? `${(s.cpu ?? 0).toFixed(1)}%` : "—"}</div>
        </div>
        <div>
          <div className="flex items-center gap-1 text-muted-foreground"><MemoryStick className="h-3 w-3" /> mem</div>
          <div className="mt-0.5 font-medium tabular-nums">{s.running ? fmtMem(s.mem ?? 0) : "—"}</div>
        </div>
        <div>
          <div className="flex items-center gap-1 text-muted-foreground"><Clock className="h-3 w-3" /> up</div>
          <div className="mt-0.5 font-medium tabular-nums">{uptime ?? "—"}</div>
        </div>
      </div>
      </Link>

      <div className="mt-3 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          {remote && (
            nodeOffline ? (
              <span className="inline-flex items-center gap-1 rounded bg-destructive/10 px-1.5 py-0.5 text-destructive">
                <WifiOff className="h-3 w-3" /> {node?.name || s.node_id} offline
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5">
                <Network className="h-3 w-3" /> {node?.name || s.node_id}
              </span>
            )
          )}
          {s.version && <span className="truncate">{s.version}</span>}
        </div>
        <div className="flex gap-1">
          {busy ? (
            <Button variant="ghost" size="sm" disabled>
              <Loader2 className="animate-spin" /> {state}
            </Button>
          ) : s.running ? (
            <>
              <Button variant="ghost" size="sm" className="h-8 w-8 p-0" title="Restart" onClick={() => onAction("restart")}>
                <RotateCw className="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="sm" className="h-8 gap-1.5 text-destructive hover:text-destructive" onClick={() => onAction("stop")}>
                <Square className="h-3.5 w-3.5" /> Stop
              </Button>
            </>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              className="h-8 gap-1.5 text-success hover:text-success"
              disabled={nodeOffline}
              title={nodeOffline ? "The node hosting this server is offline" : undefined}
              onClick={() => onAction("start")}
            >
              <Play className="h-3.5 w-3.5" /> Start
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

/* --------------------------------- page ----------------------------------- */

export default function ServersListPage() {
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState("all");
  const [nodeFilter, setNodeFilter] = useState(params.get("node") || "all");
  const { data: servers, loading, refetch } = useApi<ServerDef[]>("/servers", { pollMs: 8000 });
  const { data: cluster } = useApi<{ nodes: any[] }>("/panel/nodes", { pollMs: 20000 });

  const nodeMap = useMemo(() => {
    const map: Record<string, { name: string; online: boolean }> = {};
    for (const n of cluster?.nodes || []) map[n.node_id] = { name: n.name, online: !!n.online };
    return map;
  }, [cluster]);

  useEffect(() => {
    setNodeFilter(params.get("node") || "all");
  }, [params]);

  const pickNode = (id: string) => {
    setNodeFilter(id);
    const next = new URLSearchParams(params);
    if (id === "all") next.delete("node");
    else next.set("node", id);
    setParams(next, { replace: true });
  };

  // live refresh on lifecycle events
  useEffect(() => {
    const rt = realtime();
    const off = rt.on((msg) => {
      if (msg.event === "send_start_reload") refetch();
    });
    return () => off();
  }, [refetch]);

  const act = async (id: string, action: "start" | "stop" | "restart") => {
    try {
      await api("POST", `/servers/${id}/${action}`, {});
      toast.success(`${action} sent`);
      setTimeout(refetch, 800);
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const filtered = (servers || []).filter((s) => {
    if (q && !s.server_name.toLowerCase().includes(q.toLowerCase())) return false;
    if (nodeFilter === "local" && s.node_id && s.node_id !== "local") return false;
    if (nodeFilter !== "all" && nodeFilter !== "local" && s.node_id !== nodeFilter) return false;
    if (filter === "running") return s.running;
    if (filter === "stopped") return !s.running && !s.importing && !s.updating;
    if (filter === "issues") return s.crashed;
    return true;
  });

  const counts = {
    all: servers?.length ?? 0,
    running: servers?.filter((s) => s.running).length ?? 0,
    stopped: servers?.filter((s) => !s.running && !s.importing && !s.updating).length ?? 0,
    issues: servers?.filter((s) => s.crashed).length ?? 0,
  };

  return (
    <div className="p-6">
      <div className="mx-auto max-w-6xl">
        {/* header */}
        <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">Servers</h1>
            <p className="text-sm text-muted-foreground">
              {servers?.length ?? 0} hosted game {servers?.length === 1 ? "server" : "servers"} · {counts.running} running
            </p>
          </div>
          <Button onClick={() => nav("/servers/new")}>
            <Plus /> New server
          </Button>
        </div>

        {/* toolbar */}
        {loading && !servers ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {[...Array(6)].map((_, i) => <Skeleton key={i} className="h-44" />)}
          </div>
        ) : !servers || servers.length === 0 ? (
          /* empty state */
          <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-24 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-muted">
              <Boxes className="h-7 w-7 text-muted-foreground" />
            </div>
            <h2 className="mt-4 text-lg font-semibold">No servers yet</h2>
            <p className="mt-1 max-w-sm text-sm text-muted-foreground">
              Import a zip archive, download a Minecraft jar, install any Steam game, or point at an existing directory.
            </p>
            <Button className="mt-5" onClick={() => nav("/servers/new")}>
              <Plus /> Create your first server
            </Button>
          </div>
        ) : (
          <>
            <div className="mb-4 flex flex-wrap items-center gap-2">
              <div className="relative flex-1 sm:max-w-xs">
                <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search servers…" className="pl-8" />
              </div>
              <div className="flex gap-1 rounded-lg border p-0.5">
                {([
                  ["all", `All (${counts.all})`],
                  ["running", `Running (${counts.running})`],
                  ["stopped", `Stopped (${counts.stopped})`],
                  ["issues", `Crashed (${counts.issues})`],
                ] as const).map(([id, label]) => (
                  <button
                    key={id}
                    onClick={() => setFilter(id)}
                    className={cn(
                      "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
                      filter === id ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
              {(cluster?.nodes?.length ?? 0) > 1 && (
                <div className="flex gap-1 rounded-lg border p-0.5">
                  {[
                    { id: "all", label: "All nodes" },
                    { id: "local", label: "This panel" },
                    ...(cluster?.nodes || []).filter((n: any) => !n.local).map((n: any) => ({ id: n.node_id, label: n.name })),
                  ].map((n) => (
                    <button
                      key={n.id}
                      onClick={() => pickNode(n.id)}
                      className={cn(
                        "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
                        nodeFilter === n.id ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      {n.label}
                    </button>
                  ))}
                </div>
              )}
            </div>

            {filtered.length === 0 ? (
              <div className="rounded-xl border border-dashed py-16 text-center text-sm text-muted-foreground">
                No servers match “{q || filter}”.
              </div>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {filtered.map((s) => (
                  <ServerCard
                    key={s.server_id}
                    s={s}
                    node={s.node_id ? nodeMap[s.node_id] : undefined}
                    onAction={(a) => act(s.server_id, a)}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
