import { useState } from "react";
import { toast } from "sonner";
import { Plus, Trash2, Webhook, Save } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { POST, PATCH, DEL, WebhookView } from "@/api/client";
import { Button, Card, CardContent, Badge, Input, Label, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, SimpleSelect } from "@/components/ui/overlays";

const EVENTS = [
  { value: "server_start", label: "Server started" },
  { value: "server_stop", label: "Server stopped" },
  { value: "server_restart", label: "Server restarted" },
  { value: "server_kill", label: "Server killed" },
  { value: "crash_detected", label: "Crash detected" },
  { value: "backup_server", label: "Backup completed" },
  { value: "server_upgrade", label: "Server updated" },
  { value: "send_command", label: "Command executed" },
];
const PROVIDERS = ["Discord", "Slack", "Mattermost", "Teams", "Generic"];

export default function Webhooks({ serverId }: { serverId: string }) {
  const { data: hooks, refetch } = useApi<WebhookView[]>(`/servers/${serverId}/webhook`);
  const [editing, setEditing] = useState<any>(null);

  const save = async () => {
    if (!editing) return;
    const payload = {
      name: editing.name, url: editing.url, webhook_type: editing.webhook_type || "Discord",
      bot_name: editing.bot_name || "", trigger: editing.trigger || [], body: editing.body || "",
      color: editing.color || "", enabled: editing.enabled !== false,
    };
    try {
      if (editing.webhook_id) await PATCH(`/webhook/${editing.webhook_id}`, payload);
      else await POST(`/servers/${serverId}/webhook`, payload);
      toast.success("Webhook saved");
      setEditing(null);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const del = async (w: WebhookView) => {
    if (!confirm(`Delete webhook "${w.name}"?`)) return;
    await DEL(`/webhook/${w.webhook_id}`);
    refetch();
  };

  const toggleEvent = (ev: string, on: boolean) => {
    const cur: string[] = editing.trigger || [];
    setEditing({ ...editing, trigger: on ? [...cur, ev] : cur.filter((t) => t !== ev) });
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between">
        <h3 className="font-semibold">Webhooks</h3>
        <Button size="sm" onClick={() => setEditing({ name: "", url: "", webhook_type: "Discord", trigger: ["server_start", "crash_detected"], enabled: true })}>
          <Plus /> New webhook
        </Button>
      </div>
      {!hooks ? <Skeleton className="h-24" /> : hooks.length === 0 ? (
        <Card><CardContent className="py-10 text-center text-sm text-muted-foreground">
          No webhooks — get notified on Discord/Slack/Teams when things happen.
        </CardContent></Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {hooks.map((w) => (
            <Card key={w.webhook_id}>
              <CardContent className="p-4">
                <div className="flex items-center gap-2">
                  <Webhook className="h-4 w-4 text-primary" />
                  <span className="font-medium">{w.name}</span>
                  <Badge variant="secondary">{w.webhook_type}</Badge>
                  {!w.enabled && <Badge variant="muted">off</Badge>}
                  <div className="ml-auto flex gap-1">
                    <Button size="icon-sm" variant="ghost" onClick={() => setEditing(w)}>✏️</Button>
                    <Button size="icon-sm" variant="ghost" onClick={() => del(w)}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
                  </div>
                </div>
                <div className="mt-1 truncate font-mono text-xs text-muted-foreground">{w.url}</div>
                <div className="mt-2 flex flex-wrap gap-1">
                  {w.trigger.filter(Boolean).map((t) => <Badge key={t} variant="muted">{t}</Badge>)}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>Webhook</DialogTitle></DialogHeader>
          {editing && (
            <div className="grid gap-3">
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label>Name</Label>
                  <Input value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
                </div>
                <div className="space-y-1.5">
                  <Label>Provider</Label>
                  <SimpleSelect value={editing.webhook_type} onValueChange={(v) => setEditing({ ...editing, webhook_type: v })}
                    options={PROVIDERS.map((p) => ({ value: p, label: p }))} />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label>URL</Label>
                <Input value={editing.url} onChange={(e) => setEditing({ ...editing, url: e.target.value })} placeholder="https://discord.com/api/webhooks/…" className="font-mono text-xs" />
              </div>
              <div className="space-y-1.5">
                <Label>Bot name</Label>
                <Input value={editing.bot_name || ""} onChange={(e) => setEditing({ ...editing, bot_name: e.target.value })} placeholder="Servy" />
              </div>
              <div className="space-y-1.5">
                <Label>Message template</Label>
                <Input value={editing.body || ""} onChange={(e) => setEditing({ ...editing, body: e.target.value })}
                  placeholder="{{server_name}} has been started" className="font-mono text-xs" />
              </div>
              <div className="space-y-1.5">
                <Label>Triggers</Label>
                <div className="grid grid-cols-2 gap-1.5">
                  {EVENTS.map((ev) => (
                    <label key={ev.value} className="flex items-center gap-2 rounded-md border p-2 text-xs cursor-pointer">
                      <input type="checkbox" checked={(editing.trigger || []).includes(ev.value)}
                        onChange={(e) => toggleEvent(ev.value, e.target.checked)} />
                      {ev.label}
                    </label>
                  ))}
                </div>
              </div>
            </div>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button onClick={save}><Save /> Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
