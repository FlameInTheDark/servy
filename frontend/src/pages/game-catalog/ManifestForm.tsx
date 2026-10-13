import {
  Boxes,
  Braces,
  Cable,
  FileCog,
  HardDrive,
  ListChecks,
  Plus,
  Server,
  Trash2,
} from "lucide-react";
import type {
  ExposedGamePort,
  SteamConfigEditor,
  SteamGame,
  SteamLaunchOption,
  SteamLaunchOptionValue,
} from "@/api/client";
import { Badge, Button, Input, Label, Switch, Textarea } from "@/components/ui/primitives";

function Field({
  label,
  hint,
  children,
  className = "",
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={`min-w-0 space-y-1.5 ${className}`}>
      <Label className="text-xs font-semibold text-foreground/90">{label}</Label>
      {children}
      {hint && <p className="text-[11px] leading-relaxed text-muted-foreground">{hint}</p>}
    </div>
  );
}

function SelectControl({
  value,
  onChange,
  options,
}: {
  value: string;
  onChange: (value: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <select
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm outline-none transition focus-visible:ring-2 focus-visible:ring-ring"
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>{option.label}</option>
      ))}
    </select>
  );
}

function Section({
  index,
  title,
  description,
  icon: Icon,
  action,
  children,
}: {
  index: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-2xl border bg-card shadow-sm">
      <div className="flex items-start justify-between gap-4 border-b px-5 py-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <Icon className="h-4 w-4" />
          </div>
          <div className="min-w-0">
            <div className="text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">{index}</div>
            <h2 className="mt-0.5 text-sm font-semibold tracking-tight">{title}</h2>
            <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">{description}</p>
          </div>
        </div>
        {action && <div className="shrink-0">{action}</div>}
      </div>
      <div className="space-y-5 p-5">{children}</div>
    </section>
  );
}

function readLines(value?: string[]) {
  return (value || []).join("\n");
}

function toLines(value: string) {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function NumberInput({
  value,
  min,
  max,
  onChange,
  placeholder,
}: {
  value?: number;
  min?: number;
  max?: number;
  onChange: (value: number | undefined) => void;
  placeholder?: string;
}) {
  return (
    <Input
      type="number"
      value={value ?? ""}
      min={min}
      max={max}
      step={1}
      placeholder={placeholder}
      onChange={(event) => onChange(event.target.value === "" ? undefined : Number(event.target.value))}
    />
  );
}

function LaunchOptionCard({
  option,
  index,
  onChange,
  onRemove,
}: {
  option: SteamLaunchOption;
  index: number;
  onChange: (patch: Partial<SteamLaunchOption>) => void;
  onRemove: () => void;
}) {
  const setType = (type: SteamLaunchOption["type"]) => {
    const next: SteamLaunchOption = { ...option, type };
    if (type !== "number") { delete next.min; delete next.max; }
    if (type !== "text" && type !== "password") { delete next.min_length; delete next.max_length; }
    if (type !== "boolean") { delete next.true_value; delete next.false_value; }
    if (type !== "select") delete next.options;
    else if (!next.options?.length) next.options = [{ value: "", label: "" }];
    if (next.source) {
      const sourceType = next.source === "port" || next.source === "query_port" ? "number" : "text";
      if (type !== sourceType) delete next.source;
    }
    onChange(next);
  };

  const setSource = (source: string) => {
    if (!source) {
      onChange({ source: undefined });
      return;
    }
    const sourceType = source === "port" || source === "query_port" ? "number" : "text";
    const next: SteamLaunchOption = {
      ...option,
      source: source as SteamLaunchOption["source"],
      type: sourceType,
      default: undefined,
    };
    delete next.min_length;
    delete next.max_length;
    if (sourceType !== "number") { delete next.min; delete next.max; }
    delete next.options;
    delete next.true_value;
    delete next.false_value;
    onChange(next);
  };

  const updateChoice = (choiceIndex: number, patch: Partial<SteamLaunchOptionValue>) => {
    const choices = [...(option.options || [])];
    choices[choiceIndex] = { ...choices[choiceIndex], ...patch };
    onChange({ options: choices });
  };

  return (
    <article className="rounded-xl border bg-background/60 p-4 sm:p-5">
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted font-mono text-xs font-semibold text-muted-foreground">
            {String(index + 1).padStart(2, "0")}
          </span>
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold">{option.label || "Untitled parameter"}</div>
            <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
              <Badge variant="muted" className="font-mono text-[10px]">{option.key || "option_key"}</Badge>
              <Badge variant="outline" className="text-[10px]">{option.source ? "automatic" : option.type}</Badge>
              {option.flag && <code className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">{option.flag}</code>}
            </div>
          </div>
        </div>
        <Button variant="ghost" size="icon-sm" className="text-muted-foreground hover:text-destructive" onClick={onRemove} aria-label={`Remove ${option.label || "launch option"}`} title="Remove parameter">
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Field label="Key" hint="Stable ID; lowercase letters, numbers and underscores.">
          <Input value={option.key} onChange={(event) => onChange({ key: event.target.value })} placeholder="world_name" className="font-mono text-xs" />
        </Field>
        <Field label="Display label">
          <Input value={option.label} onChange={(event) => onChange({ label: event.target.value })} placeholder="World name" />
        </Field>
        <Field label="Input type">
          <SelectControl value={option.type} onChange={(value) => setType(value as SteamLaunchOption["type"])} options={[
            { value: "text", label: "Text" },
            { value: "password", label: "Password" },
            { value: "number", label: "Number" },
            { value: "boolean", label: "Toggle" },
            { value: "select", label: "Dropdown" },
          ]} />
        </Field>
        <Field label="Command flag" hint="Optional single argv token, e.g. -world.">
          <Input value={option.flag || ""} onChange={(event) => onChange({ flag: event.target.value || undefined })} placeholder="-world" className="font-mono text-xs" />
        </Field>
      </div>

      <div className="mt-3 grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(180px,0.65fr)]">
        <Field label="Help text" hint="Shown beside this parameter in the Create Server form.">
          <Input value={option.description || ""} onChange={(event) => onChange({ description: event.target.value || undefined })} placeholder="Explain what this setting controls" />
        </Field>
        <Field label="Default value" hint={option.source ? "Automatic values come from server settings." : "Leave blank if users should enter a value."}>
          <Input
            value={option.default || ""}
            disabled={!!option.source}
            onChange={(event) => onChange({ default: event.target.value || undefined })}
            placeholder={option.source ? "Provided automatically" : "No default"}
            className="font-mono text-xs"
          />
        </Field>
      </div>

      <details className="group mt-4 rounded-lg border bg-muted/20">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5 text-xs font-semibold text-muted-foreground hover:text-foreground [&::-webkit-details-marker]:hidden">
          <span>Advanced rules and automatic values</span>
          <span className="transition-transform group-open:rotate-180">⌄</span>
        </summary>
        <div className="space-y-4 border-t p-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Value source" hint="Use a value Servy already collects rather than asking the user again.">
              <SelectControl value={option.source || ""} onChange={setSource} options={[
                { value: "", label: "Entered by the user" },
                { value: "server_name", label: "Server name" },
                { value: "port", label: "Game port" },
                { value: "query_port", label: "Query port" },
                { value: "save_dir", label: "Save directory" },
              ]} />
            </Field>
            <div className="flex items-center justify-between rounded-lg border bg-background px-3 py-2.5">
              <div>
                <div className="text-xs font-semibold">Required</div>
                <div className="mt-0.5 text-[11px] text-muted-foreground">Creation cannot continue without it.</div>
              </div>
              <Switch checked={!!option.required} onCheckedChange={(required) => onChange({ required })} />
            </div>
          </div>

          {option.type === "number" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Minimum"><NumberInput value={option.min} onChange={(min) => onChange({ min })} placeholder="No minimum" /></Field>
              <Field label="Maximum"><NumberInput value={option.max} onChange={(max) => onChange({ max })} placeholder="No maximum" /></Field>
            </div>
          )}
          {(option.type === "text" || option.type === "password") && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Minimum length"><NumberInput value={option.min_length} min={0} max={4096} onChange={(min_length) => onChange({ min_length })} placeholder="No minimum" /></Field>
              <Field label="Maximum length"><NumberInput value={option.max_length} min={0} max={4096} onChange={(max_length) => onChange({ max_length })} placeholder="No maximum" /></Field>
            </div>
          )}
          {option.type === "boolean" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Value when enabled" hint="Optional token after the flag.">
                <Input value={option.true_value || ""} onChange={(event) => onChange({ true_value: event.target.value || undefined })} placeholder="1" className="font-mono text-xs" />
              </Field>
              <Field label="Value when disabled" hint="Leave blank to omit the flag when off.">
                <Input value={option.false_value || ""} onChange={(event) => onChange({ false_value: event.target.value || undefined })} placeholder="0" className="font-mono text-xs" />
              </Field>
            </div>
          )}
          {option.type === "select" && (
            <div className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <div className="text-xs font-semibold">Dropdown choices</div>
                  <p className="mt-0.5 text-[11px] text-muted-foreground">Values must be unique single arguments.</p>
                </div>
                <Button variant="outline" size="sm" onClick={() => onChange({ options: [...(option.options || []), { value: "", label: "" }] })}>
                  <Plus className="h-3.5 w-3.5" /> Add choice
                </Button>
              </div>
              <div className="space-y-2">
                {(option.options || []).map((choice, choiceIndex) => (
                  <div key={choiceIndex} className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_auto]">
                    <Field label="Submitted value"><Input value={choice.value} onChange={(event) => updateChoice(choiceIndex, { value: event.target.value })} placeholder="survival" className="font-mono text-xs" /></Field>
                    <Field label="Visible label"><Input value={choice.label} onChange={(event) => updateChoice(choiceIndex, { label: event.target.value })} placeholder="Survival" /></Field>
                    <Button variant="ghost" size="icon-sm" className="mb-0.5 text-muted-foreground hover:text-destructive" aria-label="Remove dropdown choice" onClick={() => onChange({ options: (option.options || []).filter((_, i) => i !== choiceIndex) })}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </details>
    </article>
  );
}

function PortRule({
  value,
  index,
  onChange,
  onRemove,
}: {
  value: ExposedGamePort;
  index: number;
  onChange: (patch: Partial<ExposedGamePort>) => void;
  onRemove: () => void;
}) {
  const mode = value.port ? "fixed" : "offset";
  return (
    <div className="grid items-end gap-3 rounded-xl border bg-background/60 p-3 sm:grid-cols-[minmax(110px,0.65fr)_minmax(150px,1fr)_minmax(110px,0.65fr)_auto]">
      <Field label={`Port ${index + 1}`}>
        <SelectControl value={mode} onChange={(next) => onChange(next === "fixed" ? { port: value.port || 27015, offset: undefined } : { port: undefined, offset: value.offset ?? 1 })} options={[
          { value: "fixed", label: "Fixed port" },
          { value: "offset", label: "Offset from game port" },
        ]} />
      </Field>
      {mode === "fixed" ? (
        <Field label="Container port" hint="Port the game listens on inside Docker.">
          <NumberInput value={value.port} min={1} max={65535} onChange={(port) => onChange({ port, offset: undefined })} placeholder="27015" />
        </Field>
      ) : (
        <Field label="Port offset" hint="Example: +1 for the next port.">
          <NumberInput value={value.offset ?? 0} min={0} max={65535} onChange={(offset) => onChange({ offset: offset ?? 0, port: undefined })} placeholder="0" />
        </Field>
      )}
      <Field label="Transport">
        <SelectControl value={value.proto || "tcp"} onChange={(proto) => onChange({ proto: proto as ExposedGamePort["proto"] })} options={[
          { value: "tcp", label: "TCP" },
          { value: "udp", label: "UDP" },
        ]} />
      </Field>
      <div className="flex items-center justify-end gap-2 pb-1">
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <Switch checked={!!value.optional} onCheckedChange={(optional) => onChange({ optional })} /> Optional
        </label>
        <Button variant="ghost" size="icon-sm" aria-label="Remove port rule" onClick={onRemove} className="text-muted-foreground hover:text-destructive">
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}

interface EnvDraft {
  key: string;
  values: string[];
  mode: "append" | "prepend" | "replace";
}

function unpackEnv(env?: Record<string, string[]>): EnvDraft[] {
  return Object.entries(env || {}).map(([key, raw]) => {
    const values = [...(raw || [])];
    let mode: EnvDraft["mode"] = "append";
    const last = values[values.length - 1];
    if (last?.startsWith("mode:")) {
      const parsed = last.slice(5);
      if (parsed === "append" || parsed === "prepend" || parsed === "replace") mode = parsed;
      values.pop();
    }
    return { key, values, mode };
  });
}

function packEnv(rows: EnvDraft[]) {
  const env: Record<string, string[]> = {};
  rows.forEach((row) => {
    const key = row.key.trim();
    if (!key) return;
    const values = row.values.map((value) => value.trim()).filter(Boolean);
    // The manifest parser encodes merge behavior as a second array item. Keep
    // an empty value slot so an in-progress row remains editable and retains
    // the selected mode until the admin fills it in.
    env[key] = [...(values.length ? values : [""]), `mode:${row.mode}`];
  });
  return Object.keys(env).length ? env : undefined;
}

export default function ManifestForm({ game, onChange }: { game: SteamGame; onChange: (game: SteamGame) => void }) {
  const patchGame = (patch: Partial<SteamGame>) => onChange({ ...game, ...patch });
  const launchOptions = game.launch_options || [];
  const configEditors = game.config_editors || [];
  const ports = game.expose || [];
  const envRows = unpackEnv(game.env);

  const setLaunchOption = (index: number, patch: Partial<SteamLaunchOption>) => {
    const next = [...launchOptions];
    next[index] = { ...next[index], ...patch };
    patchGame({ launch_options: next });
  };
  const removeLaunchOption = (index: number) => patchGame({ launch_options: launchOptions.filter((_, i) => i !== index) });
  const addLaunchOption = () => {
    const base = `option_${launchOptions.length + 1}`;
    const keys = new Set(launchOptions.map((option) => option.key));
    let key = base;
    let suffix = 2;
    while (keys.has(key)) key = `${base}_${suffix++}`;
    patchGame({ launch_options: [...launchOptions, { key, label: "New parameter", type: "text" }] });
  };

  const setConfigEditor = (index: number, patch: Partial<SteamConfigEditor>) => {
    const next = [...configEditors];
    next[index] = { ...next[index], ...patch };
    patchGame({ config_editors: next });
  };
  const addConfigEditor = () => patchGame({ config_editors: [...configEditors, { path: "config/new-file.txt", label: "New config file", editor: "auto" }] });

  const setPort = (index: number, patch: Partial<ExposedGamePort>) => {
    const next = [...ports];
    next[index] = { ...next[index], ...patch };
    patchGame({ expose: next });
  };

  const updateEnv = (index: number, patch: Partial<EnvDraft>) => {
    const next = [...envRows];
    next[index] = { ...next[index], ...patch };
    patchGame({ env: packEnv(next) });
  };

  return (
    <div className="space-y-4">
      <Section index="01 / identity" title="Game identity & ports" description="These values identify the Steam app and provide the defaults used by the Create Server wizard." icon={Server}>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Display name" hint="Shown in the game picker.">
            <Input value={game.name || ""} onChange={(event) => patchGame({ name: event.target.value })} placeholder="My Dedicated Server" />
          </Field>
          <Field label="Steam App ID" hint="Use the dedicated server app ID, not the client game's ID.">
            <Input type="number" min={1} max={2147483647} value={game.app_id || ""} onChange={(event) => patchGame({ app_id: event.target.value === "" ? 0 : Number(event.target.value) })} placeholder="123456" />
          </Field>
          <Field label="Catalog slug" hint="Lowercase letters, numbers and hyphens; unique across the active catalog.">
            <Input value={game.slug || ""} onChange={(event) => patchGame({ slug: event.target.value })} placeholder="my-game-server" className="font-mono text-xs" />
          </Field>
          <Field label="Default game port" hint="Users can change this when creating a server.">
            <Input type="number" min={1} max={65535} value={game.port || ""} onChange={(event) => patchGame({ port: event.target.value === "" ? 0 : Number(event.target.value) })} placeholder="27015" />
          </Field>
          <Field label="Query port override" hint={`Leave blank to use game port + 1${game.port < 65535 ? ` (${game.port + 1})` : ""}.`}>
            <NumberInput value={game.query_port} min={1} max={65535} onChange={(query_port) => patchGame({ query_port })} placeholder="Automatic" />
          </Field>
          <Field label="Port label" hint="Short label shown next to the game port.">
            <Input value={game.port_name || ""} onChange={(event) => patchGame({ port_name: event.target.value || undefined })} placeholder="game" />
          </Field>
        </div>
      </Section>

      <Section index="02 / startup" title="Install & startup commands" description="Servy downloads the Steam app, then runs this command directly. Launch-option values are passed as separate arguments—not through a shell." icon={Boxes}>
        <Field label="Run command" hint="Placeholders are available: {SERVER_NAME}, {PORT}, {QUERY_PORT}, {SAVE_DIR}. Use one command line; do not put user-supplied values into shell syntax.">
          <Input value={game.run_command || ""} onChange={(event) => patchGame({ run_command: event.target.value })} placeholder="./server -port {PORT} -savedir {SAVE_DIR}" className="font-mono text-xs" />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Server executable" hint="Relative to the installed server directory.">
            <Input value={game.executable || ""} onChange={(event) => patchGame({ executable: event.target.value })} placeholder="server" className="font-mono text-xs" />
          </Field>
          <Field label="Stop command" hint="Command sent for a graceful shutdown.">
            <Input value={game.stop_command || ""} onChange={(event) => patchGame({ stop_command: event.target.value || undefined })} placeholder="quit" className="font-mono text-xs" />
          </Field>
        </div>
      </Section>

      <Section
        index="03 / launch parameters"
        title="Launch parameters"
        description="Define the fields shown during server creation. Each value is validated and added to the process argument list safely."
        icon={ListChecks}
        action={<Button variant="outline" size="sm" onClick={addLaunchOption}><Plus className="h-3.5 w-3.5" /> Add parameter</Button>}
      >
        {launchOptions.length === 0 ? (
          <div className="rounded-xl border border-dashed px-5 py-8 text-center">
            <ListChecks className="mx-auto h-6 w-6 text-muted-foreground" />
            <p className="mt-2 text-sm font-medium">No launch parameters yet</p>
            <p className="mx-auto mt-1 max-w-md text-xs leading-relaxed text-muted-foreground">Add a typed field such as a world name, password, toggle, or dropdown. It will appear in Create Server for this game.</p>
            <Button variant="outline" size="sm" className="mt-4" onClick={addLaunchOption}><Plus className="h-3.5 w-3.5" /> Add the first parameter</Button>
          </div>
        ) : (
          <div className="space-y-3">
            {launchOptions.map((option, index) => (
              <LaunchOptionCard key={`${index}-${option.key}`} option={option} index={index}
                onChange={(patch) => setLaunchOption(index, patch)} onRemove={() => removeLaunchOption(index)} />
            ))}
          </div>
        )}
      </Section>

      <Section index="04 / runtime" title="Runtime & network exposure" description="Select a platform for SteamCMD downloads and declare ports for Docker-backed servers. Port rules do not open host firewalls." icon={Cable}>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Download platform" hint="Most games use Linux. Windows depots run inside a Wine-capable Docker image.">
            <SelectControl value={game.platform || "linux"} onChange={(platform) => patchGame({ platform: platform as SteamGame["platform"], ...(platform !== "windows" && game.windows_only ? { windows_only: false } : {}) })} options={[
              { value: "linux", label: "Linux" },
              { value: "windows", label: "Windows" },
            ]} />
          </Field>
          <div className="flex items-center justify-between rounded-lg border bg-background/60 px-3 py-2.5">
            <div className="pr-3">
              <div className="text-xs font-semibold">Windows-only title</div>
              <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">Requires Docker with Wine to run.</div>
            </div>
            <Switch checked={!!game.windows_only} onCheckedChange={(windows_only) => patchGame({ windows_only, platform: windows_only ? "windows" : game.platform })} />
          </div>
        </div>
        <Field label="Docker runtime image" hint={game.windows_only ? "Optional override. Leave blank to use Servy's configured default Wine image." : "Optional image override used when this game runs in Docker."}>
          <Input value={game.docker_image || ""} onChange={(event) => patchGame({ docker_image: event.target.value || undefined })} placeholder={game.windows_only ? "ghcr.io/example/game-wine:latest" : "e.g. ghcr.io/example/game:latest"} className="font-mono text-xs" />
        </Field>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="text-xs font-semibold">Docker port rules</div>
              <p className="mt-0.5 text-[11px] text-muted-foreground">An empty list preserves Servy's legacy game-port mapping.</p>
            </div>
            <Button variant="outline" size="sm" onClick={() => patchGame({ expose: [...ports, { proto: "udp" }] })}><Plus className="h-3.5 w-3.5" /> Add port rule</Button>
          </div>
          {ports.length === 0 ? (
            <div className="rounded-lg border border-dashed px-4 py-3 text-xs text-muted-foreground">No explicit rules. The game port will be published using the legacy default.</div>
          ) : (
            <div className="space-y-2">
              {ports.map((rule, index) => <PortRule key={index} value={rule} index={index} onChange={(patch) => setPort(index, patch)} onRemove={() => patchGame({ expose: ports.filter((_, i) => i !== index) })} />)}
            </div>
          )}
        </div>
      </Section>

      <Section index="05 / config & storage" title="Config files & persistent data" description="Declare the files the Config tab should surface and the directories associated with saves and backups." icon={FileCog}>
        <Field label="Config file paths" hint="One server-relative path per line. These paths are discoverable in the server Config tab.">
          <Textarea value={readLines(game.config_files)} onChange={(event) => patchGame({ config_files: toLines(event.target.value) })} rows={3} placeholder={"server.cfg\nconfig/game.ini"} className="font-mono text-xs" />
        </Field>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="text-xs font-semibold">Friendly config editors</div>
              <p className="mt-0.5 text-[11px] text-muted-foreground">Add labels, descriptions, or a row-based list editor for a declared path.</p>
            </div>
            <Button variant="outline" size="sm" onClick={addConfigEditor}><Plus className="h-3.5 w-3.5" /> Add editor</Button>
          </div>
          {configEditors.length === 0 ? (
            <div className="rounded-lg border border-dashed px-4 py-3 text-xs text-muted-foreground">No friendly editors declared. Matching files can still use the default editor based on their extension.</div>
          ) : configEditors.map((entry, index) => (
            <div key={`${index}-${entry.path}`} className="space-y-3 rounded-xl border bg-background/60 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="text-xs font-semibold">Config editor {index + 1}</div>
                <Button variant="ghost" size="icon-sm" aria-label="Remove config editor" className="text-muted-foreground hover:text-destructive" onClick={() => patchGame({ config_editors: configEditors.filter((_, i) => i !== index) })}><Trash2 className="h-4 w-4" /></Button>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Relative file path"><Input value={entry.path} onChange={(event) => setConfigEditor(index, { path: event.target.value })} placeholder="saves/adminlist.txt" className="font-mono text-xs" /></Field>
                <Field label="Visible label"><Input value={entry.label || ""} onChange={(event) => setConfigEditor(index, { label: event.target.value || undefined })} placeholder="Admin list" /></Field>
                <Field label="Editor type">
                  <SelectControl value={entry.editor || "auto"} onChange={(editor) => setConfigEditor(index, { editor: editor as SteamConfigEditor["editor"] })} options={[
                    { value: "auto", label: "Automatic" },
                    { value: "raw", label: "Raw text" },
                    { value: "lines", label: "One value per line" },
                  ]} />
                </Field>
                <Field label="Description"><Input value={entry.description || ""} onChange={(event) => setConfigEditor(index, { description: event.target.value || undefined })} placeholder="One player ID per line" /></Field>
              </div>
            </div>
          ))}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Save directories" hint="Relative paths associated with persistent world/save data.">
            <Textarea value={readLines(game.save_dirs)} onChange={(event) => patchGame({ save_dirs: toLines(event.target.value) })} rows={3} placeholder="saves" className="font-mono text-xs" />
          </Field>
          <Field label="Backup directories" hint="Relative paths associated with game backups.">
            <Textarea value={readLines(game.backup_dirs)} onChange={(event) => patchGame({ backup_dirs: toLines(event.target.value) })} rows={3} placeholder="saves" className="font-mono text-xs" />
          </Field>
        </div>
      </Section>

      <Section
        index="06 / environment"
        title="Environment & metadata"
        description="Seed environment variables at creation and add an optional note for administrators."
        icon={HardDrive}
        action={<Button variant="outline" size="sm" onClick={() => patchGame({ env: packEnv([...envRows, { key: `GAME_ENV_${envRows.length + 1}`, values: [""], mode: "append" }]) })}><Plus className="h-3.5 w-3.5" /> Add variable</Button>}
      >
        {envRows.length === 0 ? (
          <div className="rounded-lg border border-dashed px-4 py-3 text-xs text-muted-foreground">No environment overrides. Most games can leave this section empty.</div>
        ) : (
          <div className="space-y-3">
            {envRows.map((row, index) => (
              <div key={`${index}-${row.key}`} className="grid items-end gap-3 rounded-xl border bg-background/60 p-4 sm:grid-cols-[minmax(150px,0.7fr)_minmax(180px,1fr)_minmax(130px,0.6fr)_auto]">
                <Field label="Variable name"><Input value={row.key} onChange={(event) => updateEnv(index, { key: event.target.value })} placeholder="LD_LIBRARY_PATH" className="font-mono text-xs" /></Field>
                <Field label="Values" hint="One item per line."><Textarea value={row.values.join("\n")} onChange={(event) => updateEnv(index, { values: event.target.value.split(/\r?\n/) })} rows={2} placeholder="./linux64" className="font-mono text-xs" /></Field>
                <Field label="Merge behavior">
                  <SelectControl value={row.mode} onChange={(mode) => updateEnv(index, { mode: mode as EnvDraft["mode"] })} options={[
                    { value: "append", label: "Append" },
                    { value: "prepend", label: "Prepend" },
                    { value: "replace", label: "Replace" },
                  ]} />
                </Field>
                <Button variant="ghost" size="icon-sm" aria-label={`Remove environment variable ${row.key}`} className="mb-1 text-muted-foreground hover:text-destructive" onClick={() => patchGame({ env: packEnv(envRows.filter((_, i) => i !== index)) })}><Trash2 className="h-4 w-4" /></Button>
              </div>
            ))}
          </div>
        )}
        <Field label="Administrator notes" hint="Displayed in the catalog list only to help admins remember special setup requirements.">
          <Textarea value={game.notes || ""} onChange={(event) => patchGame({ notes: event.target.value || undefined })} rows={3} placeholder="Additional setup notes, caveats, or links…" />
        </Field>
        <div className="flex items-start gap-2 rounded-xl border border-warning/30 bg-warning/5 p-3 text-xs leading-relaxed text-muted-foreground">
          <Braces className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <p><b className="text-foreground">Workshop metadata:</b> {game.workshop ? "Workshop is marked as supported, but automated Workshop downloads are not wired yet." : "Workshop support can be marked here for catalog metadata; automated downloads are not wired yet."}</p>
          <Switch checked={!!game.workshop} onCheckedChange={(workshop) => patchGame({ workshop })} aria-label="Workshop metadata" />
        </div>
      </Section>
    </div>
  );
}
