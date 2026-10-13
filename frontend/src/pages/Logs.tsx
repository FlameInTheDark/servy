import { useState } from "react";
import { ScrollText, RefreshCw } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { Card, CardContent, Skeleton, Button } from "@/components/ui/primitives";
import { Tabs, TabsList, TabsTrigger, TabsContent, SimpleSelect } from "@/components/ui/overlays";
import { cn } from "@/lib/utils";

const LOG_TYPES = ["audit", "session", "schedule", "cmd_queue", "commander", "auth", "server"];

export default function LogsPage() {
  const [type, setType] = useState("audit");
  const { data, refetch, loading } = useApi<any>(`/panel/logs/${type}`, { pollMs: 8000 });
  const [filter, setFilter] = useState("");

  const lines: string[] = (data?.log || [])
    .map((l: any) => `${l.time} [${l.level}] ${l.message}`);

  const filtered = filter
    ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
    : lines;

  return (
    <div className="p-6 max-w-6xl mx-auto space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold flex items-center gap-2"><ScrollText className="h-6 w-6" /> Panel Logs</h1>
          <p className="text-sm text-muted-foreground">Panel activity & diagnostics</p>
        </div>
        <Button variant="outline" size="sm" onClick={refetch}><RefreshCw /> Refresh</Button>
      </div>

      <Tabs value={type} onValueChange={setType}>
        <TabsList className="flex-wrap h-auto">
          {LOG_TYPES.map((t) => <TabsTrigger key={t} value={t}>{t}</TabsTrigger>)}
        </TabsList>
        <TabsContent value={type} className="mt-4">
          <Card>
            <CardContent className="p-0">
              <div className="border-b p-2">
                <input
                  placeholder="Filter…"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  className="w-full rounded-md border border-input bg-background/60 px-3 py-1.5 text-xs focus-visible:outline-none"
                />
              </div>
              {loading && !data ? <Skeleton className="h-96" /> : (
                <div className="max-h-[65vh] overflow-y-auto p-3 font-mono text-[11px] leading-relaxed">
                  {filtered.length === 0 ? (
                    <div className="p-8 text-center text-muted-foreground">No log entries</div>
                  ) : filtered.map((l, i) => (
                    <div key={i} className={cn(
                      "whitespace-pre-wrap py-0.5",
                      l.includes("[error]") && "text-destructive",
                      l.includes("[warning]") && "text-warning",
                      l.includes("[critical]") && "text-destructive font-bold"
                    )}>{l}</div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
