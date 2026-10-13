import { useState } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import {
  Terminal, FolderOpen, DatabaseBackup, CalendarClock, FileCog, BarChart3, Webhook as WebhookIcon, Settings2, Trash2, FileText, RefreshCw,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { GET, DEL, POST, ServerDef, realtime } from "@/api/client";
import { Button, Badge, Skeleton } from "@/components/ui/primitives";
import { Tabs, TabsList, TabsTrigger, TabsContent, Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";
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

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const { data: server, refetch } = useApi<ServerDef>(`/servers/${id}`, { pollMs: 6000 });
  const { data: configFiles, refetch: refetchConfigs } = useApi<ConfigFileInfo[]>(server ? `/servers/${id}/config` : null);
  const [activeConfig, setActiveConfig] = useState<string | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);

  // live refresh
  useState(() => {
    const rt = realtime();
    rt.subscribe(`server.${id}`);
  });

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

  const stateBadge =
    server.running ? <Badge variant="success">Running · {formatUptime(server.started_at)}</Badge>
    : server.crashed ? <Badge variant="destructive">Crashed</Badge>
    : <Badge variant="muted">Offline</Badge>;

  const configTabs = configFiles || [];

  return (
    <div className="flex h-full flex-col">
      {/* header */}
      <div className="border-b bg-card/40 px-6 py-4">
        <div className="mx-auto flex max-w-[1600px] items-center gap-4">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-3">
              <h1 className="truncate text-xl font-bold">{server.server_name}</h1>
              {stateBadge}
              <Badge variant="outline">{server.type}</Badge>
              {server.node_id && server.node_id !== "" && server.node_id !== "local" && (
                <Badge variant="warning">node: {server.node_id}</Badge>
              )}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {server.online ?? 0}/{server.max ?? 0} players · {(server.cpu ?? 0).toFixed(1)}% CPU · uptime {formatUptime(server.started_at)}
              {server.version ? ` · ${server.version}` : ""}
            </div>
          </div>
          <Button variant="outline" size="sm" onClick={() => refetch()}>⟳</Button>
          <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
            <Trash2 /> Delete
          </Button>
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
