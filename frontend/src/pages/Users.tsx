import { useState } from "react";
import { toast } from "sonner";
import { Plus, Trash2, KeyRound, Save, UserX, UserCheck, ShieldCheck } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { GET, POST, PATCH, DEL, UserView, RoleView } from "@/api/client";
import { useAuth } from "@/hooks/AuthContext";
import { Button, Card, CardContent, Badge, Input, Label, Switch, Skeleton, Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, SimpleSelect } from "@/components/ui/overlays";
import { timeAgo } from "@/lib/utils";

export default function UsersPage() {
  const { user: me, refresh } = useAuth();
  const { data: users, refetch } = useApi<UserView[]>("/users");
  const { data: roles } = useApi<RoleView[]>("/roles");
  const [editing, setEditing] = useState<any>(null);
  const [newPassword, setNewPassword] = useState<{ id: number; name: string } | null>(null);
  const [pw, setPw] = useState("");
  const [keysFor, setKeysFor] = useState<UserView | null>(null);
  const [keys, setKeys] = useState<any[]>([]);
  const [newKey, setNewKey] = useState<{ name: string; full: boolean } | null>(null);
  const [createdToken, setCreatedToken] = useState<string | null>(null);

  const openKeys = async (u: UserView) => {
    setKeysFor(u);
    setKeys(await GET(`/users/${u.user_id}/key`));
    setCreatedToken(null);
  };

  const createKey = async () => {
    if (!newKey || !keysFor) return;
    try {
      const k = await POST(`/users/${keysFor.user_id}/key`, { name: newKey.name, full_access: newKey.full });
      setCreatedToken(k.token);
      setKeys(await GET(`/users/${keysFor.user_id}/key`));
      setNewKey(null);
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const save = async () => {
    if (!editing) return;
    try {
      const payload: any = {
        username: editing.username, email: editing.email || "",
        enabled: editing.enabled !== false, superuser: !!editing.superuser,
        lang: editing.lang || "en_EN", roles: editing.roles || [],
        panel_permissions: editing.panel_permissions || "000",
        limit_server_creation: Number(editing.limit_server_creation ?? -1),
        limit_user_creation: Number(editing.limit_user_creation ?? 0),
        limit_role_creation: Number(editing.limit_role_creation ?? 0),
      };
      if (editing.user_id) await PATCH(`/users/${editing.user_id}`, payload);
      else await POST("/users", { ...payload, password: editing.password });
      toast.success("User saved");
      setEditing(null);
      refetch();
      refresh();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const del = async (u: UserView) => {
    if (!confirm(`Delete user "${u.username}"?`)) return;
    try {
      await DEL(`/users/${u.user_id}`);
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const toggleRole = (rid: number, on: boolean) => {
    const cur: number[] = editing.roles || [];
    setEditing({ ...editing, roles: on ? [...cur, rid] : cur.filter((r) => r !== rid) });
  };

  const togglePanelPerm = (bit: number, on: boolean) => {
    const mask = (editing.panel_permissions || "000").padEnd(3, "0").slice(0, 3).split("");
    mask[bit] = on ? "1" : "0";
    setEditing({ ...editing, panel_permissions: mask.join("") });
  };

  return (
    <div className="p-6 max-w-6xl mx-auto space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Users</h1>
          <p className="text-sm text-muted-foreground">Panel accounts, permissions & API keys</p>
        </div>
        <Button onClick={() => setEditing({ username: "", enabled: true, superuser: false, roles: [], panel_permissions: "000", limit_server_creation: -1 })}>
          <Plus /> New user
        </Button>
      </div>

      <Card>
        <CardContent className="p-0">
          {!users ? <Skeleton className="h-48" /> : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User</TableHead>
                  <TableHead>Roles</TableHead>
                  <TableHead>Panel perms</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>Last login</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <TableRow key={u.user_id}>
                    <TableCell>
                      <div className="font-medium">{u.username}{u.user_id === me?.user_id && <span className="ml-1.5 text-xs text-primary">(you)</span>}</div>
                      <div className="text-xs text-muted-foreground">{u.email || "—"}</div>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {u.roles.length === 0 ? <span className="text-xs text-muted-foreground">—</span> :
                          u.roles.map((rid) => (
                            <Badge key={rid} variant="secondary">{roles?.find((r) => r.role_id === rid)?.role_name || `#${rid}`}</Badge>
                          ))}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1.5 font-mono text-xs">
                        {u.panel_permissions.padEnd(3, "0").slice(0, 3).split("").map((b, i) => (
                          <span key={i} className={b === "1" ? "text-success" : "text-muted-foreground/40"}>
                            {["S", "U", "R"][i]}
                          </span>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{timeAgo(u.created)}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{timeAgo(u.last_login)}</TableCell>
                    <TableCell>
                      {u.superuser ? <Badge variant="warning">superuser</Badge> : null}{" "}
                      {u.enabled ? <Badge variant="success">enabled</Badge> : <Badge variant="destructive">disabled</Badge>}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button size="icon-sm" variant="ghost" title="API keys" onClick={() => openKeys(u)}><KeyRound className="h-3.5 w-3.5" /></Button>
                        <Button size="icon-sm" variant="ghost" title="Password" onClick={() => { setNewPassword({ id: u.user_id, name: u.username }); setPw(""); }}><ShieldCheck className="h-3.5 w-3.5" /></Button>
                        <Button size="icon-sm" variant="ghost" title="Edit" onClick={() => setEditing(u)}>✏️</Button>
                        <Button size="icon-sm" variant="ghost" onClick={() => del(u)} disabled={u.user_id === me?.user_id}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* user editor */}
      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader><DialogTitle>{editing?.user_id ? "Edit" : "New"} user</DialogTitle></DialogHeader>
          {editing && (
            <div className="grid gap-3">
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label>Username</Label>
                  <Input value={editing.username} onChange={(e) => setEditing({ ...editing, username: e.target.value })} />
                </div>
                {!editing.user_id && (
                  <div className="space-y-1.5">
                    <Label>Password</Label>
                    <Input type="password" value={editing.password || ""} onChange={(e) => setEditing({ ...editing, password: e.target.value })} />
                  </div>
                )}
              </div>
              <div className="space-y-1.5">
                <Label>Email</Label>
                <Input value={editing.email || ""} onChange={(e) => setEditing({ ...editing, email: e.target.value })} />
              </div>
              <div className="flex gap-4">
                <label className="flex items-center gap-2 text-sm">
                  <Switch checked={editing.enabled !== false} onCheckedChange={(v) => setEditing({ ...editing, enabled: v })} /> Enabled
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <Switch checked={!!editing.superuser} onCheckedChange={(v) => setEditing({ ...editing, superuser: v })} /> Superuser
                </label>
              </div>
              <div className="space-y-1.5">
                <Label>Roles</Label>
                <div className="flex flex-wrap gap-1.5">
                  {(roles || []).map((r) => (
                    <label key={r.role_id} className={`
                      flex cursor-pointer items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs
                      ${(editing.roles || []).includes(r.role_id) ? "border-primary bg-primary/10" : "opacity-70"}
                    `}>
                      <input type="checkbox" className="hidden"
                        checked={(editing.roles || []).includes(r.role_id)}
                        onChange={(e) => toggleRole(r.role_id, e.target.checked)} />
                      {r.role_name}
                    </label>
                  ))}
                </div>
              </div>
              {!editing.superuser && (
                <>
                  <div className="space-y-1.5">
                    <Label>Panel permissions</Label>
                    <div className="flex gap-2">
                      {[["Servers", 0], ["Users", 1], ["Roles", 2]].map(([label, bit]: any) => (
                        <label key={bit} className="flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs">
                          <input type="checkbox"
                            checked={(editing.panel_permissions || "000").padEnd(3, "0")[bit] === "1"}
                            onChange={(e) => togglePanelPerm(bit, e.target.checked)} />
                          {label}
                        </label>
                      ))}
                    </div>
                  </div>
                  <div className="grid grid-cols-3 gap-2">
                    <div className="space-y-1">
                      <Label className="text-xs">Server limit</Label>
                      <Input type="number" value={editing.limit_server_creation ?? -1} onChange={(e) => setEditing({ ...editing, limit_server_creation: +e.target.value })} />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">User limit</Label>
                      <Input type="number" value={editing.limit_user_creation ?? 0} onChange={(e) => setEditing({ ...editing, limit_user_creation: +e.target.value })} />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Role limit</Label>
                      <Input type="number" value={editing.limit_role_creation ?? 0} onChange={(e) => setEditing({ ...editing, limit_role_creation: +e.target.value })} />
                    </div>
                  </div>
                </>
              )}
            </div>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button onClick={save}><Save /> Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* password dialog */}
      <Dialog open={!!newPassword} onOpenChange={(o) => !o && setNewPassword(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>Set password — {newPassword?.name}</DialogTitle></DialogHeader>
          <div className="space-y-1.5">
            <Label>New password</Label>
            <Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} />
            <p className="text-xs text-muted-foreground">All active sessions will be invalidated.</p>
          </div>
          <DialogFooter>
            <Button onClick={async () => {
              try {
                await POST(`/users/${newPassword!.id}/password`, { password: pw });
                toast.success("Password set");
                setNewPassword(null);
              } catch (e: any) { toast.error(e.message); }
            }}>Set password</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* API keys dialog */}
      <Dialog open={!!keysFor} onOpenChange={(o) => !o && setKeysFor(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>API keys — {keysFor?.username}</DialogTitle></DialogHeader>
          {createdToken && (
            <div className="rounded-md border border-warning/40 bg-warning/10 p-3">
              <div className="text-xs font-medium text-warning">Copy this token now — shown only once:</div>
              <code className="mt-1 block break-all font-mono text-xs">{createdToken}</code>
            </div>
          )}
          <div className="max-h-64 space-y-1.5 overflow-y-auto">
            {keys.length === 0 ? <div className="py-4 text-center text-sm text-muted-foreground">No API keys</div> :
              keys.map((k) => (
                <div key={k.token_id} className="flex items-center gap-2 rounded-md border p-2.5">
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium">{k.name}</div>
                    <div className="text-xs text-muted-foreground">#{k.token_id} · {k.full_access ? "full access" : "restricted"}</div>
                  </div>
                  <Button size="icon-sm" variant="ghost" onClick={async () => {
                    await DEL(`/users/${keysFor!.user_id}/key/${k.token_id}`);
                    setKeys(await GET(`/users/${keysFor!.user_id}/key`));
                  }}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
                </div>
              ))}
          </div>
          <div className="flex gap-2">
            <Input placeholder="New key name" value={newKey?.name || ""} onChange={(e) => setNewKey({ name: e.target.value, full: newKey?.full || false })} />
            <label className="flex items-center gap-1.5 text-xs">
              <input type="checkbox" checked={newKey?.full || false} onChange={(e) => setNewKey({ name: newKey?.name || "", full: e.target.checked })} />
              full
            </label>
            <Button size="sm" onClick={createKey} disabled={!newKey?.name}><Plus /> Create</Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
