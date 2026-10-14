import { useState } from "react";
import { toast } from "sonner";
import { HardDriveDownload, Play, RotateCcw, Plus, Trash2, Save, Cloud, Server, FolderOpen, X, CircleHelp } from "lucide-react";
import { Link } from "react-router-dom";
import { useApi } from "@/hooks/useApi";
import { GET, POST, PATCH, DEL, BackupConfig, BackupArchive, BackupStorage, ServerBackupFolders } from "@/api/client";
import { Button, Card, CardContent, Input, Label, Switch, Badge, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";
import { SimpleSelect, Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/overlays";
import { formatBytes, timeAgo } from "@/lib/utils";

const KIND_LABEL: Record<string, string> = {
  s3: "S3", ftp: "FTP", webdav: "WebDAV", sftp: "SFTP", local: "Folder",
};

function HelpHint({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" aria-label="More information" className="text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none">
          <CircleHelp className="h-3.5 w-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[280px] leading-relaxed">{text}</TooltipContent>
    </Tooltip>
  );
}

/** One pickable folder: a chip that toggles in and out of the backup scope. */
function DirChip({ dir, selected, onClick }: { dir: string; selected: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`rounded-md border px-2 py-0.5 font-mono text-[11px] transition-colors ${
        selected
          ? "border-primary/60 bg-primary/10 text-primary"
          : "text-muted-foreground hover:border-foreground/40 hover:text-foreground"
      }`}
    >
      {dir}
    </button>
  );
}

/** Where an archive lives: on the host, in the destination, or both. */
function SourceBadge({ source }: { source: string }) {
  if (source === "both") {
    return <Badge variant="secondary"><Cloud className="mr-1 h-3 w-3" />host + remote</Badge>;
  }
  if (source === "remote") {
    return <Badge variant="secondary"><Cloud className="mr-1 h-3 w-3" />remote only</Badge>;
  }
  return <Badge variant="muted"><Server className="mr-1 h-3 w-3" />host only</Badge>;
}

export default function BackupsTab({ serverId }: { serverId: string }) {
  const { data: configs, refetch } = useApi<BackupConfig[]>(`/servers/${serverId}/backups`);
  const { data: storages } = useApi<BackupStorage[]>("/panel/backup-storages");
  const { data: folders } = useApi<ServerBackupFolders>(`/servers/${serverId}/backup-folders`);
  const [editing, setEditing] = useState<Partial<BackupConfig> | null>(null);
  const [archivesFor, setArchivesFor] = useState<BackupConfig | null>(null);
  const [archives, setArchives] = useState<BackupArchive[]>([]);
  const [busy, setBusy] = useState("");
  const [newDir, setNewDir] = useState("");

  const storageOf = (id?: string) => storages?.find((s) => s.storage_id === id);

  const selectedDirs = editing?.included_dirs || [];
  const manifestDirs = folders?.manifest_dirs || [];
  // folders the game manifest covers are listed under their own heading
  const serverDirs = (folders?.dirs || []).filter((d) => !manifestDirs.includes(d));

  // a selection that matches the manifest byte for byte came from the game
  const fromManifest = (dirs?: string[]) =>
    !!dirs?.length && manifestDirs.length === dirs.length && dirs.every((d) => manifestDirs.includes(d));

  const toggleDir = (dir: string) =>
    setEditing((e) => {
      if (!e) return e;
      const current = e.included_dirs || [];
      return { ...e, included_dirs: current.includes(dir) ? current.filter((d) => d !== dir) : [...current, dir] };
    });

  const addDir = () => {
    let value = newDir.trim().split("\\").join("/");
    if (value.startsWith("./")) value = value.slice(2);
    while (value.startsWith("/")) value = value.slice(1);
    while (value.endsWith("/")) value = value.slice(0, -1);
    if (!value) return;
    if (!selectedDirs.includes(value)) toggleDir(value);
    setNewDir("");
  };

  const openArchives = async (cfg: BackupConfig) => {
    setArchivesFor(cfg);
    setArchives([]);
    try {
      const list = await GET<BackupArchive[]>(`/backups/backup/${cfg.backup_id}/archives`);
      setArchives(list || []);
    } catch (e: any) {
      toast.error(e.message);
    }
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
        included_dirs: editing.included_dirs || [], excluded_dirs: editing.excluded_dirs || [],
        max_backups: Number(editing.max_backups) || 0,
        compress: !!editing.compress, shutdown: !!editing.shutdown,
        before: editing.before || "", after: editing.after || "",
        default: !!editing.default, enabled: !!editing.enabled, backup_type: editing.backup_type,
        storage_id: editing.storage_id || "",
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
    const target = storageOf(cfg.storage_id);
    const extra = target ? `\nUploaded archives in "${target.name}" are removed too.` : "";
    if (!confirm(`Delete backup config "${cfg.backup_name}"?${extra}`)) return;
    try {
      await DEL(`/backups/backup/${cfg.backup_id}?files=true`);
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
        <Button size="sm" onClick={() => setEditing({ backup_name: "", backup_type: "zip_vault", compress: true, enabled: true, included_dirs: [], excluded_dirs: [], max_backups: 0, shutdown: false, default: !configs?.length })}>
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
                    <div className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
                      {storageOf(c.storage_id) ? (
                        <>
                          <Cloud className="h-3.5 w-3.5 text-primary" />
                          <span>{storageOf(c.storage_id)!.name}</span>
                          <Badge variant="muted">{KIND_LABEL[storageOf(c.storage_id)!.kind] || storageOf(c.storage_id)!.kind}</Badge>
                        </>
                      ) : (
                        <>
                          <Server className="h-3.5 w-3.5" />
                          <span>this host only</span>
                        </>
                      )}
                    </div>
                    <div className="mt-1 font-mono text-[11px] text-muted-foreground">{c.backup_location}</div>
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
                  <span>
                    retention: {storageOf(c.storage_id)
                      ? "panel policy (host + remote)"
                      : c.max_backups > 0 ? `${c.max_backups} max` : "unlimited"}
                  </span>
                  <span className="flex items-center gap-1">
                    <FolderOpen className="h-3.5 w-3.5" />
                    {c.included_dirs?.length > 0 ? c.included_dirs.join(", ") : "whole server folder"}
                    {fromManifest(c.included_dirs) && (
                      <span className="text-muted-foreground/70">(game manifest)</span>
                    )}
                  </span>
                  {c.excluded_dirs?.length > 0 && <span>excl: {c.excluded_dirs.join(", ")}</span>}
                  {c.status?.last_run && <span>last run {timeAgo(c.status.last_run as string)}</span>}
                  {c.status?.last_upload_error ? (
                    <span className="text-destructive">upload failed: {String(c.status.last_upload_error)}</span>
                  ) : c.status?.last_upload ? (
                    <span className="text-primary">uploaded {timeAgo(c.status.last_upload as string)}</span>
                  ) : null}
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
              <Label className="flex items-center gap-1.5">
                <Cloud className="h-3.5 w-3.5" /> Storage destination
              </Label>
              <SimpleSelect
                value={editing?.storage_id || ""}
                onValueChange={(v) => setEditing(editing && { ...editing, storage_id: v })}
                placeholder="This host only"
                options={(storages || []).filter((s) => s.enabled).map((s) => ({
                  value: s.storage_id,
                  label: `${s.name} (${KIND_LABEL[s.kind] || s.kind})`,
                }))}
              />
              <p className="text-[11px] text-muted-foreground">
                {editing?.storage_id
                  ? "The host running this server uploads each archive. Local copies follow the panel's retention policy."
                  : <>Archives stay on the host that runs this server. Add destinations in{" "}
                      <Link to="/config" className="text-primary underline-offset-2 hover:underline">panel settings</Link>.</>}
              </p>
            </div>
            <div className="space-y-1.5">
              <Label>Location (blank = default)</Label>
              <Input value={editing?.backup_location || ""} onChange={(e) => setEditing(editing && { ...editing, backup_location: e.target.value })} placeholder="/path/to/backups" />
            </div>
            <div className="space-y-2.5 rounded-md border p-2.5">
              <div className="flex items-center justify-between gap-2">
                <Label className="flex items-center gap-1.5">
                  <FolderOpen className="h-3.5 w-3.5" /> Backup folders
                  <HelpHint text="Pick what gets archived. Every selected folder is stored with everything inside it. Leave the list empty to back up the whole server folder." />
                </Label>
                <div className="flex items-center gap-1">
                  {manifestDirs.length > 0 && (
                    <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]" onClick={() => setEditing(editing && { ...editing, included_dirs: [...manifestDirs] })}>
                      Use manifest folders
                    </Button>
                  )}
                  {selectedDirs.length > 0 ? (
                    <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]" onClick={() => setEditing(editing && { ...editing, included_dirs: [] })}>
                      Back up everything
                    </Button>
                  ) : (
                    <Badge variant="muted">whole server folder</Badge>
                  )}
                </div>
              </div>

              {selectedDirs.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {selectedDirs.map((d) => (
                    <span key={d} className="flex items-center gap-1 rounded-md border border-primary/60 bg-primary/10 px-2 py-0.5 font-mono text-[11px] text-primary">
                      {d}
                      <button type="button" aria-label={`Remove ${d}`} onClick={() => toggleDir(d)} className="opacity-70 hover:opacity-100">
                        <X className="h-3 w-3" />
                      </button>
                    </span>
                  ))}
                </div>
              )}

              {manifestDirs.length > 0 && (
                <div className="space-y-1">
                  <div className="text-[11px] text-muted-foreground">
                    From the game manifest{folders?.manifest_source ? ` (${folders.manifest_source})` : ""} — click to add
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {manifestDirs.map((d) => (
                      <DirChip key={d} dir={d} selected={selectedDirs.includes(d)} onClick={() => toggleDir(d)} />
                    ))}
                  </div>
                </div>
              )}

              {serverDirs.length > 0 && (
                <div className="space-y-1">
                  <div className="text-[11px] text-muted-foreground">Folders found in the server folder</div>
                  <div className="flex flex-wrap gap-1.5">
                    {serverDirs.map((d) => (
                      <DirChip key={d} dir={d} selected={selectedDirs.includes(d)} onClick={() => toggleDir(d)} />
                    ))}
                  </div>
                </div>
              )}

              {folders?.folders_error && (
                <p className="text-[11px] text-destructive">Could not list the server folder: {folders.folders_error}</p>
              )}

              <div className="flex gap-2">
                <Input
                  value={newDir}
                  onChange={(e) => setNewDir(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && addDir()}
                  placeholder="unlisted folder, e.g. mods/extra or server.cfg"
                  className="h-8 font-mono text-xs"
                />
                <Button size="sm" variant="outline" onClick={addDir} disabled={!newDir.trim()}>
                  <Plus /> Add
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label className="flex items-center gap-1.5">
                Excluded dirs (comma separated)
                <HelpHint text="Subtracted from the folders above, e.g. cache or .servy." />
              </Label>
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
                  <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
                    <span>{formatBytes(a.size)} · {timeAgo(a.modified)}</span>
                    <SourceBadge source={a.source} />
                  </div>
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
