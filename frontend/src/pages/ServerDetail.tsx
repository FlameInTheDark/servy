import { useEffect, useMemo, useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  Terminal, FolderOpen, DatabaseBackup, CalendarClock, FileCog, BarChart3, Webhook as WebhookIcon, Settings2, Trash2,
  RefreshCw, Play, Square, RotateCw, Loader2, WifiOff, Copy, Network, Users, Cpu, MemoryStick, Clock, HardDrive,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { DEL, POST, ServerDef, realtime } from "@/api/client";
import { Button, Badge, Skeleton } from "@/components/ui/primitives";
import {
  Tabs, TabsList, TabsTrigger, TabsContent, Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter,
  Tooltip, TooltipTrigger, TooltipContent,
} from "@/components/ui/overlays";
import { formatUptime, cn } from "@/lib/utils";
import Console from "@/pages/server/Console";
import Files from "@/pages/server/Files";
import BackupsTab from "@/pages/server/BackupsTab";
import Schedules from "@/pages/server/Schedules";
import ConfigEditor from "@/pages/server/ConfigEditor";
import Stats from "@/pages/server/Stats";
import Webhooks from "@/pages/server/Webhooks";
import Settings from "@/pages/server/Settings";

interface ConfigFileInfo {
  file: string;
  mode: string;
  format: string;
  label?: string;
  description?: string;
  editor?: "lines";
}

function fmtMem(bytes: number) {
  if (!bytes) return "—";
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${Math.round(bytes / (1024 * 1024))} MB`;
}

function Metric({ icon, label, value, title }: { icon: React.ReactNode; label: string; value: React.ReactNode; title?: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border bg-card/60 px-3 py-1.5" title={title}>
      <span className="text-muted-foreground">{icon}</span>
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-sm font-medium tabular-nums">{value}</span>
    </div>
  );
}

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const { data: server, refetch } = useApi<ServerDef>(`/servers/${id}`, { pollMs: 6000 });
  const { data: configFiles, refetch: refetchConfigs } = useApi<ConfigFileInfo[]>(server ? `/servers/${id}/config` : null);
  const { data: cluster } = useApi<{ nodes: any[] }>("/panel/nodes", { pollMs: 30000 });
  const [activeConfig, setActiveConfig] = useState<string | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [busy, setBusy] = useState("");
  const [copied, setCopied] = useState(false);

  // live refresh
  useEffect(() => {
    const rt = realtime();
    rt.subscribe(`server.${id}`);
    const off = rt.on((msg) => {
      if (msg.event === "send_start_reload") refetch();
    });
    return () => off();
  }, [id, refetch]);

  const nodeInfo = useMemo(() => {
    const map: Record<string, { name: string; address: string; online: boolean }> = {};
    for (const n of cluster?.nodes || []) map[n.node_id] = { name: n.name, address: n.address || "", online: !!n.online };
    return map;
  }, [cluster]);

  if (!server) {
    return (
      <div className="p-6 max-w-[1600px] mx-auto space-y-4">
        <Skeleton className="h-10 w-72" />
        <Skeleton className="h-96" />
      </div>
    );
  }

  const del = async (files: boolean) => {
    try {
      await DEL(`/servers/${id}?files=${files}`);
      toast.success("Server deleted");
      nav("/servers");
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const act = async (action: "start" | "stop" | "restart") => {
    setBusy(action);
    try {
      await POST(`/servers/${id}/${action}`, {});
      toast.success(`${action} sent`);
      setTimeout(refetch, 800);
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy("");
    }
  };

  const node = server.node_id && server.node_id !== "local" ? server.node_id : "";
  const nodeState = node ? nodeInfo[node] : undefined;
  const nodeOffline = !!node && nodeState?.online === false;
  const transitional = server.importing || server.updating || server.state === "starting" || server.state === "stopping";

  const stateBadge = server.importing ? <Badge variant="warning">importing</Badge>
    : server.updating ? <Badge variant="warning">updating</Badge>
    : server.state === "starting" ? <Badge variant="warning">starting</Badge>
    : server.state === "stopping" ? <Badge variant="warning">stopping</Badge>
    : server.running ? <Badge variant="success"><span className="mr-1 h-1.5 w-1.5 rounded-full bg-success-foreground animate-pulse-dot" />Running · {formatUptime(server.started_at)}</Badge>
    : server.crashed ? <Badge variant="destructive">Crashed</Badge>
    : <Badge variant="muted">Offline</Badge>;

  // connect hint: the node the server runs on, or the host serving this panel
  const connectHost = nodeState?.address ? nodeState.address.split(":")[0] : window.location.hostname;
  const connectAddress = `${connectHost}:${server.server_port}`;

  const copyAddress = async () => {
    try {
      await navigator.clipboard.writeText(connectAddress);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Could not copy to the clipboard");
    }
  };

  const configTabs = configFiles || [];

  return (
    <div className="flex h-full flex-col">
      {/* header */}
      <div className="border-b bg-card/40 px-6 py-4">
        <div className="mx-auto max-w-[1600px] space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="truncate text-xl font-bold">{server.server_name}</h1>
                {stateBadge}
                <Badge variant="outline">{server.type}</Badge>
                {node && (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span>
                        <Badge variant={nodeOffline ? "destructive" : "secondary"} className="gap-1">
                          {nodeOffline ? <WifiOff className="h-3 w-3" /> : <Network className="h-3 w-3" />}
                          {nodeState?.name || node}
                        </Badge>
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      {nodeOffline
                        ? "The machine running this server is offline — start/stop and file actions are unavailable."
                        : `Runs on the cluster node ${nodeState?.name || node}${nodeState?.address ? ` (${nodeState.address})` : ""}.`}
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              {transitional ? (
                <Button variant="outline" size="sm" disabled>
                  <Loader2 className="animate-spin" /> {server.importing ? "importing" : server.updating ? "updating" : server.state}
                </Button>
              ) : server.running ? (
                <>
                  <Button variant="outline" size="sm" disabled={!!busy || nodeOffline} onClick={() => act("restart")}>
                    {busy === "restart" ? <Loader2 className="animate-spin" /> : <RotateCw />} Restart
                  </Button>
                  <Button variant="secondary" size="sm" disabled={!!busy || nodeOffline} onClick={() => act("stop")}>
                    {busy === "stop" ? <Loader2 className="animate-spin" /> : <Square />} Stop
                  </Button>
                </>
              ) : (
                <Button variant="success" size="sm" disabled={!!busy || nodeOffline} onClick={() => act("start")}
                  title={nodeOffline ? "The node hosting this server is offline" : undefined}>
                  {busy === "start" ? <Loader2 className="animate-spin" /> : <Play />} Start
                </Button>
              )}
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon-sm" aria-label="Refresh" onClick={() => refetch()}>
                    <RefreshCw />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Refresh status</TooltipContent>
              </Tooltip>
              <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
                <Trash2 /> Delete
              </Button>
            </div>
          </div>

          {/* live metrics */}
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Metric icon={<Users className="h-3.5 w-3.5" />} label="players" value={`${server.online ?? 0}/${server.max ?? 0}`} />
            <Metric icon={<Cpu className="h-3.5 w-3.5" />} label="cpu" value={`${(server.cpu ?? 0).toFixed(1)}%`} />
            <Metric icon={<MemoryStick className="h-3.5 w-3.5" />} label="mem" value={fmtMem(server.mem ?? 0)} />
            <Metric icon={<Clock className="h-3.5 w-3.5" />} label="uptime" value={server.running ? formatUptime(server.started_at) : "—"} />
            {server.version && <Metric icon={<HardDrive className="h-3.5 w-3.5" />} label="version" value={server.version} />}
            <button
              type="button"
              onClick={copyAddress}
              title="Copy the address players connect to"
              className="flex items-center gap-2 rounded-lg border bg-card/60 px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:border-primary/60 hover:text-foreground"
            >
              {copied ? <RefreshCw className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              <span className="font-mono">{connectAddress}</span>
            </button>
          </div>

          {nodeOffline && (
            <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs">
              <WifiOff className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
              <div>
                <b>{nodeState?.name || node}</b> is offline, so this server cannot be started, stopped or managed right now.
                Status and configuration come from the panel copy; live data resumes when the machine reconnects.
              </div>
            </div>
          )}
        </div>
      </div>

      {/* tabs */}
      <div className="flex-1 overflow-y-auto px-6 py-5">
        <div className="mx-auto max-w-[1600px]">
          <Tabs defaultValue="console">
            <TabsList className="flex-wrap h-auto">
              <TabsTrigger value="console"><Terminal className="h-3.5 w-3.5" /> Console</TabsTrigger>
              <TabsTrigger value="files"><FolderOpen className="h-3.5 w-3.5" /> Files</TabsTrigger>
              <TabsTrigger value="config"><FileCog className="h-3.5 w-3.5" /> Config</TabsTrigger>
              <TabsTrigger value="backups"><DatabaseBackup className="h-3.5 w-3.5" /> Backups</TabsTrigger>
              <TabsTrigger value="schedules"><CalendarClock className="h-3.5 w-3.5" /> Schedules</TabsTrigger>
              <TabsTrigger value="stats"><BarChart3 className="h-3.5 w-3.5" /> Stats</TabsTrigger>
              <TabsTrigger value="webhooks"><WebhookIcon className="h-3.5 w-3.5" /> Webhooks</TabsTrigger>
              <TabsTrigger value="settings"><Settings2 className="h-3.5 w-3.5" /> Settings</TabsTrigger>
            </TabsList>

            <TabsContent value="console" className="mt-4">
              <Console key={server.server_id + server.state} server={server} />
            </TabsContent>

            <TabsContent value="files" className="mt-4">
              <Files serverId={server.server_id} />
            </TabsContent>

            <TabsContent value="config" className="mt-4">
              {activeConfig ? (
                <div>
                  <Button variant="ghost" size="sm" className="mb-3" onClick={() => setActiveConfig(null)}>
                    ← all config files
                  </Button>
                  <ConfigEditor
                    serverId={server.server_id}
                    file={activeConfig}
                    configInfo={configTabs.find((entry) => entry.file === activeConfig)}
                  />
                </div>
              ) : (
                <>
                  <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                    <p className="text-sm text-muted-foreground">
                      Showing supported files that exist in this server folder. Start the game once to generate manifest-declared files, then refresh.
                    </p>
                    <Button variant="outline" size="sm" onClick={() => refetchConfigs()}>
                      <RefreshCw className="h-3.5 w-3.5" /> Refresh files
                    </Button>
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    {configTabs.length === 0 ? (
                      <div className="col-span-full py-10 text-center text-sm text-muted-foreground">
                        No editable config files detected yet. You can still browse the full server directory in Files.
                      </div>
                    ) : configTabs.map((f) => (
                      <button key={f.file}
                        onClick={() => setActiveConfig(f.file)}
                        className="flex items-start gap-3 rounded-xl border bg-card p-4 text-left transition-all hover:border-primary/60 hover:shadow">
                        <FileCog className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium">{f.label || f.file}</div>
                          {f.label && <div className="truncate font-mono text-xs text-muted-foreground">{f.file}</div>}
                          <div className="mt-1 text-xs text-muted-foreground">
                            {f.editor === "lines" ? "Line list editor" : `${f.format} · ${f.mode}`}
                          </div>
                          {f.description && <div className="mt-1 text-xs text-muted-foreground/80">{f.description}</div>}
                        </div>
                      </button>
                    ))}
                  </div>
                </>
              )}
            </TabsContent>

            <TabsContent value="backups" className="mt-4">
              <BackupsTab serverId={server.server_id} />
            </TabsContent>

            <TabsContent value="schedules" className="mt-4">
              <Schedules serverId={server.server_id} />
            </TabsContent>

            <TabsContent value="stats" className="mt-4">
              <Stats serverId={server.server_id} />
            </TabsContent>

            <TabsContent value="webhooks" className="mt-4">
              <Webhooks serverId={server.server_id} />
            </TabsContent>

            <TabsContent value="settings" className="mt-4">
              <Settings server={server} onUpdate={refetch} />
            </TabsContent>
          </Tabs>
        </div>
      </div>

      {/* delete dialog */}
      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete “{server.server_name}”?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            This removes the server from the panel. You can also delete its files from disk
            (<span className="font-mono text-xs">{server.path}</span>).
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteOpen(false)}>Cancel</Button>
            <Button variant="secondary" onClick={() => del(false)}>Remove from panel</Button>
            <Button variant="destructive" onClick={() => del(true)}>Delete + files</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
