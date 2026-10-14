import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Save, Database, Cpu, Eye, EyeOff, RotateCcw, SlidersHorizontal, CircleHelp, MemoryStick, Network } from "lucide-react";
import { PATCH, GET, ServerDef } from "@/api/client";
import { Button, Card, CardContent, CardHeader, CardTitle, Input, Label, Switch, Badge } from "@/components/ui/primitives";
import { SimpleSelect, Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/overlays";
import { useApi } from "@/hooks/useApi";
import { cn } from "@/lib/utils";

interface ShellOption { id: string; label: string; path?: string; available: boolean; platform: string }

/* ------------------------- startup / launch parameters --------------------- */

interface LaunchOption {
  key: string;
  label: string;
  description?: string;
  type: "text" | "password" | "number" | "boolean" | "select";
  flag?: string;
  value: string;
  default?: string;
  required?: boolean;
  min?: number;
  max?: number;
  min_length?: number;
  max_length?: number;
  options?: { value: string; label: string }[];
  automatic?: string;   // server_name | port | query_port | save_dir
  using_default?: boolean;
}

interface LaunchOptionsPayload {
  server_id: string;
  editable: boolean;
  kind?: string;
  argv?: string[];
  command?: string;
  remote?: boolean;
  node_id?: string;
  node_name?: string;
  node_online?: boolean;
  steam?: {
    game: { name: string; app_id?: number; platform?: string; slug?: string; stop_command?: string };
    options: LaunchOption[];
    argv: string[];
  };
  memory?: { min: string; max: string };
}

function HelpHint({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" aria-label="More information"
          className="text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none">
          <CircleHelp className="h-3.5 w-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[280px] leading-relaxed">{text}</TooltipContent>
    </Tooltip>
  );
}

function shellQuote(argv: string[]): string {
  return argv
    .map((arg) => (/^[A-Za-z0-9_./:=+-]+$/.test(arg) ? arg : `"${arg.replace(/"/g, '\\"')}"`))
    .join(" ");
}

/** Password field with a reveal toggle (values are stored per server). */
function SecretInput({ id, value, onChange, placeholder }: {
  id?: string; value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  const [visible, setVisible] = useState(false);
  return (
    <div className="relative">
      <Input
        id={id}
        type={visible ? "text" : "password"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="new-password"
        className="pr-9 font-mono text-xs"
      />
      <button
        type="button"
        onClick={() => setVisible((v) => !v)}
        aria-label={visible ? "Hide value" : "Show value"}
        className="absolute right-2 top-2 text-muted-foreground transition-colors hover:text-foreground"
      >
        {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
      </button>
    </div>
  );
}

interface JavaInstall {
  path: string;
  home: string;
  version: string;
  major: number;
  vendor: string;
  source: string;
}

export default function Settings({ server, onUpdate }: { server: ServerDef; onUpdate: () => void }) {
  const [form, setForm] = useState({
    server_name: server.server_name,
    execution_command: server.execution_command || "",
    executable: server.executable || "",
    stop_command: server.stop_command || "",
    server_ip: server.server_ip || "127.0.0.1",
    server_port: server.server_port,
    auto_start: server.auto_start,
    auto_start_delay: server.auto_start_delay,
    crash_detection: server.crash_detection,
    shutdown_timeout: server.shutdown_timeout,
    io_mode: server.io_mode || "pipes",
    type: server.type,
    show_status: server.show_status,
    executable_update_url: (server as any).executable_update_url || "",
    // execution environment
    java_selection: server.java_selection || "",
    shell: server.shell || "",
    execution_backend: server.execution_backend || "process",
    docker_image: server.docker_image || "",
    docker_ports: (server.docker_ports || []).join(", "),
    docker_env: Object.entries(server.docker_env || {}).map(([k, v]) => `${k}=${v}`).join("\n"),
    docker_binds: (server.docker_binds || []).join(", "),
    docker_network: server.docker_network || "",
    docker_workdir: server.docker_workdir || "",
    docker_autoremove: server.docker_autoremove || false,
  });
  const [busy, setBusy] = useState(false);
  // parameters collected during creation (Steam launch options, -Xms/-Xmx)
  const { data: launch, refetch: refetchLaunch } = useApi<LaunchOptionsPayload>(`/servers/${server.server_id}/launch-options`);
  const [launchBusy, setLaunchBusy] = useState(false);
  const [launchError, setLaunchError] = useState("");
  const [steamValues, setSteamValues] = useState<Record<string, string>>({});
  const [memoryValues, setMemoryValues] = useState({ min: "", max: "" });

  useEffect(() => {
    if (!launch) return;
    const values: Record<string, string> = {};
    for (const option of launch.steam?.options || []) {
      if (option.automatic) continue;
      values[option.key] = option.value ?? "";
    }
    setSteamValues(values);
    setMemoryValues({ min: launch.memory?.min || "", max: launch.memory?.max || "" });
  }, [launch]);

  const launchDirty = (() => {
    if (!launch) return false;
    for (const option of launch.steam?.options || []) {
      if (option.automatic) continue;
      if ((steamValues[option.key] ?? "") !== (option.value ?? "")) return true;
    }
    if (launch.memory && (memoryValues.min !== launch.memory.min || memoryValues.max !== launch.memory.max)) return true;
    return false;
  })();

  const saveLaunch = async () => {
    if (!launch) return;
    setLaunchBusy(true);
    setLaunchError("");
    try {
      const payload: Record<string, unknown> = {};
      if (launch.steam) payload.steam_options = steamValues;
      if (launch.memory) {
        payload.min_mem = memoryValues.min;
        payload.max_mem = memoryValues.max;
      }
      await PATCH(`/servers/${server.server_id}`, payload);
      toast.success("Startup parameters saved");
      await refetchLaunch();
      onUpdate();
    } catch (e: any) {
      setLaunchError(e.message || "Could not save the parameters");
      toast.error(e.message);
    } finally {
      setLaunchBusy(false);
    }
  };

  const resetOption = (option: LaunchOption) => setSteamValues((v) => ({ ...v, [option.key]: "" }));

  const { data: javaData } = useApi<{ installs: JavaInstall[]; minecraft_java_requirements: { range: string; major: number }[] }>("/panel/java");
  const { data: dockerData } = useApi<{ available: boolean; host?: string; version?: string }>("/panel/docker");
  const { data: shellsData } = useApi<{ default: string; platform: string; shells: ShellOption[] }>("/panel/shells");
  const shellOptions = shellsData?.shells || [];
  const installs = javaData?.installs || [];

  const set = (k: string, v: any) => setForm((f) => ({ ...f, [k]: v }));
  const isDocker = form.execution_backend === "docker";
  const isManifestSteam = server.type === "steam_cmd" && server.steam_manifest === true;

  const save = async () => {
    setBusy(true);
    try {
      await PATCH(`/servers/${server.server_id}`, {
        server_name: form.server_name,
        execution_command: form.execution_command,
        executable: form.executable,
        stop_command: form.stop_command,
        server_ip: form.server_ip,
        server_port: form.server_port,
        auto_start: form.auto_start,
        auto_start_delay: form.auto_start_delay,
        crash_detection: form.crash_detection,
        shutdown_timeout: form.shutdown_timeout,
        io_mode: form.io_mode,
        type: form.type,
        show_status: form.show_status,
        executable_update_url: form.executable_update_url,
        java_selection: form.java_selection,
        shell: isManifestSteam ? "none" : form.shell,
        execution_backend: form.execution_backend,
        docker_image: isDocker ? form.docker_image : "",
        docker_ports: isDocker ? form.docker_ports.split(",").map((s) => s.trim()).filter(Boolean) : [],
        docker_env: isDocker
          ? Object.fromEntries(
              form.docker_env
                .split("\n")
                .map((l) => l.trim())
                .filter((l) => l.includes("="))
                .map((l) => {
                  const i = l.indexOf("=");
                  return [l.slice(0, i), l.slice(i + 1)];
                }),
            )
          : {},
        docker_binds: isDocker ? form.docker_binds.split(",").map((s) => s.trim()).filter(Boolean) : [],
        docker_network: isDocker ? form.docker_network : "",
        docker_workdir: isDocker ? form.docker_workdir : "",
        docker_autoremove: isDocker && form.docker_autoremove,
      });
      toast.success("Server settings saved");
      onUpdate();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4 max-w-3xl">
      <Card>
        <CardHeader><CardTitle className="text-base">General</CardTitle></CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>Name</Label>
            <Input value={form.server_name} onChange={(e) => set("server_name", e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label>Type</Label>
            <SimpleSelect value={form.type} onValueChange={(v) => set("type", v)} options={[
              { value: "minecraft-java", label: "Minecraft (Java)" },
              { value: "minecraft-bedrock", label: "Minecraft (Bedrock)" },
              { value: "steam_cmd", label: "SteamCMD" },
              { value: "custom", label: "Custom" },
            ]} />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label>Execution command</Label>
            <Input value={form.execution_command} onChange={(e) => set("execution_command", e.target.value)} className="font-mono text-xs" />
            {form.java_selection && form.java_selection !== "none" && (
              <p className="text-xs text-muted-foreground">
                Java selection overrides the first token of this command at start.
              </p>
            )}
            {isManifestSteam && (
              <p className="text-xs text-muted-foreground">
                This catalog-backed command runs directly without a shell. Manifest placeholders and saved launch parameters are added at start.
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label>Executable</Label>
            <Input value={form.executable} onChange={(e) => set("executable", e.target.value)} className="font-mono text-xs" />
          </div>
          <div className="space-y-1.5">
            <Label>Stop command</Label>
            <Input value={form.stop_command} onChange={(e) => set("stop_command", e.target.value)} className="font-mono text-xs" />
          </div>
          <div className="space-y-1.5">
            <Label>IP</Label>
            <Input value={form.server_ip} onChange={(e) => set("server_ip", e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label>Port</Label>
            <Input type="number" value={form.server_port} onChange={(e) => set("server_port", +e.target.value)} />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label>Executable update URL (auto-update source)</Label>
            <Input value={form.executable_update_url} onChange={(e) => set("executable_update_url", e.target.value)} className="font-mono text-xs" placeholder="https://…" />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2"><Cpu className="h-4 w-4" /> Execution environment</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            {!isDocker && !isManifestSteam && (
              <div className="space-y-1.5">
                <Label>Terminal</Label>
                <SimpleSelect
                  value={form.shell || "auto"}
                  onValueChange={(v) => set("shell", v === "auto" ? "" : v)}
                  options={shellOptions.map((sh) => ({
                    value: sh.id,
                    label: sh.available ? sh.label : `${sh.label} — unavailable on this host`,
                  }))}
                />
                <p className="text-xs text-muted-foreground">
                  {shellsData?.platform === "windows"
                    ? "Windows default: cmd.exe — required for .bat/.cmd start scripts."
                    : "Linux default: bash."}{" "}
                  Pick PowerShell or Git Bash if your start script needs them.
                </p>
              </div>
            )}
            <div className="space-y-1.5">
              <Label>Backend</Label>
              <SimpleSelect value={form.execution_backend} onValueChange={(v) => set("execution_backend", v)} options={[
                { value: "process", label: "Host process (default)" },
                { value: "docker", label: "Docker container" },
              ]} />
              <p className="text-xs text-muted-foreground">
                {dockerData?.available
                  ? `Docker ready — ${dockerData.version || ""}`
                  : "Docker daemon not reachable: containers unavailable on this host."}
              </p>
            </div>
            {!isDocker && (
              <div className="space-y-1.5">
                <Label>Java selection (Minecraft)</Label>
                <SimpleSelect
                  value={form.java_selection || ""}
                  onValueChange={(v) => set("java_selection", v)}
                  options={[
                    { value: "", label: "Use command as written" },
                    { value: "java", label: "System default (java)" },
                    ...installs.map((j) => ({
                      value: j.path,
                      label: `Java ${j.major} — ${j.vendor} (${j.version})`,
                    })),
                  ]}
                />
                <p className="text-xs text-muted-foreground">
                  Minecraft ≤1.16 needs Java 8 · 1.17 needs 16+ · 1.18–1.20.4 needs 17 · 1.20.5+ needs 21
                </p>
              </div>
            )}
          </div>

          {isDocker && (
            <div className="grid gap-4 sm:grid-cols-2 border-t pt-4">
              <div className="space-y-1.5 sm:col-span-2">
                <Label>Image</Label>
                <Input value={form.docker_image} onChange={(e) => set("docker_image", e.target.value)}
                  className="font-mono text-xs" placeholder="e.g. eclipse-temurin:21-jre, steamcmd/steamcmd, ghcr.io/…/wine" />
                <p className="text-xs text-muted-foreground">
                  The server directory is bind-mounted to the working dir; game port is published automatically.
                  Windows-only games need a Wine-capable image.
                </p>
              </div>
              <div className="space-y-1.5">
                <Label>Ports (host:container/proto, comma separated)</Label>
                <Input value={form.docker_ports} onChange={(e) => set("docker_ports", e.target.value)}
                  className="font-mono text-xs" placeholder="25565:25565/tcp, 19132:19132/udp" />
              </div>
              <div className="space-y-1.5">
                <Label>Extra binds (host:container[:ro])</Label>
                <Input value={form.docker_binds} onChange={(e) => set("docker_binds", e.target.value)}
                  className="font-mono text-xs" placeholder="/mnt/mods:/data/mods:ro" />
              </div>
              <div className="space-y-1.5">
                <Label>Environment (KEY=value, one per line)</Label>
                <textarea value={form.docker_env} onChange={(e) => set("docker_env", e.target.value)}
                  className="flex min-h-20 w-full rounded-md border border-input bg-background px-3 py-2 text-xs font-mono"
                  placeholder={"MEMORY=4G\nTZ=Etc/UTC"} />
              </div>
              <div className="space-y-1.5">
                <Label>Network</Label>
                <Input value={form.docker_network} onChange={(e) => set("docker_network", e.target.value)}
                  className="font-mono text-xs" placeholder="bridge (default)" />
              </div>
              <div className="space-y-1.5">
                <Label>Working dir inside container</Label>
                <Input value={form.docker_workdir} onChange={(e) => set("docker_workdir", e.target.value)}
                  className="font-mono text-xs" placeholder="/data (default)" />
              </div>
              <div className="flex items-center justify-between rounded-md border p-3">
                <div>
                  <div className="text-sm font-medium">Auto-remove container</div>
                  <div className="text-xs text-muted-foreground">delete the container after exit</div>
                </div>
                <Switch checked={form.docker_autoremove} onCheckedChange={(v) => set("docker_autoremove", v)} />
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-base">Runtime</CardTitle></CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="flex items-center justify-between rounded-md border p-3">
            <div>
              <div className="text-sm font-medium">Auto start</div>
              <div className="text-xs text-muted-foreground">boot with panel (after delay)</div>
            </div>
            <Switch checked={form.auto_start} onCheckedChange={(v) => set("auto_start", v)} />
          </div>
          <div className="space-y-1.5">
            <Label>Auto start delay (s)</Label>
            <Input type="number" value={form.auto_start_delay} onChange={(e) => set("auto_start_delay", +e.target.value)} />
          </div>
          <div className="flex items-center justify-between rounded-md border p-3">
            <div>
              <div className="text-sm font-medium">Crash detection</div>
              <div className="text-xs text-muted-foreground">auto-restart up to 3× on crash</div>
            </div>
            <Switch checked={form.crash_detection} onCheckedChange={(v) => set("crash_detection", v)} />
          </div>
          <div className="space-y-1.5">
            <Label>Shutdown timeout (s)</Label>
            <Input type="number" value={form.shutdown_timeout} onChange={(e) => set("shutdown_timeout", +e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label>I/O mode</Label>
            <SimpleSelect value={form.io_mode} onValueChange={(v) => set("io_mode", v)} options={[
              { value: "pipes", label: "Pipes (default)" },
              { value: "pty", label: "PTY (interactive consoles)" },
            ]} />
          </div>
          <div className="flex items-center justify-between rounded-md border p-3">
            <div>
              <div className="text-sm font-medium">Show in public status</div>
              <div className="text-xs text-muted-foreground">/status endpoint + server list</div>
            </div>
            <Switch checked={form.show_status} onCheckedChange={(v) => set("show_status", v)} />
          </div>
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button onClick={save} disabled={busy}><Save /> Save settings</Button>
      </div>

      {/* values picked when the server was created — editable afterwards */}
      {launch && (launch.steam || launch.memory) && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <SlidersHorizontal className="h-4 w-4" /> Startup parameters
            </CardTitle>
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span>
                {launch.steam
                  ? "Launch options for this game, as filled in during creation."
                  : "Java memory flags of the execution command."}
              </span>
              {launch.remote && (
                <span className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5">
                  <Network className="h-3 w-3" /> applied on {launch.node_name || launch.node_id}
                  {launch.node_online === false && <span className="text-destructive"> · offline</span>}
                </span>
              )}
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            {launch.steam && (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{launch.steam.game.name}</span>
                  {launch.steam.game.app_id ? <Badge variant="secondary">#{launch.steam.game.app_id}</Badge> : null}
                  {launch.steam.game.platform && <Badge variant="muted">{launch.steam.game.platform}</Badge>}
                </div>

                {(launch.steam.options || []).some((o) => o.automatic) && (
                  <div className="space-y-1.5">
                    <div className="text-sm font-medium">Filled in automatically</div>
                    {(launch.steam.options || []).filter((o) => o.automatic).map((option) => (
                      <div key={option.key} className="flex items-center justify-between gap-3 rounded-md border bg-muted/30 px-3 py-2 text-sm">
                        <span className="flex items-center gap-1.5">
                          {option.label}
                          <Badge variant="muted" className="text-[10px]">{(option.automatic || "").replace(/_/g, " ")}</Badge>
                        </span>
                        <span className="truncate font-mono text-xs text-muted-foreground">{option.value}</span>
                      </div>
                    ))}
                  </div>
                )}

                <div className="grid gap-4 sm:grid-cols-2">
                  {(launch.steam.options || []).filter((o) => !o.automatic).map((option) => {
                    const value = steamValues[option.key] ?? "";
                    const canReset = !option.required && !option.using_default;
                    return (
                      <div key={option.key} className="space-y-1.5">
                        <div className="flex items-center gap-1.5">
                          <Label htmlFor={`launch-${option.key}`}>{option.label}</Label>
                          {option.required && <span className="text-destructive">*</span>}
                          {option.description && <HelpHint text={option.description} />}
                          {option.using_default && value && <Badge variant="muted" className="text-[10px]">default</Badge>}
                          {canReset && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <button
                                  type="button"
                                  aria-label={`Reset ${option.label}`}
                                  onClick={() => resetOption(option)}
                                  className="text-muted-foreground/70 transition-colors hover:text-foreground"
                                >
                                  <RotateCcw className="h-3.5 w-3.5" />
                                </button>
                              </TooltipTrigger>
                              <TooltipContent>Clear the value and fall back to the game default ({option.default || "none"}).</TooltipContent>
                            </Tooltip>
                          )}
                        </div>
                        {option.type === "boolean" ? (
                          <div className="flex min-h-9 items-center gap-2">
                            <Switch
                              id={`launch-${option.key}`}
                              checked={value === "true"}
                              onCheckedChange={(checked) => setSteamValues((v) => ({ ...v, [option.key]: checked ? "true" : "false" }))}
                            />
                            <span className="text-xs text-muted-foreground">{option.flag ? `${option.flag} ${value === "true" ? "on" : "off"}` : value}</span>
                          </div>
                        ) : option.type === "select" ? (
                          <SimpleSelect
                            value={value}
                            onValueChange={(v) => setSteamValues((prev) => ({ ...prev, [option.key]: v }))}
                            placeholder={option.required ? "Choose an option…" : "Use game default"}
                            options={(option.options || []).map((choice) => ({ value: choice.value, label: choice.label }))}
                          />
                        ) : option.type === "password" ? (
                          <SecretInput
                            id={`launch-${option.key}`}
                            value={value}
                            onChange={(v) => setSteamValues((prev) => ({ ...prev, [option.key]: v }))}
                          />
                        ) : (
                          <Input
                            id={`launch-${option.key}`}
                            type={option.type === "number" ? "number" : "text"}
                            value={value}
                            min={option.min}
                            max={option.max}
                            minLength={option.min_length}
                            maxLength={option.max_length}
                            onChange={(e) => setSteamValues((prev) => ({ ...prev, [option.key]: e.target.value }))}
                            className="font-mono text-xs"
                          />
                        )}
                        {option.description && <p className="text-xs text-muted-foreground">{option.description}</p>}
                      </div>
                    );
                  })}
                </div>

                {(launch.steam.argv || []).length > 0 && (
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-1.5 text-sm font-medium">
                      Launch arguments
                      <HelpHint text="The exact argument list the game is started with. Automatic values (port, name, save directory) come from the server settings above." />
                    </div>
                    <div className="overflow-x-auto rounded-md border bg-muted/30 p-3 font-mono text-xs text-muted-foreground">
                      {shellQuote(launch.steam.argv || [])}
                    </div>
                  </div>
                )}
              </>
            )}

            {launch.memory && (
              <div className="space-y-3">
                <div className="flex items-center gap-2 text-sm font-medium">
                  <MemoryStick className="h-4 w-4 text-primary" /> Java memory
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-1.5">
                      <Label htmlFor="launch-min-mem">Min memory (-Xms)</Label>
                      <HelpHint text="Initial heap. Sizes look like 512M, 2G or 1024." />
                    </div>
                    <Input id="launch-min-mem" value={memoryValues.min} onChange={(e) => setMemoryValues((v) => ({ ...v, min: e.target.value }))} className="font-mono text-xs" />
                  </div>
                  <div className="space-y-1.5">
                    <div className="flex items-center gap-1.5">
                      <Label htmlFor="launch-max-mem">Max memory (-Xmx)</Label>
                      <HelpHint text="Upper heap limit. Give the JVM as much as the machine can spare — modpacks often need 4–8 GB." />
                    </div>
                    <Input id="launch-max-mem" value={memoryValues.max} onChange={(e) => setMemoryValues((v) => ({ ...v, max: e.target.value }))} className="font-mono text-xs" />
                  </div>
                </div>
                {launch.command && (
                  <div className="overflow-x-auto rounded-md border bg-muted/30 p-3 font-mono text-xs text-muted-foreground">{launch.command}</div>
                )}
              </div>
            )}

            {launchError && <p role="alert" className="text-sm text-destructive">{launchError}</p>}

            <div className="flex items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">
                {launch.steam ? "Changes apply the next time the server starts." : "Applied at the next start."}
              </p>
              <Button onClick={saveLaunch} disabled={launchBusy || !launchDirty}>
                <Save /> {launchBusy ? "Saving…" : "Save parameters"}
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      <Card className="border-destructive/30">
        <CardHeader><CardTitle className="text-base text-destructive flex items-center gap-2">
          <Database className="h-4 w-4" /> Storage
        </CardTitle></CardHeader>
        <CardContent>
          <div className="font-mono text-xs text-muted-foreground">{server.path}</div>
          <p className="mt-2 text-xs text-muted-foreground">
            Deleting the server (with files) removes this directory permanently. Use the delete button in the header.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
