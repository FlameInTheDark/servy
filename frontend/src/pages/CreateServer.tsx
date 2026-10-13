import { useState, useEffect, useRef } from "react";
import { useNavigate, Link } from "react-router-dom";
import { toast } from "sonner";
import {
  Upload, Download, Terminal, Gamepad2, Boxes, ArrowLeft, ArrowRight, Loader2, FileArchive, Container, Cpu, AlertTriangle, Monitor, X,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { api, GET, SteamGame, JavaInstall, realtime } from "@/api/client";

interface ShellInfo { id: string; label: string; path?: string; available: boolean; platform: string }
import { Card, CardContent, CardHeader, CardTitle, CardDescription, Button, Input, Label, Badge, Skeleton, Switch, Textarea } from "@/components/ui/primitives";
import { SimpleSelect, Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/overlays";
import { cn } from "@/lib/utils";

const CREATE_TYPES = [
  { id: "import_zip", label: "Import Zip", desc: "Upload or point at a server .zip archive", icon: Upload },
  { id: "download_jar", label: "Download Jar", desc: "Minecraft jar from BigBucket or URL", icon: Download },
  { id: "steam_game", label: "SteamCMD Game", desc: "Any Steam game server (Valheim, CS2, …)", icon: Gamepad2 },
  { id: "raw_exec", label: "Custom / Raw", desc: "Existing directory + execution command", icon: Terminal },
];

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
  const { data: steamGames, loading: steamLoading } = useApi<SteamGame[]>("/panel/SteamCatalog");
  const { data: jarCache } = useApi<any>("/panel/JarCache");
  const { data: javaData } = useApi<{ installs: JavaInstall[] }>("/panel/java");
  const { data: dockerData } = useApi<{ available: boolean }>("/panel/docker");
  const { data: shellsData } = useApi<{ default: string; platform: string; shells: ShellInfo[] }>("/panel/shells");
  const shells = shellsData?.shells || [];
  const javaInstalls = javaData?.installs || [];
  const pickedSteamGame = form.steam_slug ? steamGames?.find((g) => g.slug === form.steam_slug) : undefined;
  const windowsOnlySteam = !!pickedSteamGame?.windows_only;
  const isDocker = form.execution_backend === "docker" || windowsOnlySteam;

  // SteamCMD live output
  const [steamOutput, setSteamOutput] = useState<string[]>([]);
  const [steamInstalling, setSteamInstalling] = useState(false);
  const [createdServerId, setCreatedServerId] = useState<string | null>(null);
  const rtRef = useRef<ReturnType<typeof realtime> | null>(null);

  const set = (k: string, v: any) => setForm((f: any) => ({ ...f, [k]: v }));

  const submit = async () => {
    setBusy(true);
    let unsubscribe: (() => void) | null = null;
    
    // For Steam games, set up live output subscription BEFORE the API call
    if (createType === "steam_game") {
      setSteamInstalling(true);
      setSteamOutput([]);
      
      const rt = realtime();
      rtRef.current = rt;
      
      // Wait for WebSocket connection to be ready
      await rt.waitForConnection();
      
      // Subscribe to all server events (wildcard) to catch steamcmd_output
      // The backend publishes to both "server.<id>" and "*" topics
      unsubscribe = rt.on((msg) => {
        if (msg.event === "steamcmd_output" && msg.data?.line) {
          setSteamOutput((prev) => [...prev.slice(-499), msg.data.line]);
        } else if (msg.event === "send_start_reload" && msg.data?.server_id) {
          // Server creation complete - use server_id from event directly
          const serverId = msg.data.server_id;
          setCreatedServerId(serverId);
          setSteamInstalling(false);
          setTimeout(() => nav(`/servers/${serverId}`), 500);
        }
      });
      rt.subscribe("*"); // listen to all server events
    }
    
    try {
      const payload: any = { ...form };
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
      setCreatedServerId(res.server_id);
      toast.success(`Server "${res.name}" created`);
      
      // For Steam games, if we already have the server_id from response, 
      // also try to navigate (fallback if event didn't fire)
      if (createType === "steam_game") {
        // Check if we already got the event, otherwise wait a bit
        setTimeout(() => {
          if (createdServerId === res.server_id) {
            nav(`/servers/${res.server_id}`);
          }
        }, 2000);
      } else {
        nav(`/servers/${res.server_id}`);
      }
      
      // If Steam game, show live output from the response
      if (createType === "steam_game" && res.steam_output) {
        setSteamOutput(res.steam_output);
      }
      
      // Cleanup after navigation
      if (unsubscribe) {
        setTimeout(() => unsubscribe(), 10000);
      }
    } catch (e: any) {
      toast.error(e.message);
      // Keep steam output visible on failure so user can diagnose
      // Also add the error to the output panel
      if (e.message && steamOutput.length > 0) {
        setSteamOutput((prev) => [...prev, `ERROR: ${e.message}`]);
      }
      setSteamInstalling(false);
    } finally {
      if (createType !== "steam_game") {
        setBusy(false);
      }
    }
  };

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (rtRef.current) {
        rtRef.current.close();
        rtRef.current = null;
      }
    };
  }, []);

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
  const jarEntries: { name: string; version: string; url: string }[] =
    jarCache?.categories?.flatMap((c: any) => c.servers || []) || [];

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <div className="mb-6">
        <Link to="/servers" className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground">
          <ArrowLeft className="h-3.5 w-3.5" /> Back to servers
        </Link>
      </div>
      <h1 className="text-2xl font-bold mb-1">Create Server</h1>
      <p className="text-sm text-muted-foreground mb-6">Step {step} of 3</p>

      {/* progress */}
      <div className="mb-6 flex gap-2">
        {[1, 2, 3].map((s) => (
          <div key={s} className={cn("h-1.5 flex-1 rounded-full", s <= step ? "bg-primary" : "bg-muted")} />
        ))}
      </div>

      {step === 1 && (
        <div className="grid gap-4 sm:grid-cols-2">
          {CREATE_TYPES.map((t) => (
            <button key={t.id} onClick={() => { setCreateType(t.id); set("create_type", t.id); setStep(2); }}
              className={cn(
                "rounded-xl border bg-card p-5 text-left transition-all hover:border-primary/60 hover:shadow-lg",
                createType === t.id && "border-primary ring-1 ring-primary"
              )}>
              <t.icon className="mb-3 h-7 w-7 text-primary" />
              <div className="font-semibold">{t.label}</div>
              <div className="mt-1 text-sm text-muted-foreground">{t.desc}</div>
            </button>
          ))}
        </div>
      )}

      {step === 2 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-lg">{CREATE_TYPES.find((t) => t.id === createType)?.label} — details</CardTitle>
            <CardDescription>Basic settings. You can change everything later.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>Server name</Label>
                <Input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="My Awesome Server" />
              </div>
              <div className="space-y-1.5">
                <Label>Port</Label>
                <Input type="number" value={form.port} onChange={(e) => set("port", +e.target.value)} />
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
                    <Label>Min memory</Label>
                    <Input value={form.min_mem} onChange={(e) => set("min_mem", e.target.value)} placeholder="512M" />
                  </div>
                  <div className="space-y-1.5">
                    <Label>Max memory</Label>
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
                  <div className="grid max-h-96 gap-2 overflow-y-auto pr-1 sm:grid-cols-2">
                    {(steamGames || []).map((g) => (
                      <button key={g.slug}
                        onClick={() => {
                          set("steam_slug", g.slug);
                          set("name", form.name || g.name);
                          set("port", g.port);
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
                        <div className="flex items-center justify-between">
                          <span className="font-medium">{g.name}</span>
                          <span className="flex gap-1">
                            {g.windows_only && <Badge variant="destructive" className="text-[10px]">windows</Badge>}
                            <Badge variant="secondary">#{g.app_id}</Badge>
                          </span>
                        </div>
                        <div className="mt-1 text-xs text-muted-foreground">port {g.port}</div>
                      </button>
                    ))}
                  </div>
                )}
                {pickedSteam && (
                  <div className="rounded-lg border bg-muted/40 p-3 text-xs">
                    <div className="font-mono">{pickedSteam.run_command}</div>
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
                  <Label>Execution command</Label>
                  <Textarea value={form.execution_command || ""} onChange={(e) => set("execution_command", e.target.value)}
                    placeholder="java -Xms512M -Xmx1024M -jar server.jar nogui" className="font-mono text-xs" />
                </div>
                <div className="space-y-1.5">
                  <Label>Stop command</Label>
                  <Input value={form.stop_command || ""} onChange={(e) => set("stop_command", e.target.value)} placeholder="stop" />
                </div>
              </div>
            )}

            <div className="space-y-3 rounded-lg border p-4">
              <div className="flex items-center gap-2 text-sm font-medium">
                {isDocker ? <Container className="h-4 w-4 text-primary" /> : <Cpu className="h-4 w-4 text-primary" />}
                Execution environment
              </div>
              {windowsOnlySteam && (
                <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs">
                  <AlertTriangle className="mt-0.5 h-4 w-4 text-destructive" />
                  <div>
                    <b>{pickedSteam?.name}</b> has no Linux build — it runs in its own Docker container
                    with a Wine image{pickedSteam?.docker_image ? <> (<span className="font-mono">{pickedSteam.docker_image}</span>)</> : " (set one below)"}.
                    SteamCMD fetches the Windows depot automatically, and the container publishes the game
                    port itself — no extra port mapping needed on Servy.
                  </div>
                </div>
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label>Terminal</Label>
                  <SimpleSelect
                    value={form.shell || "auto"}
                    onValueChange={(v) => set("shell", v === "auto" ? "" : v)}
                    options={shells.map((sh) => ({
                      value: sh.id,
                      label: sh.available ? sh.label : `${sh.label} — unavailable`,
                    }))}
                  />
                  <p className="text-xs text-muted-foreground">
                    How the execution command is launched. {shellsData?.platform === "windows"
                      ? "Windows default: cmd (batch files)."
                      : "Linux default: bash."} Pick PowerShell / Git Bash when the start script needs it.
                  </p>
                </div>
                <div className="space-y-1.5">
                  <Label>Backend</Label>
                  <SimpleSelect
                    value={windowsOnlySteam ? "docker" : form.execution_backend}
                    onValueChange={(v) => set("execution_backend", v)}
                    options={[
                      { value: "process", label: "Host process (default)" },
                      { value: "docker", label: "Docker container" },
                    ]}
                  />
                  <p className="text-xs text-muted-foreground">
                    {dockerData?.available
                      ? "Docker daemon reachable."
                      : "Docker daemon not reachable on this host — containers disabled."}
                  </p>
                </div>
                {!isDocker && (form.type === "minecraft-java") && (
                  <div className="space-y-1.5">
                    <Label>Java version</Label>
                    <SimpleSelect
                      value={form.java_selection || ""}
                      onValueChange={(v) => set("java_selection", v)}
                      options={[
                        { value: "", label: "Use command as written" },
                        { value: "java", label: "System default (java)" },
                        ...javaInstalls.map((j) => ({ value: j.path, label: `Java ${j.major} — ${j.vendor} (${j.version})` })),
                      ]}
                    />
                    <p className="text-xs text-muted-foreground">
                      ≤1.16 → Java 8 · 1.17 → 16+ · 1.18–1.20.4 → 17 · 1.20.5+ → 21
                    </p>
                  </div>
                )}
              </div>
              {isDocker && (
                <div className="grid gap-4 sm:grid-cols-2">
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
            </div>

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
          </CardContent>
        </Card>
      )}

      {/* SteamCMD live output panel */}
      {(createType === "steam_game" && (steamInstalling || steamOutput.length > 0)) && (
        <Card className="mt-4">
          <CardHeader>
            <div className="flex items-center justify-between">
              <CardTitle className="text-lg">SteamCMD Installation</CardTitle>
              {steamInstalling && <span className="flex items-center gap-1.5 text-sm text-primary"><Loader2 className="h-4 w-4 animate-spin" /> Installing…</span>}
            </div>
            <CardDescription>Live output from SteamCMD. This may take several minutes.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="rounded bg-black/50 p-4 font-mono text-xs text-green-300 max-h-96 overflow-y-auto border" style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }}>
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
        <Card>
          <CardHeader>
            <CardTitle className="text-lg">Review & create</CardTitle>
            <CardDescription>
              {createType === "steam_game" && "SteamCMD will download the game files (may take several minutes)."}
              {createType === "download_jar" && "The jar will be downloaded into the server directory."}
              {createType === "import_zip" && "The archive will be extracted into the server directory."}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            {Object.entries(form).filter(([k, v]) => v !== "" && v !== undefined && v !== null && v !== false)
              .map(([k, v]) => (
                <div key={k} className="flex justify-between gap-4 border-b py-1.5">
                  <span className="text-muted-foreground">{k.replace(/_/g, " ")}</span>
                  <span className="font-mono text-xs truncate max-w-[60%]">{String(v)}</span>
                </div>
              ))}
          </CardContent>
        </Card>
      )}

      <div className="mt-6 flex justify-between">
        <Button variant="ghost" onClick={() => setStep(Math.max(1, step - 1))} disabled={step === 1}>
          <ArrowLeft /> Back
        </Button>
        {step < 3 ? (
          <Button onClick={() => setStep(step + 1)} disabled={uploadPct !== null || (step === 1 ? false : !form.name)}>
            Continue <ArrowRight />
          </Button>
        ) : (
          <Button onClick={submit} disabled={busy || uploadPct !== null || !form.name}>
            {busy && <Loader2 className="animate-spin" />} Create Server
          </Button>
        )}
      </div>
    </div>
  );
}
