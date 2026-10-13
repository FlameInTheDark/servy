import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  Cpu, MemoryStick, HardDrive, Activity, Server, Plus, RefreshCw, Play, Square, RotateCw,
} from "lucide-react";
import { toast } from "sonner";
import { AreaChart, Area, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from "recharts";
import { useApi } from "@/hooks/useApi";
import { GET, POST, ServerDef, HostStatsSnapshot, HostHistoryPoint, realtime } from "@/api/client";
import { Card, CardContent, CardHeader, CardTitle, Button, Badge, Skeleton } from "@/components/ui/primitives";
import { formatBytes, formatUptime, timeAgo, cn } from "@/lib/utils";

function StatTile({ icon: Icon, label, value, sub, accent }: any) {
  return (
    <Card>
      <CardContent className="flex items-center gap-4 p-5">
        <div className={cn("flex h-11 w-11 items-center justify-center rounded-lg", accent || "bg-primary/15 text-primary")}>
          <Icon className="h-5 w-5" />
        </div>
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground">{label}</div>
          <div className="truncate text-xl font-semibold">{value}</div>
          {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
        </div>
      </CardContent>
    </Card>
  );
}

function ServerCard({ server }: { server: ServerDef }) {
  const [busy, setBusy] = useState(false);
  const act = async (action: string) => {
    setBusy(true);
    try {
      if (action === "start") await POST(`/servers/${server.server_id}/start`);
      if (action === "stop") await POST(`/servers/${server.server_id}/stop`);
      if (action === "restart") await POST(`/servers/${server.server_id}/restart`);
      toast.success(`${server.server_name}: ${action} sent`);
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };
  const stateBadge =
    server.running ? <Badge variant="success">Running</Badge>
    : server.crashed ? <Badge variant="destructive">Crashed</Badge>
    : server.updating ? <Badge variant="warning">Updating</Badge>
    : <Badge variant="muted">Offline</Badge>;

  return (
    <Card className="group">
      <CardContent className="p-5">
        <div className="flex items-start justify-between">
          <div className="min-w-0">
            <Link to={`/servers/${server.server_id}`} className="block truncate text-base font-semibold hover:text-primary">
              {server.server_name}
            </Link>
            <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
              <span>{server.type}</span>
              <span>·</span>
              <span>{server.online ?? 0}/{server.max ?? 0} players</span>
              {server.version && <><span>·</span><span className="truncate">{server.version}</span></>}
            </div>
          </div>
          {stateBadge}
        </div>
        <div className="mt-4 grid grid-cols-3 gap-3 text-center">
          <div className="rounded-md bg-muted/50 p-2">
            <div className="text-lg font-semibold">{(server.cpu ?? 0).toFixed(1)}%</div>
            <div className="text-[10px] uppercase text-muted-foreground">CPU</div>
          </div>
          <div className="rounded-md bg-muted/50 p-2">
            <div className="text-lg font-semibold">{formatBytes(server.mem ?? 0)}</div>
            <div className="text-[10px] uppercase text-muted-foreground">Memory</div>
          </div>
          <div className="rounded-md bg-muted/50 p-2">
            <div className="text-lg font-semibold">{formatUptime(server.started_at)}</div>
            <div className="text-[10px] uppercase text-muted-foreground">Uptime</div>
          </div>
        </div>
        <div className="mt-4 flex gap-2 opacity-0 transition-opacity group-hover:opacity-100">
          <Button size="sm" variant="secondary" className="flex-1" disabled={busy || server.running} onClick={() => act("start")}>
            <Play /> Start
          </Button>
          <Button size="sm" variant="secondary" className="flex-1" disabled={busy || !server.running} onClick={() => act("restart")}>
            <RotateCw /> Restart
          </Button>
          <Button size="sm" variant="secondary" className="flex-1" disabled={busy || !server.running} onClick={() => act("stop")}>
            <Square /> Stop
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

export default function Dashboard() {
  const { data: servers, refetch: refetchServers } = useApi<ServerDef[]>("/servers", { pollMs: 8000 });
  const { data: hostData, refetch: refetchHost } = useApi<any>("/panel/stats", { pollMs: 5000 });

  // live updates
  useEffect(() => {
    const rt = realtime();
    rt.subscribe("host");
    const off = rt.on((msg) => {
      if (msg.event === "host_stats") refetchHost();
      if (msg.event === "send_start_reload") refetchServers();
    });
    return () => {
      rt.unsubscribe("host");
      off();
    };
  }, []);

  const snap: HostStatsSnapshot | undefined = hostData?.snapshot;
  const history: HostHistoryPoint[] = hostData?.history || [];
  const running = servers?.filter((s) => s.running).length ?? 0;
  const chartData = history.map((h) => ({
    t: new Date(h.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }),
    cpu: +(h.cpu_usage ?? 0).toFixed(1),
    mem_pct: Math.round(h.mem_percent ?? 0),
  }));

  return (
    <div className="p-6 space-y-6 max-w-[1600px] mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Dashboard</h1>
          <p className="text-sm text-muted-foreground">Host telemetry & server overview</p>
        </div>
        <Button onClick={() => { refetchHost(); refetchServers(); }} variant="outline" size="sm">
          <RefreshCw /> Refresh
        </Button>
      </div>

      {/* host stat tiles */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile icon={Cpu} label="CPU Usage" value={`${(snap?.cpu_usage ?? 0).toFixed(1)}%`} sub={`${snap?.cpu_cores ?? 0} cores`} />
        <StatTile icon={MemoryStick} label="Memory" value={formatBytes(snap?.mem_usage ?? 0)} sub={`of ${formatBytes(snap?.mem_total ?? 0)} (${(snap?.mem_percent ?? 0).toFixed(1)}%)`} />
        <StatTile icon={Server} label="Servers" value={`${running} / ${servers?.length ?? 0}`} sub="running / total" accent="bg-success/15 text-success" />
        <StatTile icon={HardDrive} label="Disk (root)" value={formatBytes(snap?.disk_json?.[0]?.used ?? 0)} sub={`of ${formatBytes(snap?.disk_json?.[0]?.total ?? 0)}`} accent="bg-warning/15 text-warning" />
      </div>

      {/* host history chart */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Activity className="h-4 w-4 text-primary" /> Host — last 24h
          </CardTitle>
        </CardHeader>
        <CardContent>
          {chartData.length === 0 ? (
            <div className="flex h-48 items-center justify-center text-sm text-muted-foreground">
              Collecting telemetry… (first samples appear within a minute)
            </div>
          ) : (
            <div className="h-52">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={chartData} margin={{ top: 4, right: 8, left: -18, bottom: 0 }}>
                  <defs>
                    <linearGradient id="cpu" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="hsl(168 84% 45%)" stopOpacity={0.35} />
                      <stop offset="100%" stopColor="hsl(168 84% 45%)" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="mem" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="hsl(217 91% 60%)" stopOpacity={0.35} />
                      <stop offset="100%" stopColor="hsl(217 91% 60%)" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
                  <XAxis dataKey="t" stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} minTickGap={48} />
                  <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} unit="%" domain={[0, 100]} />
                  <Tooltip
                    contentStyle={{ background: "hsl(var(--card))", border: "1px solid hsl(var(--border))", borderRadius: 8, fontSize: 12 }}
                  />
                  <Area type="monotone" dataKey="cpu" name="CPU %" stroke="hsl(168 84% 45%)" fill="url(#cpu)" strokeWidth={2} />
                  <Area type="monotone" dataKey="mem_pct" name="Memory %" stroke="hsl(217 91% 60%)" fill="url(#mem)" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          )}
        </CardContent>
      </Card>

      {/* servers grid */}
      <div>
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Your Servers</h2>
          <Link to="/servers">
            <Button size="sm"><Plus /> New Server</Button>
          </Link>
        </div>
        {!servers ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {[1, 2, 3].map((i) => <Skeleton key={i} className="h-56" />)}
          </div>
        ) : servers.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center gap-3 py-12 text-center">
              <Server className="h-10 w-10 text-muted-foreground" />
              <div className="font-medium">No servers yet</div>
              <p className="text-sm text-muted-foreground max-w-sm">
                Create your first server — import a zip, download a jar, install any SteamCMD game, or start from scratch.
              </p>
              <Link to="/servers/new"><Button><Plus /> Create Server</Button></Link>
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {servers.map((s) => <ServerCard key={s.server_id} server={s} />)}
          </div>
        )}
      </div>
    </div>
  );
}
