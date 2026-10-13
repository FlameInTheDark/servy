import { useState } from "react";
import { toast } from "sonner";
import { Plus, Trash2, Save } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { GET, POST, PATCH, DEL, RoleView, ServerDef } from "@/api/client";
import { Button, Card, CardContent, Badge, Input, Label, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";

const PERMS = [
  ["COMMANDS", 0], ["TERMINAL", 1], ["LOGS", 2], ["SCHEDULE", 3],
  ["BACKUP", 4], ["FILES", 5], ["CONFIG", 6], ["PLAYERS", 7],
] as const;

export default function RolesPage() {
  const { data: roles, refetch } = useApi<RoleView[]>("/roles");
  const { data: servers } = useApi<ServerDef[]>("/servers");
  const [editing, setEditing] = useState<any>(null);

  const save = async () => {
    if (!editing) return;
    try {
      const payload = { name: editing.role_name, servers: editing.servers || {} };
      if (editing.role_id) await PATCH(`/roles/${editing.role_id}`, payload);
      else await POST("/roles", payload);
      toast.success("Role saved");
      setEditing(null);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const togglePerm = (sid: string, bit: number, on: boolean) => {
    const cur = (editing.servers?.[sid] || "00000000").padEnd(8, "0").slice(0, 8).split("");
    cur[bit] = on ? "1" : "0";
    setEditing({ ...editing, servers: { ...editing.servers, [sid]: cur.join("") } });
  };

  return (
    <div className="p-6 max-w-5xl mx-auto space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Roles</h1>
          <p className="text-sm text-muted-foreground">Permission bundles assigned to users</p>
        </div>
        <Button onClick={() => setEditing({ role_name: "", servers: {} })}><Plus /> New role</Button>
      </div>

      {!roles ? <Skeleton className="h-40" /> : (
        <div className="grid gap-3 sm:grid-cols-2">
          {roles.map((r) => (
            <Card key={r.role_id}>
              <CardContent className="p-4">
                <div className="flex items-center justify-between">
                  <span className="font-medium">{r.role_name}</span>
                  <div className="flex gap-1">
                    <Button size="icon-sm" variant="ghost" onClick={() => setEditing({ ...r, servers: r.servers || {} })}>✏️</Button>
                    <Button size="icon-sm" variant="ghost" onClick={async () => {
                      if (!confirm(`Delete role "${r.role_name}"?`)) return;
                      await DEL(`/roles/${r.role_id}`);
                      refetch();
                    }}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
                  </div>
                </div>
                <div className="mt-2 flex flex-wrap gap-1">
                  {Object.keys(r.servers || {}).length === 0 ? (
                    <span className="text-xs text-muted-foreground">No servers</span>
                  ) : Object.entries(r.servers).map(([sid, mask]) => (
                    <Badge key={sid} variant="secondary">
                      {servers?.find((s) => s.server_id === sid)?.server_name || sid.slice(0, 8)}
                    </Badge>
                  ))}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
          <DialogHeader><DialogTitle>{editing?.role_id ? "Edit" : "New"} role</DialogTitle></DialogHeader>
          {editing && (
            <div className="grid gap-3">
              <div className="space-y-1.5">
                <Label>Role name</Label>
                <Input value={editing.role_name} onChange={(e) => setEditing({ ...editing, role_name: e.target.value })} />
              </div>
              <div className="space-y-1.5">
                <Label>Server access & permissions</Label>
                {(servers || []).map((s) => {
                  const mask = (editing.servers?.[s.server_id] || "").padEnd(8, "0").slice(0, 8);
                  const enabled = editing.servers?.[s.server_id] !== undefined;
                  return (
                    <div key={s.server_id} className={`
                      rounded-lg border p-3 ${enabled ? "border-primary/40" : "opacity-60"}
                    `}>
                      <label className="flex items-center gap-2 text-sm font-medium">
                        <input type="checkbox" checked={enabled}
                          onChange={(e) => {
                            const servers = { ...editing.servers };
                            if (e.target.checked) servers[s.server_id] = "11000000";
                            else delete servers[s.server_id];
                            setEditing({ ...editing, servers });
                          }} />
                        {s.server_name}
                      </label>
                      {enabled && (
                        <div className="mt-2 flex flex-wrap gap-1.5">
                          {PERMS.map(([name, bit]) => (
                            <label key={name} className={`
                              flex cursor-pointer items-center rounded border px-1.5 py-0.5 text-[10px] font-mono
                              ${mask[bit] === "1" ? "border-success/50 bg-success/10 text-success" : "opacity-50"}
                            `}>
                              <input type="checkbox" className="hidden"
                                checked={mask[bit] === "1"}
                                onChange={(e) => togglePerm(s.server_id, bit, e.target.checked)} />
                              {name}
                            </label>
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
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
