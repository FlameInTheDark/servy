import { useState } from "react";
import { toast } from "sonner";
import { Plus, Trash2, Play, Clock, Zap } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { POST, PATCH, DEL } from "@/api/client";
import type { TaskView } from "@/api/client";
import { Button, Card, CardContent, Badge, Input, Label, Switch, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, SimpleSelect } from "@/components/ui/overlays";

const ACTIONS = ["start", "stop", "restart", "kill", "backup", "command"];
const TRIGGER_LABEL: Record<string, string> = {
  minutes: "every N minutes", hours: "every N hours", days: "every N days",
  cron: "cron expression", reaction: "on server event",
};

export default function Schedules({ serverId }: { serverId: string }) {
  const { data: tasks, refetch } = useApi<TaskView[]>(`/servers/${serverId}/tasks`);
  const [editing, setEditing] = useState<Partial<TaskView> | null>(null);

  const save = async () => {
    if (!editing) return;
    const payload = {
      name: editing.name || "Schedule", action: editing.action || "start",
      interval: editing.interval || "hours", interval_int: Number(editing.interval_int) || 1,
      start_time: editing.start_time || "", command: editing.command || "",
      action_id: editing.action_id || "", one_time: !!editing.one_time,
      cron_string: editing.cron_string || "", delay: Number(editing.delay) || 0,
      comment: editing.comment || "", enabled: editing.enabled !== false,
    };
    try {
      if (editing.schedule_id) await PATCH(`/tasks/${editing.schedule_id}`, payload);
      else await POST(`/servers/${serverId}/tasks`, payload);
      toast.success("Schedule saved");
      setEditing(null);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const runNow = async (t: TaskView) => {
    try {
      await POST(`/tasks/${t.schedule_id}/run`);
      toast.success("Schedule triggered");
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const del = async (t: TaskView) => {
    if (!confirm(`Delete schedule "${t.name}"?`)) return;
    try {
      await DEL(`/tasks/${t.schedule_id}`);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const toggle = async (t: TaskView, enabled: boolean) => {
    try {
      await PATCH(`/tasks/${t.schedule_id}`, { enabled });
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between">
        <h3 className="font-semibold">Scheduled tasks</h3>
        <Button size="sm" onClick={() => setEditing({ action: "start", interval: "hours", interval_int: 1, enabled: true })}>
          <Plus /> New schedule
        </Button>
      </div>

      {!tasks ? (
        <Skeleton className="h-32" />
      ) : tasks.length === 0 ? (
        <Card><CardContent className="py-10 text-center text-sm text-muted-foreground">
          No schedules — automate restarts, backups and commands here.
        </CardContent></Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {tasks.map((t) => (
            <Card key={t.schedule_id}>
              <CardContent className="flex items-center gap-3 p-4">
                {t.parent ? <Zap className="h-4 w-4 text-warning" /> : <Clock className="h-4 w-4 text-primary" />}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <span className="truncate">{t.name || `#${t.schedule_id}`}</span>
                    <Badge variant="secondary">{t.action}</Badge>
                    {t.parent && <Badge variant="warning">reaction</Badge>}
                    {t.one_time && <Badge variant="muted">one-time</Badge>}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {t.interval === "cron" ? `cron: ${t.cron_string}`
                      : t.interval === "reaction" ? `on ${t.action} (delay ${t.delay}s)`
                      : t.interval === "days" || t.interval === "hours" || t.interval === "minutes" ? `every ${t.interval_int} ${t.interval}`
                      : `daily @ ${t.start_time}`}
                    {t.command && ` · "${t.command}"`}
                  </div>
                </div>
                <Switch checked={t.enabled} onCheckedChange={(v) => toggle(t, v)} />
                <Button size="icon-sm" variant="ghost" title="Run now" onClick={() => runNow(t)}><Play /></Button>
                <Button size="icon-sm" variant="ghost" title="Edit" onClick={() => setEditing(t)}>✏️</Button>
                <Button size="icon-sm" variant="ghost" onClick={() => del(t)}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>{editing?.schedule_id ? "Edit" : "New"} schedule</DialogTitle></DialogHeader>
          <div className="grid gap-3">
            <div className="space-y-1.5">
              <Label>Name</Label>
              <Input value={editing?.name || ""} onChange={(e) => setEditing(editing && { ...editing, name: e.target.value })} placeholder="Nightly restart" />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label>Action</Label>
                <SimpleSelect
                  value={editing?.action || "start"}
                  onValueChange={(v) => setEditing(editing && { ...editing, action: v })}
                  options={ACTIONS.map((a) => ({ value: a, label: a }))}
                />
              </div>
              <div className="space-y-1.5">
                <Label>Trigger</Label>
                <SimpleSelect
                  value={editing?.interval || "hours"}
                  onValueChange={(v) => setEditing(editing && { ...editing, interval: v })}
                  options={Object.entries(TRIGGER_LABEL).map(([v, l]) => ({ value: v, label: l }))}
                />
              </div>
            </div>
            {(editing?.interval === "minutes" || editing?.interval === "hours" || editing?.interval === "days") && (
              <div className="space-y-1.5">
                <Label>Every N {editing?.interval}</Label>
                <Input type="number" min={1} value={editing?.interval_int ?? 1} onChange={(e) => setEditing(editing && { ...editing, interval_int: +e.target.value })} />
              </div>
            )}
            {editing?.interval === "cron" && (
              <div className="space-y-1.5">
                <Label>Cron expression</Label>
                <Input value={editing?.cron_string || ""} onChange={(e) => setEditing(editing && { ...editing, cron_string: e.target.value })} placeholder="0 4 * * *" className="font-mono" />
              </div>
            )}
            {editing?.action === "command" && (
              <div className="space-y-1.5">
                <Label>Command to send</Label>
                <Input value={editing?.command || ""} onChange={(e) => setEditing(editing && { ...editing, command: e.target.value })} placeholder="say Restarting soon!" className="font-mono" />
              </div>
            )}
            {editing?.action === "backup" && (
              <div className="space-y-1.5">
                <Label>Backup config ID (blank = default)</Label>
                <Input value={editing?.action_id || ""} onChange={(e) => setEditing(editing && { ...editing, action_id: e.target.value })} className="font-mono" />
              </div>
            )}
            <div className="flex items-center justify-between rounded-md border p-2.5">
              <span className="text-sm">One-time (disable after run)</span>
              <Switch checked={!!editing?.one_time} onCheckedChange={(v) => setEditing(editing && { ...editing, one_time: v })} />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button onClick={save}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
