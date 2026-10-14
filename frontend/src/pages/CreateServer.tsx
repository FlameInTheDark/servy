import { useState, useEffect, useRef } from "react";
import { useNavigate, Link } from "react-router-dom";
import { toast } from "sonner";
import {
  Upload, Download, Terminal, Gamepad2, ArrowLeft, ArrowRight, Loader2, FileArchive, Container, Cpu, AlertTriangle,
  Monitor, X, Network, HardDrive, CircleHelp, Check, Rocket, SlidersHorizontal, Server as ServerIcon,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { api, GET, SteamGame, SteamLaunchOption, JavaInstall, realtime } from "@/api/client";

interface ShellInfo { id: string; label: string; path?: string; available: boolean; platform: string }
import { Card, CardContent, CardHeader, CardTitle, Button, Input, Label, Badge, Skeleton, Switch, Textarea } from "@/components/ui/primitives";
import { SimpleSelect, Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/overlays";
import { cn } from "@/lib/utils";

const CREATE_TYPES = [
  { id: "import_zip", label: "Import Zip", desc: "Upload a .zip with an existing server folder", icon: Upload },
  { id: "download_jar", label: "Download Jar", desc: "Minecraft jar from BigBucket or a direct URL", icon: Download },
  { id: "steam_game", label: "SteamCMD Game", desc: "Any Steam game server (Valheim, CS2, …)", icon: Gamepad2 },
  { id: "raw_exec", label: "Custom / Raw", desc: "Existing directory and your own start command", icon: Terminal },
];

const STEP_LABELS = ["Choose a type", "Configure", "Review & create"];

function defaultSteamOptions(game?: SteamGame): Record<string, string> {
  const values: Record<string, string> = {};
  for (const option of game?.launch_options || []) {
    if (option.source) continue;
    if (option.default !== undefined) values[option.key] = option.default;
    else if (option.type === "boolean") values[option.key] = "false";
  }
  return values;
}

/* --------------------------- small shared pieces --------------------------- */

function HelpHint({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label="More information"
          className="text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none"
        >
          <CircleHelp className="h-3.5 w-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[280px] leading-relaxed">{text}</TooltipContent>
    </Tooltip>
  );
}

function SectionCard({ icon, title, hint, description, children }: {
  icon: React.ReactNode;
  title: string;
  hint?: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader className="pb-4">
        <div className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary/10 text-primary">{icon}</span>
          <CardTitle className="text-base">{title}</CardTitle>
          {hint && <HelpHint text={hint} />}
        </div>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  );
}

function Stepper({ step, onJump }: { step: number; onJump: (s: number) => void }) {
  return (
    <ol className="mb-6 flex flex-wrap items-center gap-x-2 gap-y-2">
      {STEP_LABELS.map((label, index) => {
        const number = index + 1;
        const done = number < step;
        const active = number === step;
        return (
          <li key={label} className="flex items-center gap-2">
            <button
              type="button"
              disabled={number > step}
              onClick={() => onJump(number)}
              className={cn(
                "flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                active && "border-primary bg-primary/10 text-foreground",
                done && "border-border text-muted-foreground hover:border-primary/60 hover:text-foreground",
                !active && !done && "border-dashed border-border text-muted-foreground/70",
                number > step && "cursor-default opacity-60",
              )}
            >
              <span className={cn(
                "flex h-5 w-5 items-center justify-center rounded-full text-[11px] font-semibold",
                active ? "bg-primary text-primary-foreground" : done ? "bg-success/15 text-success" : "bg-muted text-muted-foreground",
              )}>
                {done ? <Check className="h-3 w-3" /> : number}
              </span>
              {label}
            </button>
            {number < STEP_LABELS.length && <span className="hidden h-px w-6 bg-border sm:block" />}
          </li>
        );
      })}
    </ol>
  );
}

function SummaryRow({ label, value, mono, masked }: { label: string; value: React.ReactNode; mono?: boolean; masked?: boolean }) {
  if (value === undefined || value === null || value === "" || value === false) return null;
  return (
    <div className="flex items-baseline justify-between gap-4 border-b py-1.5 last:border-0">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("truncate text-right", mono && "font-mono text-xs", masked && "text-muted-foreground")}>
        {masked ? "•••••• (hidden)" : value}
      </span>
    </div>
  );
}

function SummaryGroup({ title, onEdit, children }: { title: string; onEdit?: () => void; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border p-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-sm font-medium">{title}</div>
        {onEdit && (
          <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={onEdit}>
            Edit
          </Button>
        )}
      </div>
      <div className="text-sm">{children}</div>
    </div>
  );
}

/* ---------------------------------- page ----------------------------------- */

export default function CreateServerPage() {
  const nav = useNavigate();
  const [step, setStep] = useState(1);
  const [createType, setCreateType] = useState("");
  const [form, setForm] = useState<any>({
    name: "", type: "minecraft-java", port: 25565, min_mem: "512M", max_mem: "1024M",
    auto_start: false, crash_detection: true,
    execution_backend: "process", java_selection: "", docker_image: "", docker_ports: "", docker_env: "",
  });
  const [busy, setBusy] = useState(false);
  const [steamSearch, setSteamSearch] = useState("");
  const { data: steamGames, loading: steamLoading } = useApi<SteamGame[]>("/panel/steam/catalog");
  const { data: jarCache } = useApi<any>("/panel/JarCache");
  const { data: javaData } = useApi<{ installs: JavaInstall[] }>("/panel/java");
  const { data: dockerData } = useApi<{ available: boolean }>("/panel/docker");
  const { data: shellsData } = useApi<{ default: string; platform: string; shells: ShellInfo[] }>("/panel/shells");
  const { data: cluster } = useApi<{ nodes: any[]; cluster_enabled: boolean; panel_address: string }>("/panel/nodes", { pollMs: 30000 });
  const clusterNodes = (cluster?.nodes || []).filter((n: any) => !n.local);
  const onlineNodes = clusterNodes.filter((n: any) => n.online);
  const targetNode = form.node ? clusterNodes.find((n: any) => n.node_id === form.node) : undefined;
  const shells = shellsData?.shells || [];
  const javaInstalls = javaData?.installs || [];
  const pickedSteamGame = form.steam_slug ? steamGames?.find((g) => g.slug === form.steam_slug) : undefined;
  const windowsOnlySteam = !!pickedSteamGame?.windows_only;
  const isDocker = form.execution_backend === "docker" || windowsOnlySteam;

  // SteamCMD live output
  const [steamOutput, setSteamOutput] = useState<string[]>([]);
  const [steamInstalling, setSteamInstalling] = useState(false);
  const outputRef = useRef<HTMLDivElement>(null);
  const rtRef = useRef<ReturnType<typeof realtime> | null>(null);
  const progressUnsubscribeRef = useRef<(() => void) | null>(null);

  const set = (k: string, v: any) => setForm((f: any) => ({ ...f, [k]: v }));
  const setSteamOption = (key: string, value: string) => set("steam_options", {
    ...(form.steam_options || {}), [key]: value,
  });

  const submit = async () => {
    setBusy(true);
    let unsubscribe: (() => void) | null = null;
    const isSteamInstall = createType === "steam_game";

    if (isSteamInstall) {
      setSteamInstalling(true);
      setSteamOutput([]);
      const rt = realtime();
      rtRef.current = rt;
      unsubscribe = rt.on((msg) => {
        if (msg.event === "steamcmd_output" && msg.data?.line) {
          setSteamOutput((prev) => [...prev.slice(-499), msg.data.line]);
        }
      });
      progressUnsubscribeRef.current = unsubscribe;

      // The panel may be temporarily offline; don't block creation forever if
      // the WebSocket handshake never completes. The topic is retained for the
      // next reconnect, while the HTTP request remains the source of truth.
      await Promise.race([
        rt.waitForConnection().catch(() => undefined),
        new Promise<void>((resolve) => window.setTimeout(resolve, 2500)),
      ]);
      rt.subscribe("creation");
    }

    try {
      const payload: any = { ...form, node: form.node || "" };
      if (isDocker) {
        payload.execution_backend = "docker";
        payload.docker_ports = (form.docker_ports || "").split(",").map((x: string) => x.trim()).filter(Boolean);
        payload.docker_env = Object.fromEntries(
          (form.docker_env || "").split("\n").map((l: string) => l.trim()).filter((l: string) => l.includes("="))
            .map((l: string) => { const i = l.indexOf("="); return [l.slice(0, i), l.slice(i + 1)]; }),
        );
      } else {
        delete payload.docker_image; delete payload.docker_ports; delete payload.docker_env;
      }

      const res = await api<any>("POST", "/servers", payload);
      toast.success(`Server "${res.name}" created`);
      if (res.steam_output) setSteamOutput(res.steam_output);
      setSteamInstalling(false);
      nav(`/servers/${res.server_id}`);
    } catch (e: any) {
      toast.error(e.message);
      if (e.message) setSteamOutput((prev) => [...prev, `ERROR: ${e.message}`]);
      setSteamInstalling(false);
    } finally {
      if (unsubscribe) {
        unsubscribe();
        progressUnsubscribeRef.current = null;
      }
      if (isSteamInstall) rtRef.current?.unsubscribe("creation");
      setBusy(false);
    }
  };

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      progressUnsubscribeRef.current?.();
      progressUnsubscribeRef.current = null;
      rtRef.current?.unsubscribe("creation");
      rtRef.current = null;
    };
  }, []);

  // Keep the SteamCMD log pinned to the newest line.
  useEffect(() => {
    if (outputRef.current) outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [steamOutput, steamInstalling]);

  const [uploadPct, setUploadPct] = useState<number | null>(null);

  const uploadZip = async (file: File) => {
    // Chunked upload: keeps every request small (works behind strict reverse
    // proxies, no giant single body) and gives us real progress for the
    // multi-GB archives people actually import.
    const CHUNK = 4 * 1024 * 1024;
    const total = Math.max(1, Math.ceil(file.size / CHUNK));
    const uploadId = Math.random().toString(36).slice(2);
    set("archive_name", file.name);
    set("archive_path", "");
    setUploadPct(0);
    try {
      let finalPath = "";
      for (let i = 0; i < total; i++) {
        const res = await fetch(
          `/api/v1/servers/import/upload?name=${encodeURIComponent(file.name)}&upload_id=${uploadId}&chunk=${i}&total=${total}`,
          {
            method: "POST",
            headers: {
              Authorization: `Bearer ${localStorage.getItem("servy_token")}`,
              "Content-Type": "application/octet-stream",
            },
            body: file.slice(i * CHUNK, (i + 1) * CHUNK),
          }
        );
        const j = await res.json().catch(() => null);
        if (!res.ok || !j || j.status !== "ok") {
          throw new Error(j?.error_data || `upload failed (HTTP ${res.status})`);
        }
        if (j.data?.path) finalPath = j.data.path;
        setUploadPct(Math.round(((i + 1) / total) * 100));
      }
      if (!finalPath) throw new Error("upload failed (no path returned)");
      set("archive_path", finalPath);
      toast.success(`Uploaded ${file.name}`);
    } catch (e: any) {
      toast.error(e?.message || "upload failed");
      set("archive_name", "");
    } finally {
      setUploadPct(null);
    }
  };

  const pickedSteam = pickedSteamGame;
  const filteredSteamGames = (steamGames || []).filter((game) => {
    const q = steamSearch.trim().toLowerCase();
    return !q || game.name.toLowerCase().includes(q) || game.slug.toLowerCase().includes(q) || String(game.app_id).includes(q);
  });
  const jarEntries: { name: string; version: string; url: string }[] =
    jarCache?.categories?.flatMap((c: any) => c.servers || []) || [];

  let step2Error = "";
  if (!form.name?.trim()) step2Error = "Enter a name for this server.";
  else if (!Number.isInteger(form.port) || form.port < 1 || form.port > 65535) step2Error = "Enter a port between 1 and 65535.";
  else if (createType === "import_zip" && !form.archive_path) step2Error = "Upload a ZIP archive or enter its path on the host.";
  else if (createType === "download_jar" && !form.jar_url) step2Error = "Choose a server JAR or enter its download URL.";
  else if (createType === "steam_game" && !form.steam_slug) step2Error = "Choose a SteamCMD game.";
  else if (createType === "raw_exec" && (!form.executable || !form.execution_command)) step2Error = "Provide an executable and its start command.";
  else if (windowsOnlySteam && !dockerData?.available) step2Error = "This game requires Docker, but no Docker daemon is available to Servy.";
  if (!step2Error && createType === "steam_game" && pickedSteam && !pickedSteam.query_port && Number(form.port) >= 65535) {
    step2Error = "This game's query port would be outside 1–65535; choose a lower game port or set an explicit query_port in its manifest.";
  }
  if (!step2Error && createType === "steam_game" && pickedSteam) {
    for (const option of pickedSteam.launch_options || []) {
      let value = option.source === "server_name" ? String(form.name || "").trim()
        : option.source === "port" ? String(form.port ?? "")
          : option.source === "query_port" ? String(pickedSteam.query_port || Number(form.port) + 1)
            : option.source === "save_dir" ? "server save directory"
              : (form.steam_options || {})[option.key] ?? option.default ?? (option.type === "boolean" ? "false" : "");
      if (option.required && !value.trim()) {
        step2Error = `${option.label} is required.`;
        break;
      }
      if (!value && !option.required) continue;
      if ((option.type === "text" || option.type === "password") && option.source !== "port" && option.source !== "query_port" && option.source !== "save_dir") {
        const length = [...value].length;
        if (option.min_length !== undefined && length < option.min_length) {
          step2Error = `${option.label} must be at least ${option.min_length} characters.`;
          break;
        }
        if (option.max_length !== undefined && length > option.max_length) {
          step2Error = `${option.label} must be no longer than ${option.max_length} characters.`;
          break;
        }
      }
      if (option.type === "number") {
        const number = Number(value);
        if (!Number.isInteger(number)) {
          step2Error = `${option.label} must be a whole number.`;
          break;
        }
        if (option.min !== undefined && number < option.min) {
          step2Error = `${option.label} must be at least ${option.min}.`;
          break;
        }
        if (option.max !== undefined && number > option.max) {
          step2Error = `${option.label} must be no greater than ${option.max}.`;
          break;
        }
      }
      if (option.type === "select" && !(option.options || []).some((choice) => choice.value === value)) {
        step2Error = `Choose a valid value for ${option.label}.`;
        break;
      }
    }
  }

  const typeLabel = CREATE_TYPES.find((t) => t.id === createType)?.label || createType;
  const steamOptionRows = (pickedSteam?.launch_options || []).filter((option) => !option.source);
  const automaticOptionRows = (pickedSteam?.launch_options || []).filter((option) => option.source);

  const renderSteamOption = (option: SteamLaunchOption) => {
    const value = (form.steam_options || {})[option.key] ?? option.default ?? (option.type === "boolean" ? "false" : "");
    return (
      <div key={option.key} className="space-y-1.5">
        <div className="flex items-center gap-1.5">
          <Label htmlFor={`opt-${option.key}`}>{option.label}{option.required && <span className="ml-1 text-destructive">*</span>}</Label>
          {option.description && <HelpHint text={option.description} />}
        </div>
        {option.type === "boolean" ? (
          <div className="flex min-h-9 items-center">
            <Switch checked={value === "true"} onCheckedChange={(checked) => setSteamOption(option.key, checked ? "true" : "false")} />
          </div>
        ) : option.type === "select" ? (
          <SimpleSelect
            value={value}
            onValueChange={(selected) => setSteamOption(option.key, selected)}
            placeholder={option.required ? "Choose an option…" : "Use manifest default"}
            options={(option.options || []).map((choice) => ({ value: choice.value, label: choice.label }))}
            className="w-full"
          />
        ) : (
          <Input
            id={`opt-${option.key}`}
            type={option.type === "password" ? "password" : option.type === "number" ? "number" : "text"}
            value={value}
            min={option.min}
            max={option.max}
            minLength={option.min_length}
            maxLength={option.max_length}
            required={option.required}
            autoComplete={option.type === "password" ? "new-password" : undefined}
            onChange={(e) => setSteamOption(option.key, e.target.value)}
          />
        )}
        {option.description && !option.required && <p className="text-xs text-muted-foreground">{option.description}</p>}
      </div>
    );
  };

  return (
    <div className="mx-auto max-w-3xl p-6 pb-0">
      <Link to="/servers" className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground">
        <ArrowLeft className="h-3.5 w-3.5" /> Back to servers
      </Link>

      <div className="mb-6 mt-4 flex items-center gap-3">
        <span className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <Rocket className="h-5 w-5" />
        </span>
        <div>
          <h1 className="text-2xl font-bold leading-tight">Create Server</h1>
          <p className="text-sm text-muted-foreground">
            Step {step} of 3 · {step === 1 ? "pick how the server is built" : step === 2 ? typeLabel : "check and create"}
          </p>
        </div>
      </div>

      <Stepper step={step} onJump={setStep} />

      {step === 1 && (
        <div className="grid gap-4 sm:grid-cols-2">
          {CREATE_TYPES.map((t) => (
            <button
              key={t.id}
              onClick={() => { setCreateType(t.id); set("create_type", t.id); setStep(2); }}
              className={cn(
                "group rounded-xl border bg-card p-5 text-left transition-all hover:border-primary/60 hover:shadow-lg",
                createType === t.id && "border-primary ring-1 ring-primary"
              )}
            >
              <span className="mb-3 flex h-10 w-10 items-center justify-center rounded-lg bg-primary/10 text-primary transition-colors group-hover:bg-primary/15">
                <t.icon className="h-5 w-5" />
              </span>
              <div className="font-semibold">{t.label}</div>
              <div className="mt-1 text-sm text-muted-foreground">{t.desc}</div>
            </button>
          ))}
        </div>
      )}

      {step === 2 && (
        <div className="space-y-4">
          {/* where the server runs */}
          <SectionCard
            icon={<Network className="h-4 w-4" />}
            title="Where should it run?"
            hint="This panel or another machine that joined the cluster as a node. Files and processes live on the chosen machine."
            description={targetNode ? `Files are streamed to ${targetNode.name} automatically.` : undefined}
          >
            <div className="grid gap-2 sm:grid-cols-2">
              <button
                type="button"
                onClick={() => set("node", "")}
                className={cn(
                  "rounded-lg border p-3 text-left text-sm transition-all hover:border-primary/60",
                  !form.node && "border-primary ring-1 ring-primary"
                )}
              >
                <div className="flex items-center gap-2 font-medium"><HardDrive className="h-3.5 w-3.5" /> This panel</div>
                <div className="mt-0.5 text-xs text-muted-foreground">Runs on the machine hosting this panel.</div>
              </button>
              {onlineNodes.length === 0 ? (
                <div className="rounded-lg border border-dashed p-3 text-xs text-muted-foreground">
                  No other nodes online. Add one under <span className="font-medium text-foreground">Nodes</span> and it appears here.
                </div>
              ) : (
                onlineNodes.map((n: any) => (
                  <button
                    key={n.node_id}
                    type="button"
                    onClick={() => set("node", n.node_id)}
                    className={cn(
                      "rounded-lg border p-3 text-left text-sm transition-all hover:border-primary/60",
                      form.node === n.node_id && "border-primary ring-1 ring-primary"
                    )}
                  >
                    <div className="flex items-center gap-2 font-medium">
                      <Network className="h-3.5 w-3.5" /> {n.name}
                    </div>
                    <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
                      {n.address} · {n.servers_running ?? 0}/{n.servers_total ?? 0} servers
                    </div>
                  </button>
                ))
              )}
            </div>
            {targetNode && (
              <p className="text-xs text-muted-foreground">
                Enter paths as they exist on <span className="font-medium text-foreground">{targetNode.name}</span>, not on this panel.
              </p>
            )}
          </SectionCard>

          {/* name, port and type-specific fields */}
          <SectionCard
            icon={<ServerIcon className="h-4 w-4" />}
            title="Server details"
            hint="The name is shown in the panel and used by some games as the server name."
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="server-name">Server name</Label>
                <Input id="server-name" value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="My Awesome Server" />
              </div>
              <div className="space-y-1.5">
                <div className="flex items-center gap-1.5">
                  <Label htmlFor="server-port">Port</Label>
                  <HelpHint text="The game port clients connect to. Steam games usually take the next port for queries; some games also need it open in your firewall." />
                </div>
                <Input id="server-port" type="number" value={form.port} onChange={(e) => set("port", +e.target.value)} />
              </div>
            </div>

            {createType === "import_zip" && (
              <div className="space-y-3">
                <Label className="flex items-center gap-2"><FileArchive className="h-4 w-4" /> Server archive</Label>
                {uploadPct !== null ? (
                  <div className="space-y-2 rounded-xl border-2 border-dashed border-border p-8">
                    <div className="flex items-center justify-between text-sm text-muted-foreground">
                      <span className="flex items-center gap-2"><Upload className="h-4 w-4 animate-pulse" /> Uploading {form.archive_name}…</span>
                      <span className="font-mono">{uploadPct}%</span>
                    </div>
                    <div className="h-2 overflow-hidden rounded-full bg-muted">
                      <div className="h-full bg-primary transition-all" style={{ width: `${uploadPct}%` }} />
                    </div>
                  </div>
                ) : (
                  <label className="flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-border p-8 text-center transition-colors hover:border-primary/60 hover:bg-accent/30">
                    <Upload className="h-8 w-8 text-muted-foreground" />
                    <span className="text-sm text-muted-foreground">
                      {form.archive_name || "Click to choose a .zip file"}
                    </span>
                    <input type="file" accept=".zip" className="hidden"
                      onChange={(e) => e.target.files?.[0] && uploadZip(e.target.files[0])} />
                  </label>
                )}
                <Input placeholder="…or absolute archive path on the host" value={form.archive_path || ""} onChange={(e) => set("archive_path", e.target.value)} />
              </div>
            )}

            {createType === "download_jar" && (
              <div className="space-y-3">
                <div className="space-y-1.5">
                  <Label>BigBucket catalog</Label>
                  <SimpleSelect
                    value={form.jar_url || ""}
                    onValueChange={(v) => { set("jar_url", v); set("jar_name", v.split("/").pop()); }}
                    placeholder="Pick a server jar…"
                    options={jarEntries.slice(0, 200).map((j: any) => ({ value: j.url, label: `${j.name} ${j.version || ""}` }))}
                    className="w-full"
                  />
                </div>
                <div className="space-y-1.5">
                  <Label>…or direct jar URL</Label>
                  <Input value={form.jar_url || ""} onChange={(e) => set("jar_url", e.target.value)} placeholder="https://…/server.jar" />
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-1.5">
                      <Label>Min memory</Label>
                      <HelpHint text="Passed as -Xms. Editable later in the server view under Settings → Startup parameters." />
                    </div>
                    <Input value={form.min_mem} onChange={(e) => set("min_mem", e.target.value)} placeholder="512M" />
                  </div>
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-1.5">
                      <Label>Max memory</Label>
                      <HelpHint text="Passed as -Xmx. Give the JVM as much as the server can spare — modpacks often need 4–8 GB." />
                    </div>
                    <Input value={form.max_mem} onChange={(e) => set("max_mem", e.target.value)} placeholder="1024M" />
                  </div>
                </div>
                <div className="space-y-1.5">
                  <Label>Server type</Label>
                  <SimpleSelect value={form.type} onValueChange={(v) => set("type", v)} options={[
                    { value: "minecraft-java", label: "Minecraft (Java)" },
                    { value: "minecraft-bedrock", label: "Minecraft (Bedrock)" },
                    { value: "custom", label: "Custom" },
                  ]} />
                </div>
              </div>
            )}

            {createType === "steam_game" && (
              <div className="space-y-3">
                {steamLoading ? (
                  <Skeleton className="h-40" />
                ) : (
                  <div className="space-y-2">
                    <Input value={steamSearch} onChange={(e) => setSteamSearch(e.target.value)} placeholder="Search games by name, slug, or Steam App ID" aria-label="Search SteamCMD games" />
                    <div className="grid max-h-72 gap-2 overflow-y-auto pr-1 sm:grid-cols-2">
                      {filteredSteamGames.length === 0 ? (
                        <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground sm:col-span-2">No games match that search. Ask an administrator to add a game manifest in Game manifests.</div>
                      ) : filteredSteamGames.map((g) => (
                        <button key={g.slug}
                          onClick={() => {
                            set("steam_slug", g.slug);
                            set("name", form.name || g.name);
                            set("port", g.port);
                            set("steam_options", defaultSteamOptions(g));
                            set("type", "steam_cmd");
                            // Windows-only titles ship a Wine image in the catalog:
                            // preselect it so the field is never blank.
                            if (g.windows_only) {
                              set("execution_backend", "docker");
                              set("docker_image", g.docker_image || form.docker_image || "");
                            }
                          }}
                          className={cn(
                            "rounded-lg border p-3 text-left transition-all hover:border-primary/60",
                            form.steam_slug === g.slug && "border-primary ring-1 ring-primary"
                          )}>
                          <div className="flex items-center justify-between gap-2">
                            <span className="truncate font-medium">{g.name}</span>
                            <span className="flex shrink-0 gap-1">
                              {g.custom && <Badge variant="muted" className="text-[10px]">custom</Badge>}
                              {g.windows_only && <Badge variant="destructive" className="text-[10px]">windows</Badge>}
                              <Badge variant="secondary">#{g.app_id}</Badge>
                            </span>
                          </div>
                          <div className="mt-1 text-xs text-muted-foreground">port {g.port}</div>
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}

            {createType === "raw_exec" && (
              <div className="space-y-3">
                <div className="space-y-1.5">
                  <Label>Server type</Label>
                  <SimpleSelect value={form.type} onValueChange={(v) => set("type", v)} options={[
                    { value: "minecraft-java", label: "Minecraft (Java)" },
                    { value: "minecraft-bedrock", label: "Minecraft (Bedrock)" },
                    { value: "steam_cmd", label: "Steam" },
                    { value: "custom", label: "Custom" },
                  ]} />
                </div>
                <div className="space-y-1.5">
                  <Label>Executable (relative to server dir)</Label>
                  <Input value={form.executable || ""} onChange={(e) => set("executable", e.target.value)} placeholder="server.jar / run.sh / bedrock_server" />
                </div>
                <div className="space-y-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label>Execution command</Label>
                    <HelpHint text="Run from the server directory at start. You can change it any time in the server view under Settings." />
                  </div>
                  <Textarea value={form.execution_command || ""} onChange={(e) => set("execution_command", e.target.value)}
                    placeholder="java -Xms512M -Xmx1024M -jar server.jar nogui" className="font-mono text-xs" />
                </div>
                <div className="space-y-1.5">
                  <Label>Stop command</Label>
                  <Input value={form.stop_command || ""} onChange={(e) => set("stop_command", e.target.value)} placeholder="stop" />
                </div>
              </div>
            )}
          </SectionCard>

          {/* steam launch parameters */}
          {createType === "steam_game" && (steamOptionRows.length > 0 || automaticOptionRows.length > 0) && (
            <SectionCard
              icon={<SlidersHorizontal className="h-4 w-4" />}
              title="Launch parameters"
              hint="Passed to the server command as separate arguments, never through a shell. Everything here can be changed later in the server view."
              description={pickedSteam ? `${pickedSteam.name} · app ${pickedSteam.app_id}` : undefined}
            >
              {automaticOptionRows.map((option) => (
                <div key={option.key} className="rounded-md bg-muted/40 px-3 py-2">
                  <div className="text-sm font-medium">
                    {option.label} <span className="text-xs font-normal text-muted-foreground">(automatic)</span>
                  </div>
                  {option.description && <p className="mt-0.5 text-xs text-muted-foreground">{option.description}</p>}
                </div>
              ))}
              {steamOptionRows.map(renderSteamOption)}
            </SectionCard>
          )}

          {/* execution environment */}
          <SectionCard
            icon={isDocker ? <Container className="h-4 w-4" /> : <Cpu className="h-4 w-4" />}
            title="Execution environment"
            description={windowsOnlySteam
              ? `${pickedSteam?.name} has no Linux build: it runs in Docker with a Wine image, and the container publishes the game port itself.`
              : undefined}
          >
            {windowsOnlySteam && (
              <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs">
                <AlertTriangle className="mt-0.5 h-4 w-4 text-destructive" />
                <div>
                  <b>{pickedSteam?.name}</b> is Windows-only. SteamCMD fetches the Windows depot automatically.
                </div>
              </div>
            )}
            {createType === "steam_game" ? (
              <p className="rounded-md border bg-muted/30 p-3 text-xs text-muted-foreground">
                Steam games run as an argument vector — Servy never interprets typed values as shell text.
              </p>
            ) : (
              <div className="space-y-1.5">
                <div className="flex items-center gap-1.5">
                  <Label>Terminal</Label>
                  <HelpHint text={
                    shellsData?.platform === "windows"
                      ? "Windows default: cmd (batch files). Pick PowerShell or Git Bash when the start script needs them."
                      : "Linux default: bash. Pick PowerShell or Git Bash when the start script needs them."
                  } />
                </div>
                <SimpleSelect
                  value={form.shell || "auto"}
                  onValueChange={(v) => set("shell", v === "auto" ? "" : v)}
                  options={shells.map((sh) => ({
                    value: sh.id,
                    label: sh.available ? sh.label : `${sh.label} — unavailable`,
                  }))}
                />
              </div>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <div className="flex items-center gap-1.5">
                  <Label>Backend</Label>
                  <HelpHint text={dockerData?.available
                    ? "Host process runs the server directly. Docker runs it in a container with its own image, ports and environment."
                    : "Docker daemon not reachable on this host — containers are unavailable."} />
                </div>
                <SimpleSelect
                  value={windowsOnlySteam ? "docker" : form.execution_backend}
                  onValueChange={(v) => set("execution_backend", v)}
                  options={[
                    { value: "process", label: "Host process (default)" },
                    { value: "docker", label: "Docker container" },
                  ]}
                />
              </div>
              {!isDocker && (form.type === "minecraft-java") && (
                <div className="space-y-1.5">
                  <div className="flex items-center gap-1.5">
                    <Label>Java version</Label>
                    <HelpHint text="≤1.16 → Java 8 · 1.17 → 16+ · 1.18–1.20.4 → 17 · 1.20.5+ → 21" />
                  </div>
                  <SimpleSelect
                    value={form.java_selection || ""}
                    onValueChange={(v) => set("java_selection", v)}
                    options={[
                      { value: "", label: "Use command as written" },
                      { value: "java", label: "System default (java)" },
                      ...javaInstalls.map((j) => ({ value: j.path, label: `Java ${j.major} — ${j.vendor} (${j.version})` })),
                    ]}
                  />
                </div>
              )}
            </div>
            {isDocker && (
              <div className="grid gap-4 border-t pt-4 sm:grid-cols-2">
                <div className="space-y-1.5 sm:col-span-2">
                  <Label>Image {windowsOnlySteam && <span className="text-destructive">(Wine required)</span>}</Label>
                  <Input value={form.docker_image} onChange={(e) => set("docker_image", e.target.value)}
                    className="font-mono text-xs"
                    placeholder={windowsOnlySteam ? "a Wine-capable image, e.g. your-registry/steam-wine:latest" : "e.g. eclipse-temurin:21-jre"} />
                </div>
                <div className="space-y-1.5">
                  <Label>Ports (host:container/proto)</Label>
                  <Input value={form.docker_ports} onChange={(e) => set("docker_ports", e.target.value)}
                    className="font-mono text-xs" placeholder="auto (game port)" />
                </div>
                <div className="space-y-1.5">
                  <Label>Environment (KEY=value per line)</Label>
                  <Textarea value={form.docker_env} onChange={(e) => set("docker_env", e.target.value)}
                    className="font-mono text-xs" placeholder={"MEMORY=4G"} />
                </div>
              </div>
            )}
          </SectionCard>

          <SectionCard icon={<Monitor className="h-4 w-4" />} title="Behaviour">
            <div className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <div className="text-sm font-medium">Auto start</div>
                <div className="text-xs text-muted-foreground">Start automatically when the panel boots</div>
              </div>
              <Switch checked={form.auto_start} onCheckedChange={(v) => set("auto_start", v)} />
            </div>
            <div className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <div className="text-sm font-medium">Crash detection</div>
                <div className="text-xs text-muted-foreground">Auto-restart up to 3 times after crashes</div>
              </div>
              <Switch checked={form.crash_detection} onCheckedChange={(v) => set("crash_detection", v)} />
            </div>
          </SectionCard>
        </div>
      )}

      {/* SteamCMD live output */}
      {(createType === "steam_game" && (steamInstalling || steamOutput.length > 0)) && (
        <Card className="mt-4">
          <CardHeader className="pb-3">
            <div className="flex items-center justify-between">
              <CardTitle className="text-base">SteamCMD installation</CardTitle>
              {steamInstalling && (
                <span className="flex items-center gap-1.5 text-sm text-primary">
                  <Loader2 className="h-4 w-4 animate-spin" /> Installing…
                </span>
              )}
            </div>
            <p className="text-xs text-muted-foreground">Live output while the game files are downloaded — this can take several minutes.</p>
          </CardHeader>
          <CardContent>
            <div ref={outputRef}
              className="max-h-80 overflow-y-auto rounded-lg border bg-black/60 p-3 font-mono text-xs leading-relaxed text-green-300"
              style={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace" }}>
              {steamOutput.length === 0 ? (
                <div className="text-muted-foreground">Waiting for SteamCMD output…</div>
              ) : (
                steamOutput.map((line, i) => (
                  <div key={i} className="whitespace-pre-wrap break-all">{line}</div>
                ))
              )}
            </div>
            {steamOutput.length > 0 && (
              <div className="mt-2 flex justify-end">
                <Button variant="ghost" size="sm" onClick={() => setSteamOutput([])}>
                  <X className="h-3.5 w-3.5" /> Clear
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {step === 3 && (
        <div className="space-y-4">
          <SummaryGroup title="Placement" onEdit={() => setStep(2)}>
            <SummaryRow label="runs on" value={targetNode ? targetNode.name : "this panel"} mono />
            {targetNode && <SummaryRow label="node address" value={targetNode.address} mono />}
          </SummaryGroup>

          <SummaryGroup title="Basics" onEdit={() => setStep(2)}>
            <SummaryRow label="How it is built" value={typeLabel} />
            <SummaryRow label="Name" value={form.name} />
            <SummaryRow label="Type" value={form.type} />
            <SummaryRow label="Port" value={form.port} mono />
            {createType === "import_zip" && <SummaryRow label="Archive" value={form.archive_path} mono />}
            {createType === "download_jar" && <SummaryRow label="Jar" value={form.jar_name || form.jar_url} mono />}
            {createType === "download_jar" && <SummaryRow label="Memory" value={`${form.min_mem} – ${form.max_mem}`} mono />}
            {createType === "raw_exec" && <SummaryRow label="Executable" value={form.executable} mono />}
            {createType === "raw_exec" && <SummaryRow label="Command" value={form.execution_command} mono />}
            {createType === "raw_exec" && <SummaryRow label="Stop command" value={form.stop_command} mono />}
          </SummaryGroup>

          {pickedSteam && (steamOptionRows.length > 0 || automaticOptionRows.length > 0) && (
            <SummaryGroup title={`Launch parameters · ${pickedSteam.name}`} onEdit={() => setStep(2)}>
              {automaticOptionRows.map((option) => (
                <SummaryRow key={option.key} label={`${option.label} (automatic)`}
                  value={option.source === "server_name" ? form.name
                    : option.source === "port" ? form.port
                      : option.source === "query_port" ? (pickedSteam.query_port || Number(form.port) + 1)
                        : "server save directory"} mono />
              ))}
              {steamOptionRows.map((option) => {
                const value = (form.steam_options || {})[option.key] ?? option.default ?? "";
                return (
                  <SummaryRow key={option.key} label={option.label}
                    value={option.type === "password" ? "" : value} masked={option.type === "password"} mono />
                );
              })}
            </SummaryGroup>
          )}

          <SummaryGroup title="Execution environment" onEdit={() => setStep(2)}>
            <SummaryRow label="Backend" value={isDocker ? "Docker container" : "Host process"} />
            {isDocker && <SummaryRow label="Image" value={form.docker_image} mono />}
            {isDocker && form.docker_ports && <SummaryRow label="Ports" value={form.docker_ports} mono />}
            {!isDocker && createType !== "steam_game" && form.shell && <SummaryRow label="Terminal" value={form.shell} />}
            {!isDocker && form.java_selection && <SummaryRow label="Java" value={form.java_selection} mono />}
          </SummaryGroup>

          <SummaryGroup title="Behaviour" onEdit={() => setStep(2)}>
            <SummaryRow label="Auto start" value={form.auto_start ? "yes" : "no"} />
            <SummaryRow label="Crash detection" value={form.crash_detection ? "yes" : "no"} />
          </SummaryGroup>
        </div>
      )}

      {/* sticky footer: always reachable, shows why you cannot continue.
          Sticky (not fixed) so it stays inside the main column and never
          covers the sidebar. */}
      <div className="sticky bottom-0 z-10 -mx-6 mt-6 border-t bg-background/95 px-6 backdrop-blur">
        <div className="flex items-center gap-4 py-3">
          <div className="min-w-0 flex-1">
            {step === 2 && step2Error && (
              <p role="alert" className="truncate text-sm text-warning">{step2Error}</p>
            )}
            {step === 3 && (
              <p className="truncate text-sm text-muted-foreground">
                {createType === "steam_game" && "SteamCMD will download the game files (may take several minutes)."}
                {createType === "download_jar" && "The jar is downloaded into the server directory."}
                {createType === "import_zip" && "The archive is extracted into the server directory."}
                {createType === "raw_exec" && "The folder must already contain your executable."}
              </p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="ghost" onClick={() => setStep(Math.max(1, step - 1))} disabled={step === 1}>
              <ArrowLeft /> Back
            </Button>
            {step < 3 ? (
              <Button onClick={() => setStep(step + 1)} disabled={uploadPct !== null || busy || (step === 2 && !!step2Error)}>
                Continue <ArrowRight />
              </Button>
            ) : (
              <Button onClick={submit} disabled={busy || uploadPct !== null || !!step2Error}>
                {busy && <Loader2 className="animate-spin" />} {busy && createType === "steam_game" ? "Installing…" : busy ? "Creating…" : "Create Server"}
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
