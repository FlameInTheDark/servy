import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Play, Square, RotateCw, Skull, Send, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { POST, GET, ServerDef, realtime } from "@/api/client";
import { Button } from "@/components/ui/primitives";
import { cn } from "@/lib/utils";

export default function Console({ server }: { server: ServerDef }) {
  const termRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<XTerm | null>(null);
  const [command, setCommand] = useState("");
  const [busy, setBusy] = useState("");
  const [eulaOpen, setEulaOpen] = useState(server.eula_pending);

  useEffect(() => {
    if (!termRef.current) return;
    const term = new XTerm({
      theme: {
        background: "transparent",
        foreground: "#c9d1d9",
        green: "#3fb950",
        red: "#f85149",
        yellow: "#d29922",
        cursor: "transparent",
        selectionBackground: "#264f78",
      },
      fontSize: 12.5,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
      cursorBlink: false,
      disableStdin: true,
      convertEol: true,
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termRef.current);
    xtermRef.current = term;
    try { fit.fit(); } catch {}

    const onResize = () => { try { fit.fit(); } catch {} };
    window.addEventListener("resize", onResize);

    GET<string[]>(`/servers/${server.server_id}/terminal`).then((lines) => {
      lines.forEach((l) => term.writeln(l));
      term.scrollToBottom();
    }).catch(() => {});

    const rt = realtime();
    const topic = `server.${server.server_id}.terminal`;
    rt.subscribe(topic);
    const off = rt.on((msg) => {
      if (msg.event === "vterm_new_line" && msg.topic === topic) {
        (msg.data?.lines || []).forEach((l: string) => term.writeln(l));
        term.scrollToBottom();
      }
      if (msg.event === "send_eula_bootbox" && msg.data?.server_id === server.server_id) {
        setEulaOpen(true);
      }
    });

    return () => {
      rt.unsubscribe(topic);
      off();
      window.removeEventListener("resize", onResize);
      term.dispose();
    };
  }, [server.server_id]);

  const send = async () => {
    if (!command.trim()) return;
    try {
      await POST(`/servers/${server.server_id}/stdin`, { command });
      setCommand("");
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const act = async (action: string) => {
    setBusy(action);
    try {
      await POST(`/servers/${server.server_id}/${action}`);
      toast.success(`${action} — done`);
    } catch (e: any) {
      if (e.code === "EULA_AGREEMENT_REQUIRED") setEulaOpen(true);
      else toast.error(e.message);
    } finally {
      setBusy("");
    }
  };

  const agreeEula = async () => {
    try {
      await POST(`/servers/${server.server_id}/agree_eula`);
      setEulaOpen(false);
      toast.success("EULA accepted — starting server…");
      POST(`/servers/${server.server_id}/start`).catch(() => {});
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  return (
    <div className="flex h-full flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="success" onClick={() => act("start")} disabled={server.running || !!busy}>
          {busy === "start" ? <Loader2 className="animate-spin" /> : <Play />} Start
        </Button>
        <Button size="sm" variant="secondary" onClick={() => act("restart")} disabled={!server.running || !!busy}>
          {busy === "restart" ? <Loader2 className="animate-spin" /> : <RotateCw />} Restart
        </Button>
        <Button size="sm" variant="secondary" onClick={() => act("stop")} disabled={!server.running || !!busy}>
          {busy === "stop" ? <Loader2 className="animate-spin" /> : <Square />} Stop
        </Button>
        <Button size="sm" variant="destructive" onClick={() => act("kill")} disabled={!server.running || !!busy}>
          <Skull /> Kill
        </Button>
        <div className="ml-auto text-xs text-muted-foreground">
          {server.running ? "running" : server.state}
        </div>
      </div>

      {eulaOpen && (
        <div className="rounded-lg border border-warning/40 bg-warning/10 p-4 text-sm">
          <div className="font-medium text-warning">Minecraft EULA required</div>
          <p className="mt-1 text-muted-foreground">
            This server needs the Minecraft EULA accepted before it can start.
          </p>
          <Button size="sm" variant="warning" className="mt-2" onClick={agreeEula}>
            Agree &amp; start
          </Button>
        </div>
      )}

      <div className={cn(
        "relative flex-1 min-h-[420px] rounded-xl border bg-black/60 p-3 overflow-hidden",
        !server.running && "opacity-70"
      )}>
        <div ref={termRef} className="h-full w-full" />
      </div>

      <div className="flex gap-2">
        <input
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && send()}
          placeholder={server.running ? "Type a command… (e.g. say hello)" : "Server is offline — type 'start' to boot"}
          className="flex-1 rounded-md border border-input bg-background/60 px-3 py-2 text-sm font-mono focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
        <Button onClick={send}><Send /> Send</Button>
      </div>
    </div>
  );
}
