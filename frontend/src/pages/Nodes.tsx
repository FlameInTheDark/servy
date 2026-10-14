import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  Network, Plus, Trash2, Server, RefreshCw, Copy, Check, Cpu, MemoryStick,
  Ticket, Power, AlertTriangle, ChevronRight, Settings2, Loader2,
} from "lucide-react";
import { Link } from "react-router-dom";
import { useApi } from "@/hooks/useApi";
import { POST, DEL } from "@/api/client";
import { Button, buttonVariants, Card, CardContent, Badge, Input, Label, Skeleton } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/overlays";
import { timeAgo, cn } from "@/lib/utils";

interface ClusterNode {
  node_id: string;
  name: string;
  address: string;
  role: string;
  status: string;
  online: boolean;
  version?: string;
  local?: boolean;
  capabilities?: string[];
  cpu?: number;
  mem_percent?: number;
  mem_used?: number;
  mem_total?: number;
  servers_running?: number;
  servers_total?: number;
  last_seen?: string;
  created?: string;
  last_error?: string;
}

interface ClusterPayload {
  nodes: ClusterNode[];
  tickets: { note: string; created: string; expires: string; expires_in: number }[];
  cluster_enabled: boolean;
  panel_address: string;
}

interface TicketPayload {
  ticket: string;
  expires: string;
  expires_in: number;
  panel_address: string;
  command: string;
}

function CopyButton({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="ghost"
      size="sm"
      className="gap-1.5"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          toast.success("Copied");
          window.setTimeout(() => setCopied(false), 1500);
        } catch {
          toast.error("Copy failed — select the text manually");
        }
      }}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
      {label ?? "Copy"}
    </Button>
  );
}

function UsageBar({ value, icon: Icon, label }: { value: number; icon: any; label: string }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  return (
    <div className="flex items-center gap-2">
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="text-muted-foreground"><Icon className="h-3.5 w-3.5" /></span>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
      <div className="h-1.5 w-16 overflow-hidden rounded-full bg-muted">
        <div
          className={cn("h-full rounded-full transition-all", pct > 85 ? "bg-destructive" : pct > 60 ? "bg-warning" : "bg-primary")}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="w-9 text-right font-mono text-[11px] tabular-nums text-muted-foreground">{pct.toFixed(0)}%</span>
    </div>
  );
}

function TicketDialog({ open, onOpenChange, panelAddress }: { open: boolean; onOpenChange: (v: boolean) => void; panelAddress: string }) {
  const [note, setNote] = useState("");
  const [ticket, setTicket] = useState<TicketPayload | null>(null);
  const [busy, setBusy] = useState(false);
  const [remaining, setRemaining] = useState(0);

  useEffect(() => {
    if (!open) { setTicket(null); setNote(""); return; }
  }, [open]);

  useEffect(() => {
    if (!ticket) return;
    setRemaining(ticket.expires_in);
    const t = window.setInterval(() => setRemaining((r) => Math.max(0, r - 1)), 1000);
    return () => window.clearInterval(t);
  }, [ticket]);

  const issue = async () => {
    setBusy(true);
    try {
      const res = await POST<TicketPayload>("/panel/nodes/tickets", { note });
      setTicket(res);
      toast.success("Join ticket issued");
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };

  const expired = ticket && remaining <= 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Add a node</DialogTitle>
        </DialogHeader>
        {!ticket ? (
          <div className="grid gap-4">
            <p className="text-sm text-muted-foreground">
              Install Servy on the other machine, then run one command there. The machine registers itself — no
              address or shared secret needed.
            </p>
            <div className="space-y-1.5">
              <Label>Label <span className="font-normal text-muted-foreground">(optional)</span></Label>
              <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="build-box, hetzner-01, …" />
            </div>
            <div className="rounded-lg border bg-muted/40 p-3 text-xs">
              <div className="mb-1 text-muted-foreground">This panel is reachable at</div>
              <div className="font-mono">{panelAddress || "—"}</div>
              <div className="mt-1 text-muted-foreground">
                The node must reach this address and port. Open it in your firewall today before adding nodes.
              </div>
            </div>
          </div>
        ) : (
          <div className="grid gap-4">
            <ol className="grid gap-3 text-sm">
              <li className="flex gap-3">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">1</span>
                <div className="min-w-0 flex-1">
                  <div className="font-medium">Install Servy on the new machine</div>
                  <div className="mt-1 flex items-center gap-2 rounded-md bg-muted/50 px-2 py-1.5">
                    <code className="min-w-0 flex-1 truncate font-mono text-xs">servy --help</code>
                    <CopyButton value="servy --help" label="" />
                  </div>
                </div>
              </li>
              <li className="flex gap-3">
                <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">2</span>
                <div className="min-w-0 flex-1">
                  <div className="font-medium">Run this join command there</div>
                  <div className="mt-1 flex items-center gap-2 rounded-md bg-muted/50 px-2 py-1.5">
                    <code className="min-w-0 flex-1 truncate font-mono text-xs">{ticket.command}</code>
                    <CopyButton value={ticket.command} label="" />
                  </div>
                  <div className={cn("mt-1 text-xs", expired ? "text-destructive" : "text-muted-foreground")}>
                    {expired
                      ? "This ticket expired — issue a new one."
                      : `Ticket valid for ${Math.floor(remaining / 60)}:${String(remaining % 60).padStart(2, "0")} more.`}
                  </div>
                </div>
              </li>
            </ol>
            <p className="rounded-md border border-primary/30 bg-primary/5 p-3 text-xs text-muted-foreground">
              The node keeps its own credential in <code>&lt;data&gt;/config/agent.json</code>. Restarting it later needs no ticket.
            </p>
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Close</Button>
          {!ticket ? (
            <Button onClick={issue} disabled={busy}>
              {busy ? <Loader2 className="animate-spin" /> : <Ticket />} Issue ticket
            </Button>
          ) : (
            <Button onClick={() => { setTicket(null); setNote(""); }}>New ticket</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function NodesPage() {
  const { data, loading, refetch } = useApi<ClusterPayload>("/panel/nodes", { pollMs: 10000 });
  const [adding, setAdding] = useState(false);
  const [pinging, setPinging] = useState<string | null>(null);
  const [removing, setRemoving] = useState<ClusterNode | null>(null);
  const [forceState, setForceState] = useState<{ node: ClusterNode; servers: string[] } | null>(null);

  const nodes = data?.nodes ?? [];
  const online = nodes.filter((n) => n.online).length;
  const hosted = useMemo(() => nodes.reduce((acc, n) => acc + (n.servers_total ?? 0), 0), [nodes]);

  const ping = async (node: ClusterNode) => {
    setPinging(node.node_id);
    try {
      const res = await POST<{ latency_ms?: number; version?: string }>(`/panel/nodes/${node.node_id}/ping`, {});
      toast.success(`${node.name} answered in ${res.latency_ms ?? 0} ms`);
      refetch();
    } catch (e: any) {
      toast.error(`${node.name}: ${e.message}`);
      refetch();
    } finally {
      setPinging(null);
    }
  };

  const remove = async (node: ClusterNode, force: boolean) => {
    try {
      await DEL(`/panel/nodes/${node.node_id}${force ? "?force=true" : ""}`);
      toast.success(`Removed ${node.name}`);
      setRemoving(null);
      setForceState(null);
      refetch();
      return true;
    } catch (e: any) {
      const message = e?.message || "removal failed";
      if (/still hosts/i.test(message)) {
        setForceState({ node, servers: e?.payload?.servers ?? [] });
        setRemoving(null);
        return false;
      }
      toast.error(message);
      return false;
    }
  };

  return (
    <div className="mx-auto max-w-5xl space-y-4 p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight"><Network className="h-6 w-6" /> Nodes</h1>
          <p className="text-sm text-muted-foreground">
            {nodes.length === 0
              ? "Run servers on this machine only"
              : `${online}/${nodes.length} online · ${hosted} managed server${hosted === 1 ? "" : "s"}`}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => refetch()}>
            <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} /> Refresh
          </Button>
          <Button onClick={() => setAdding(true)} disabled={data ? !data.cluster_enabled : false}>
            <Plus /> Add node
          </Button>
        </div>
      </div>

      {data && !data.cluster_enabled && (
        <div className="flex items-start gap-3 rounded-lg border border-warning/40 bg-warning/10 p-4 text-sm">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <div className="space-y-1">
            <div className="font-medium">Node connections are turned off</div>
            <p className="text-muted-foreground">
              Other machines cannot join this panel yet. Enable <span className="font-medium text-foreground">Cluster → Enable cluster nodes</span> in
              panel settings, restart the panel, then open port {data.panel_address.split(":").pop() || "50052"} in your firewall.
            </p>
            <Link
              to="/panel/config?section=cluster"
              className={cn(buttonVariants({ variant: "outline", size: "sm" }), "mt-1")}
            >
              <Settings2 className="h-3.5 w-3.5" /> Open panel settings
            </Link>
          </div>
        </div>
      )}

      {loading && !data ? (
        <div className="grid gap-3 sm:grid-cols-2"><Skeleton className="h-40" /><Skeleton className="h-40" /></div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {nodes.map((n) => (
            <Card key={n.node_id} className={cn(!n.online && "opacity-80")}>
              <CardContent className="space-y-3 p-4">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate font-medium">{n.name}</span>
                      {n.local && <Badge variant="outline" className="text-[10px]">this panel</Badge>}
                    </div>
                    <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground">{n.address || "—"}</div>
                  </div>
                  <Badge variant={n.online ? "success" : n.status === "unreachable" ? "destructive" : "muted"} className="shrink-0 capitalize">
                    {n.online ? "online" : n.status === "unreachable" ? "unreachable" : "offline"}
                  </Badge>
                </div>

                <div className="flex items-center justify-between text-xs text-muted-foreground">
                  <div className="flex items-center gap-3">
                    {!n.local && <UsageBar value={n.cpu ?? 0} icon={Cpu} label="CPU load reported by the node" />}
                    {!n.local && <UsageBar value={n.mem_percent ?? 0} icon={MemoryStick} label="Memory usage reported by the node" />}
                  </div>
                  <span className="inline-flex items-center gap-1">
                    <Server className="h-3 w-3" /> {n.servers_running ?? 0}/{n.servers_total ?? 0}
                  </span>
                </div>

                <div className="flex items-center justify-between border-t pt-3">
                  <span className="text-xs text-muted-foreground">
                    {n.online
                      ? n.version ? `v${n.version}` : "connected"
                      : n.last_seen ? `last seen ${timeAgo(n.last_seen)}` : "never connected"}
                    {n.last_error ? ` · ${n.last_error}` : ""}
                  </span>
                  <div className="flex items-center gap-1">
                    {!n.local && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-sm" disabled={pinging === n.node_id} onClick={() => ping(n)}>
                            <Power className={cn("h-3.5 w-3.5", pinging === n.node_id && "animate-pulse")} />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>Check the connection now</TooltipContent>
                      </Tooltip>
                    )}
                    {!n.local && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => {
                              if (!n.online) {
                                toast.error(`${n.name} is offline — remove it only if the machine is gone for good`);
                              }
                              setRemoving(n);
                            }}
                          >
                            <Trash2 className="h-3.5 w-3.5 text-destructive" />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>Remove from the cluster</TooltipContent>
                      </Tooltip>
                    )}
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Link
                          to={n.local ? "/servers" : `/servers?node=${n.node_id}`}
                          aria-label={`Show servers on ${n.name}`}
                          className={cn(buttonVariants({ variant: "ghost", size: "icon-sm" }))}
                        >
                          <ChevronRight className="h-4 w-4" />
                        </Link>
                      </TooltipTrigger>
                      <TooltipContent>Show this node's servers</TooltipContent>
                    </Tooltip>
                  </div>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {data && data.tickets.length > 0 && (
        <Card>
          <CardContent className="space-y-2 p-4">
            <div className="text-sm font-medium">Waiting for nodes to join</div>
            {data.tickets.map((t) => (
              <div key={`${t.note}-${t.created}`} className="flex items-center justify-between rounded-md border px-3 py-2 text-xs">
                <span>
                  {t.note || "unnamed"} <span className="text-muted-foreground">· issued {timeAgo(t.created)}</span>
                </span>
                <span className="text-muted-foreground">
                  {t.expires_in > 0 ? `expires in ${Math.floor(t.expires_in / 60)}m ${t.expires_in % 60}s` : "expired"}
                </span>
              </div>
            ))}
            <p className="text-xs text-muted-foreground">
              Tickets disappear once used. They are single-use and expire after 10 minutes, so a leaked command is useless after that.
            </p>
          </CardContent>
        </Card>
      )}

      <TicketDialog open={adding} onOpenChange={setAdding} panelAddress={data?.panel_address ?? ""} />

      {/* remove node */}
      <Dialog open={!!removing} onOpenChange={(v) => !v && setRemoving(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>Remove {removing?.name}?</DialogTitle></DialogHeader>
          <p className="text-sm text-muted-foreground">
            The node is disconnected and its credential is revoked. Files on that machine are not deleted.
            {(removing?.servers_total ?? 0) > 0 && ` This node still hosts ${removing?.servers_total} server${removing?.servers_total === 1 ? "" : "s"}.`}
          </p>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRemoving(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => removing && remove(removing, false)}>Remove node</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* node still hosts servers */}
      <Dialog open={!!forceState} onOpenChange={(v) => !v && setForceState(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>{forceState?.node.name} still hosts servers</DialogTitle></DialogHeader>
          <div className="space-y-3 text-sm">
            <p className="text-muted-foreground">
              These servers run on that machine. Removing the node hides them from the panel; their files stay on disk.
            </p>
            <ul className="rounded-md border bg-muted/30 p-3 text-xs">
              {(forceState?.servers ?? []).map((s) => <li key={s} className="font-mono">{s}</li>)}
            </ul>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setForceState(null)}>Keep the node</Button>
            <Button variant="destructive" onClick={() => forceState && remove(forceState.node, true)}>
              Remove anyway
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
