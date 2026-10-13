import { useState } from "react";
import { toast } from "sonner";
import { HardDriveDownload, Play, RotateCcw, Plus, Trash2, Save } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { GET, POST, PATCH, DEL, BackupConfig } from "@/api/client";
import { Button, Card, CardContent, Input, Label, Switch, Badge, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";
import { SimpleSelect } from "@/components/ui/overlays";
import { formatBytes, timeAgo } from "@/lib/utils";

export default function BackupsTab({ serverId }: { serverId: string }) {
  const { data: configs, refetch } = useApi<BackupConfig[]>(`/servers/${serverId}/backups`);
  const [editing, setEditing] = useState<Partial<BackupConfig> | null>(null);
  const [archivesFor, setArchivesFor] = useState<BackupConfig | null>(null);
  const [archives, setArchives] = useState<any[]>([]);
  const [busy, setBusy] = useState("");

  const openArchives = async (cfg: BackupConfig) => {
    setArchivesFor(cfg);
    const list = await GET<any[]>(`/backups/backup/${cfg.backup_id}/archives`);
    setArchives(list || []);
  };

  const runBackup = async (cfg: BackupConfig) => {
    setBusy(cfg.backup_id);
    try {
      await POST(`/backups/backup/${cfg.backup_id}/run`);
      toast.success("Backup completed");
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy("");
    }
  };

  const save = async () => {
    if (!editing) return;
    try {
      const payload = {
        name: editing.backup_name, location: editing.backup_location,
        excluded_dirs: editing.excluded_dirs, max_backups: Number(editing.max_backups) || 0,
        compress: !!editing.compress, shutdown: !!editing.shutdown,
        before: editing.before || "", after: editing.after || "",
        default: !!editing.default, enabled: !!editing.enabled, backup_type: editing.backup_type,
      };
      if (editing.backup_id) await PATCH(`/backups/backup/${editing.backup_id}`, payload);
      else await POST(`/servers/${serverId}/backups`, payload);
      toast.success("Saved backup config");
      setEditing(null);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const del = async (cfg: BackupConfig) => {
    if (!confirm(`Delete backup config "${cfg.backup_name}"?`)) return;
    try {
      await DEL(`/backups/backup/${cfg.backup_id}`);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const restore = async (cfg: BackupConfig, name: string) => {
    if (!confirm(`Restore ${name}? The server will stop.`)) return;
    try {
      await POST(`/backups/backup/${cfg.backup_id}/restore/${encodeURIComponent(name)}`);
      toast.success("Restore complete");
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between">
        <h3 className="font-semibold">Backup configurations</h3>
        <Button size="sm" onClick={() => setEditing({ backup_name: "", backup_type: "zip_vault", compress: true, enabled: true, excluded_dirs: [], max_backups: 0, shutdown: false, default: !configs?.length })}>
          <Plus /> New backup config
        </Button>
      </div>

      {!configs ? (
        <Skeleton className="h-40" />
      ) : configs.length === 0 ? (
        <Card><CardContent className="py-10 text-center text-sm text-muted-foreground">
          No backup configs — create one to protect this server.
        </CardContent></Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {configs.map((c) => (
            <Card key={c.backup_id}>
              <CardContent className="p-5">
                <div className="flex items-start justify-between">
                  <div>
                    <div className="flex items-center gap-2 font-medium">
                      {c.backup_name}
                      {c.default && <Badge>default</Badge>}
                      <Badge variant="secondary">{c.backup_type}</Badge>
                      {!c.enabled && <Badge variant="muted">disabled</Badge>}
                    </div>
                    <div className="mt-1 font-mono text-xs text-muted-foreground">{c.backup_location}</div>
                  </div>
                  <div className="flex gap-1">
                    <Button size="icon-sm" variant="ghost" title="Run now" onClick={() => runBackup(c)}>
                      <Play className="h-3.5 w-3.5" />
                    </Button>
                    <Button size="icon-sm" variant="ghost" title="Archives" onClick={() => openArchives(c)}>
                      <HardDriveDownload className="h-3.5 w-3.5" />
                    </Button>
                    <Button size="icon-sm" variant="ghost" title="Edit" onClick={() => setEditing(c)}>
                      <Save className="h-3.5 w-3.5" />
                    </Button>
                    <Button size="icon-sm" variant="ghost" onClick={() => del(c)}>
                      <Trash2 className="h-3.5 w-3.5 text-destructive" />
                    </Button>
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                  <span>{c.compress ? "compressed" : "uncompressed"}</span>
                  <span>{c.shutdown ? "stops server first" : "hot backup"}</span>
                  <span>retention: {c.max_backups > 0 ? `${c.max_backups} max` : "unlimited"}</span>
                  {c.excluded_dirs?.length > 0 && <span>excl: {c.excluded_dirs.join(", ")}</span>}
                  {c.status?.last_run && <span>last run {timeAgo(c.status.last_run as string)}</span>}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {/* editor */}
      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing?.backup_id ? "Edit" : "New"} backup config</DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="space-y-1.5">
              <Label>Name</Label>
              <Input value={editing?.backup_name || ""} onChange={(e) => setEditing(editing && { ...editing, backup_name: e.target.value })} />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label>Engine</Label>
                <SimpleSelect
                  value={editing?.backup_type || "zip_vault"}
                  onValueChange={(v) => setEditing(editing && { ...editing, backup_type: v })}
                  options={[
                    { value: "zip_vault", label: "Zip Vault (portable)" },
                    { value: "snapshot", label: "Snapshot (deduplicated)" },
                  ]}
                />
              </div>
              <div className="space-y-1.5">
                <Label>Max backups (0 = all)</Label>
                <Input type="number" value={editing?.max_backups ?? 0} onChange={(e) => setEditing(editing && { ...editing, max_backups: +e.target.value })} />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>Location (blank = default)</Label>
              <Input value={editing?.backup_location || ""} onChange={(e) => setEditing(editing && { ...editing, backup_location: e.target.value })} placeholder="/path/to/backups" />
            </div>
            <div className="space-y-1.5">
              <Label>Excluded dirs (comma separated)</Label>
              <Input value={(editing?.excluded_dirs || []).join(",")} onChange={(e) => setEditing(editing && { ...editing, excluded_dirs: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })} placeholder=".servy, cache" />
            </div>
            <div className="flex items-center justify-between rounded-md border p-2.5">
              <span className="text-sm">Stop server before backup</span>
              <Switch checked={!!editing?.shutdown} onCheckedChange={(v) => setEditing(editing && { ...editing, shutdown: v })} />
            </div>
            <div className="flex items-center justify-between rounded-md border p-2.5">
              <span className="text-sm">Compress archive</span>
              <Switch checked={!!editing?.compress} onCheckedChange={(v) => setEditing(editing && { ...editing, compress: v })} />
            </div>
            <div className="flex items-center justify-between rounded-md border p-2.5">
              <span className="text-sm">Enabled</span>
              <Switch checked={editing?.enabled !== false} onCheckedChange={(v) => setEditing(editing && { ...editing, enabled: v })} />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button onClick={save}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* archives browser */}
      <Dialog open={!!archivesFor} onOpenChange={(o) => !o && setArchivesFor(null)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>Archives — {archivesFor?.backup_name}</DialogTitle>
          </DialogHeader>
          <div className="max-h-[50vh] space-y-1.5 overflow-y-auto">
            {archives.length === 0 ? (
              <div className="py-6 text-center text-sm text-muted-foreground">No archives yet — run a backup first.</div>
            ) : archives.map((a) => (
              <div key={a.name} className="flex items-center gap-3 rounded-md border p-2.5">
                <HardDriveDownload className="h-4 w-4 shrink-0 text-primary" />
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono text-xs">{a.name}</div>
                  <div className="text-[10px] text-muted-foreground">{formatBytes(a.size)} · {timeAgo(a.modified)}</div>
                </div>
                <a href={`/api/v1/backups/backup/${archivesFor?.backup_id}/download/${a.name}?token=${localStorage.getItem("servy_token")}`}>
                  <Button size="sm" variant="outline">Download</Button>
                </a>
                <Button size="sm" variant="secondary" onClick={() => archivesFor && restore(archivesFor, a.name)}>
                  <RotateCcw /> Restore
                </Button>
              </div>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
