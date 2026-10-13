import { useEffect } from "react";
import { AreaChart, Area, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from "recharts";
import { Users, Gauge, Database, Wifi } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { HistoryPoint, realtime } from "@/api/client";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/primitives";
import { SimpleSelect } from "@/components/ui/overlays";
import { formatBytes } from "@/lib/utils";

const tooltipStyle = { background: "hsl(var(--card))", border: "1px solid hsl(var(--border))", borderRadius: 8, fontSize: 12 };

export default function Stats({ serverId }: { serverId: string }) {
  const [hours, setHours] = [ (window as any).__statHours || 24, (v: any) => { (window as any).__statHours = v; } ];
  const { data: history, refetch } = useApi<HistoryPoint[]>(`/servers/${serverId}/history?hours=${hours}`, { pollMs: 30000 });

  useEffect(() => {
    const rt = realtime();
    rt.subscribe(`server.${serverId}`);
    const off = rt.on((m) => { if (m.event === "server_stats") refetch(); });
    return () => { rt.unsubscribe(`server.${serverId}`); off(); };
  }, []);

  const data = (history || []).map((h) => ({
    t: new Date(h.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }),
    cpu: +(h.cpu || 0).toFixed(1),
    mem: Math.round((h.mem || 0) / 1048576),
    online: h.online,
    ping: Math.round((h.int_ping_results || 0) * 10) / 10,
    world: Math.round((h.world_size || 0) / 1048576),
  }));

  const last = data[data.length - 1];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-semibold">Statistics</h3>
        <SimpleSelect
          value={String(hours)}
          onValueChange={(v) => setHours(+v)}
          options={[
            { value: "1", label: "Last hour" }, { value: "6", label: "Last 6 hours" },
            { value: "24", label: "Last 24 hours" }, { value: "168", label: "Last 7 days" },
          ]}
          className="w-40"
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {[
          { icon: Gauge, label: "CPU", value: `${last?.cpu ?? 0}%` },
          { icon: Database, label: "Memory", value: formatBytes((last?.mem || 0) * 1048576) },
          { icon: Users, label: "Players", value: `${last?.online ?? 0}` },
          { icon: Wifi, label: "Ping", value: `${last?.ping ?? 0} ms` },
        ].map((s) => (
          <Card key={s.label}>
            <CardContent className="flex items-center gap-3 p-4">
              <s.icon className="h-5 w-5 text-primary" />
              <div>
                <div className="text-xs text-muted-foreground">{s.label}</div>
                <div className="text-lg font-semibold">{s.value}</div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader><CardTitle className="text-sm">CPU % / Memory (MB)</CardTitle></CardHeader>
        <CardContent>
          <div className="h-56">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={data} margin={{ top: 4, right: 8, left: -12, bottom: 0 }}>
                <defs>
                  <linearGradient id="scpu" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(168 84% 45%)" stopOpacity={0.35} />
                    <stop offset="100%" stopColor="hsl(168 84% 45%)" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="smem" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(217 91% 60%)" stopOpacity={0.3} />
                    <stop offset="100%" stopColor="hsl(217 91% 60%)" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
                <XAxis dataKey="t" stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} minTickGap={48} />
                <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} />
                <Tooltip contentStyle={tooltipStyle} />
                <Area type="monotone" dataKey="cpu" name="CPU %" stroke="hsl(168 84% 45%)" fill="url(#scpu)" strokeWidth={2} />
                <Area type="monotone" dataKey="mem" name="Mem (MB)" stroke="hsl(217 91% 60%)" fill="url(#smem)" strokeWidth={2} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-sm">Players online</CardTitle></CardHeader>
        <CardContent>
          <div className="h-40">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={data} margin={{ top: 4, right: 8, left: -22, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
                <XAxis dataKey="t" stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} minTickGap={48} />
                <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} allowDecimals={false} />
                <Tooltip contentStyle={tooltipStyle} />
                <Area type="stepAfter" dataKey="online" name="Players" stroke="hsl(142 71% 45%)" fill="hsl(142 71% 45%)" fillOpacity={0.15} strokeWidth={2} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
