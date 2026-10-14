/* Backup storage destinations: where the panel uploads server archives.
 *
 * The form is driven by the panel: each kind reports its own fields, so a new
 * backend only needs to exist on the server side. Secrets are masked in every
 * response — sending the mask back keeps what is stored, so editing a
 * destination never forces the admin to retype a key. */

import { useState } from "react";
import { toast } from "sonner";
import {
  Plus, Trash2, Pencil, PlugZap, HardDrive, Cloud, Star, HelpCircle,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { POST, PATCH, DEL, BackupStorage, BackupStorageField } from "@/api/client";
import {
  Button, Card, CardContent, Input, Label, Switch, Badge, Skeleton,
} from "@/components/ui/primitives";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
  SimpleSelect, Tooltip, TooltipContent, TooltipTrigger,
} from "@/components/ui/overlays";

interface StorageKind {
  kind: string;
  label: string;
  fields: BackupStorageField[];
}

const KIND_SHORT: Record<string, string> = {
  s3: "S3",
  ftp: "FTP",
  webdav: "WebDAV",
  sftp: "SFTP",
  local: "Local folder",
};

function kindLabel(kind: string) {
  return KIND_SHORT[kind] || kind;
}

function Hint({ text }: { text?: string }) {
  if (!text) return null;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" className="text-muted-foreground transition-colors hover:text-foreground" aria-label="What is this?">
          <HelpCircle className="h-3 w-3" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{text}</TooltipContent>
    </Tooltip>
  );
}

export function BackupStoragesManager() {
  const { data, refetch } = useApi<BackupStorage[]>("/panel/backup-storages");
  // Backends and their form fields come from the panel: adding one on the
  // server side is enough for it to show up here.
  const { data: kinds } = useApi<StorageKind[]>("/panel/backup-storages/kinds");
  const [editing, setEditing] = useState<Partial<BackupStorage> | null>(null);
  const [busy, setBusy] = useState("");
  const [testing, setTesting] = useState("");

  const test = async (s: BackupStorage) => {
    setTesting(s.storage_id);
    try {
      await POST(`/panel/backup-storages/${s.storage_id}/test`);
      toast.success(`${s.name} is reachable`);
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setTesting("");
    }
  };

  const del = async (s: BackupStorage) => {
    const used = s.used_by?.length ?? 0;
    const msg = used
      ? `"${s.name}" is used by ${used} backup config${used > 1 ? "s" : ""}. Delete it anyway?`
      : `Delete the "${s.name}" destination? Backup archives already uploaded stay where they are.`;
    if (!confirm(msg)) return;
    setBusy(s.storage_id);
    try {
      await DEL(`/panel/backup-storages/${s.storage_id}${used ? "?force=true" : ""}`);
      toast.success("Destination deleted");
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy("");
    }
  };

  return (
    <Card>
      <CardContent className="space-y-4 p-5">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="flex items-center gap-2 text-sm font-semibold">
              <Cloud className="h-4 w-4 text-muted-foreground" /> Storage destinations
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Backups are uploaded by the host that runs the server. Every destination keeps the
              archives of this panel under its own folder, so two panels can share one bucket or account.
            </p>
          </div>
          <Button size="sm" onClick={() => setEditing({ kind: kinds?.[0]?.kind || "s3", enabled: true, settings: {}, prefix: "", fields: kinds?.[0]?.fields })}>
            <Plus /> Add destination
          </Button>
        </div>

        {!data ? (
          <Skeleton className="h-24" />
        ) : data.length === 0 ? (
          <div className="rounded-md border border-dashed py-8 text-center text-sm text-muted-foreground">
            No destinations yet — backups are stored on the host only.
          </div>
        ) : (
          <div className="space-y-2">
            {data.map((s) => (
              <div key={s.storage_id} className="flex flex-wrap items-center gap-3 rounded-md border p-3">
                <HardDrive className="h-4 w-4 shrink-0 text-primary" />
                <div className="min-w-[180px] flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{s.name}</span>
                    <Badge variant="secondary">{kindLabel(s.kind)}</Badge>
                    {s.default && <Badge><Star className="mr-1 h-3 w-3" />default</Badge>}
                    {!s.enabled && <Badge variant="muted">disabled</Badge>}
                    {(s.used_by?.length ?? 0) > 0 && (
                      <Badge variant="muted">{s.used_by!.length} backup config{s.used_by!.length > 1 ? "s" : ""}</Badge>
                    )}
                  </div>
                  <div className="mt-1 font-mono text-[11px] text-muted-foreground">
                    {s.location || s.kind}{s.namespace ? ` / ${s.namespace}` : ""}
                  </div>
                </div>
                <div className="flex gap-1">
                  <Button size="sm" variant="outline" disabled={testing === s.storage_id} onClick={() => test(s)}>
                    <PlugZap /> {testing === s.storage_id ? "Testing…" : "Test"}
                  </Button>
                  <Button size="icon-sm" variant="ghost" title="Edit" onClick={() => setEditing(s)}>
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>
                  <Button size="icon-sm" variant="ghost" disabled={busy === s.storage_id} onClick={() => del(s)}>
                    <Trash2 className="h-3.5 w-3.5 text-destructive" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      {editing && (
        <StorageDialog
          key={editing.storage_id || "new"}
          storage={editing}
          kinds={kinds || []}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); refetch(); }}
        />
      )}
    </Card>
  );
}

function StorageDialog({
  storage, kinds, onClose, onSaved,
}: {
  storage: Partial<BackupStorage>;
  kinds: StorageKind[];
  onClose: () => void;
  onSaved: () => void;
}) {
  // The dialog is remounted per destination (keyed by id in the parent), so a
  // plain initial state is enough and no effect has to sync it back.
  const isNew = !storage.storage_id;
  const [form, setForm] = useState<Partial<BackupStorage>>(() => ({
    ...storage,
    settings: { ...(storage.settings || {}) },
  }));
  const [saving, setSaving] = useState(false);
  const [reachable, setReachable] = useState(false);

  const fields = form.fields?.length ? form.fields : kinds.find((k) => k.kind === form.kind)?.fields || [];
  const setField = (key: string, value: string) =>
    setForm((f) => ({ ...f, settings: { ...(f.settings || {}), [key]: value } }));

  const payload = () => ({
    name: form.name || "",
    kind: form.kind || "s3",
    prefix: form.prefix || "",
    settings: form.settings || {},
    enabled: form.enabled !== false,
    default: !!form.default,
  });

  const runTest = async () => {
    setReachable(false);
    try {
      if (isNew) await POST("/panel/backup-storages/test", payload());
      else await POST(`/panel/backup-storages/${storage.storage_id}/test`, payload());
      setReachable(true);
      toast.success("Destination is reachable");
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      if (isNew) await POST("/panel/backup-storages", payload());
      else await PATCH(`/panel/backup-storages/${storage.storage_id}`, payload());
      toast.success(isNew ? "Destination added" : "Destination updated");
      onSaved();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{isNew ? "New" : "Edit"} storage destination</DialogTitle>
          <DialogDescription>
            The host that runs the server uploads the archives, so it needs to reach this location.
          </DialogDescription>
        </DialogHeader>

        <div className="grid max-h-[60vh] gap-3 overflow-y-auto pr-1">
          <div className="space-y-1.5">
            <Label>Name</Label>
            <Input
              value={form.name || ""}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder="Off-site S3"
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>Kind</Label>
              <SimpleSelect
                value={form.kind || kinds[0]?.kind || "s3"}
                onValueChange={(v) =>
                  setForm((f) => ({
                    ...f,
                    kind: v,
                    settings: {},
                    fields: kinds.find((k) => k.kind === v)?.fields,
                  }))
                }
                options={kinds.map((k) => ({ value: k.kind, label: k.label }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label className="flex items-center gap-1.5">
                Folder prefix
                <Hint text="Optional. Archives are stored below this prefix and then below the panel's own folder." />
              </Label>
              <Input
                value={form.prefix || ""}
                onChange={(e) => setForm({ ...form, prefix: e.target.value })}
                placeholder="servy-backups"
              />
            </div>
          </div>

          {fields.map((f) => {
            const value = form.settings?.[f.key] ?? f.default ?? "";
            if (f.type === "boolean") {
              return (
                <div key={f.key} className="flex items-center justify-between rounded-md border p-2.5">
                  <span className="flex items-center gap-1.5 text-sm">
                    {f.label} <Hint text={f.hint} />
                  </span>
                  <Switch checked={value === "true"} onCheckedChange={(v) => setField(f.key, v ? "true" : "false")} />
                </div>
              );
            }
            return (
              <div key={f.key} className="space-y-1.5">
                <Label className="flex items-center gap-1.5">
                  {f.label}
                  {f.required && <span className="text-destructive">*</span>}
                  <Hint text={f.hint} />
                </Label>
                <Input
                  type={f.type === "password" ? "password" : "text"}
                  value={value}
                  onChange={(e) => setField(f.key, e.target.value)}
                  placeholder={f.default || ""}
                />
              </div>
            );
          })}

          <div className="flex items-center justify-between rounded-md border p-2.5">
            <span className="text-sm">Enabled</span>
            <Switch checked={form.enabled !== false} onCheckedChange={(v) => setForm({ ...form, enabled: v })} />
          </div>
          <div className="flex items-center justify-between rounded-md border p-2.5">
            <span className="text-sm">Use for new backup configs</span>
            <Switch checked={!!form.default} onCheckedChange={(v) => setForm({ ...form, default: v })} />
          </div>

          {!!form.namespace && (
            <p className="text-xs text-muted-foreground">
              Archives of this panel land under the <span className="font-mono">{form.namespace}</span> folder.
            </p>
          )}
        </div>

        <DialogFooter className="justify-between sm:justify-between">
          <Button variant="outline" onClick={runTest} disabled={saving}>
            <PlugZap /> {reachable ? "Reachable" : "Test connection"}
          </Button>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button onClick={save} disabled={saving}>{saving ? "Saving…" : "Save"}</Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
