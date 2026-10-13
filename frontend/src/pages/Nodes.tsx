import { useState } from "react";
import { toast } from "sonner";
import { Network, Plus, Trash2, Server, RefreshCw } from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { POST, DEL } from "@/api/client";
import { Button, Card, CardContent, Badge, Input, Label, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";
import { timeAgo } from "@/lib/utils";

export default function NodesPage() {
  const { data: nodes, refetch } = useApi<any[]>("/panel/nodes", { pollMs: 15000 });
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ name: "", address: "", token: "" });

  const add = async () => {
    try {
      await POST("/panel/nodes", form);
      toast.success("Node registered — agent will connect");
      setAdding(false);
      refetch();
    } catch (e: any) { toast.error(e.message); }
  };

  return (
    <div className="p-6 max-w-5xl mx-auto space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold flex items-center gap-2"><Network className="h-6 w-6" /> Cluster Nodes</h1>
          <p className="text-sm text-muted-foreground">Proxmox-style multi-node management</p>
        </div>
        <Button onClick={() => setAdding(true)}><Plus /> Add node</Button>
      </div>

      <Card>
        <CardContent className="p-4">
          <div className="flex items-start gap-3 rounded-lg border border-primary/30 bg-primary/5 p-4 text-sm">
            <Server className="mt-0.5 h-5 w-5 shrink-0 text-primary" />
            <div className="space-y-1">
              <div className="font-medium">This panel (local)</div>
              <div className="text-xs text-muted-foreground">
                Servers without a node assignment run on this machine.
              </div>
            </div>
            <Badge variant="success" className="ml-auto">online</Badge>
          </div>
        </CardContent>
      </Card>

      {!nodes ? <Skeleton className="h-32" /> : nodes.length === 0 ? (
        <Card><CardContent className="py-10 text-center text-sm text-muted-foreground">
          No remote nodes. Run <code className="rounded bg-muted px-1.5 py-0.5">servy-agent --panel host:50051 --token &lt;token&gt;</code> on another machine,
          then register it here.
        </CardContent></Card>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {nodes.map((n) => (
            <Card key={n.node_id}>
              <CardContent className="p-4">
                <div className="flex items-center justify-between">
                  <div className="font-medium">{n.name}</div>
                  <Badge variant={n.status === "online" ? "success" : n.status === "registered" ? "warning" : "muted"}>
                    {n.status}
                  </Badge>
                </div>
                <div className="mt-1 font-mono text-xs text-muted-foreground">{n.address}</div>
                <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
                  <span>last seen {timeAgo(n.last_seen)} · {n.version || "?"}</span>
                  <Button size="icon-sm" variant="ghost" onClick={async () => {
                    if (!confirm(`Remove node "${n.name}"?`)) return;
                    await DEL(`/panel/nodes/${n.node_id}`);
                    refetch();
                  }}><Trash2 className="h-3.5 w-3.5 text-destructive" /></Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent>
          <DialogHeader><DialogTitle>Register cluster node</DialogTitle></DialogHeader>
          <div className="grid gap-3">
            <div className="space-y-1.5">
              <Label>Node name</Label>
              <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="agent-01" />
            </div>
            <div className="space-y-1.5">
              <Label>Agent gRPC address</Label>
              <Input value={form.address} onChange={(e) => setForm({ ...form, address: e.target.value })} placeholder="192.168.1.50:50052" className="font-mono" />
            </div>
            <div className="space-y-1.5">
              <Label>Cluster token (shared with the agent)</Label>
              <Input value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} className="font-mono" />
            </div>
            <p className="text-xs text-muted-foreground">
              On the agent machine: <code>servy agent --panel this-host:50051 --token &lt;same token&gt;</code>
            </p>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
            <Button onClick={add}>Register node</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
