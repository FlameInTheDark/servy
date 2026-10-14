import { useMemo, useState, type ReactNode } from "react";
import { toast } from "sonner";
import {
  Activity, Check, ChevronDown, CircleHelp, Copy, Eye, EyeOff, Globe, Network,
  Plus, RefreshCw, RotateCcw, Save, ScrollText, ServerCog, Settings2, ShieldAlert,
  ShieldCheck, X, type LucideIcon,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { PATCH } from "@/api/client";
import {
  Badge, Button, Card, CardContent, CardHeader, CardTitle, Input, Label,
  Skeleton, Switch,
} from "@/components/ui/primitives";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
  Tooltip, TooltipContent, TooltipTrigger,
} from "@/components/ui/overlays";
import { cn } from "@/lib/utils";

/* ------------------------------ field schema ------------------------------- */

/** How a value is edited: the UI picks a control, the wire format stays flat. */
type Kind = "bool" | "number" | "text" | "list" | "secret" | "tiers";

interface FieldDef {
  key: string;
  label: string;
  kind?: Kind;
  /** One-liner shown behind the ? icon — only for options that need context. */
  hint?: string;
  unit?: string;
  min?: number;
  max?: number;
  placeholder?: string;
  /** Value is only read at boot, so changing it needs a panel restart. */
  restart?: boolean;
  /** Rarely touched knob, hidden behind the section's "Advanced" toggle. */
  advanced?: boolean;
}

interface SectionDef {
  id: string;
  label: string;
  icon: LucideIcon;
  fields: FieldDef[];
}

const SECTIONS: SectionDef[] = [
  {
    id: "general",
    label: "General",
    icon: Globe,
    fields: [
      {
        key: "http_host", label: "Bind address", placeholder: "0.0.0.0", restart: true,
        hint: "Interface the panel listens on. 0.0.0.0 accepts connections from your network, 127.0.0.1 keeps it local.",
      },
      {
        key: "https_port", label: "Panel port", kind: "number", min: 1, max: 65535, restart: true,
        hint: "Port the web panel and its API are served on.",
      },
    ],
  },
  {
    id: "security",
    label: "Security",
    icon: ShieldCheck,
    fields: [
      {
        key: "max_login_attempts", label: "Login attempts", kind: "number", min: 1,
        hint: "Failed sign-ins allowed per account before it is locked out.",
      },
      {
        key: "cookie_expire", label: "Session lifetime", kind: "number", min: 1, unit: "days",
        hint: "How long a signed-in session stays valid before the user has to log in again.",
      },
      {
        key: "enable_otp_skew", label: "Allow TOTP clock skew", kind: "bool",
        hint: "Also accept 2FA codes from the neighbouring 30-second window — useful when the server clock drifts.",
      },
      {
        key: "super_mfa", label: "Require 2FA for superusers", kind: "bool",
        hint: "Superusers must set up a TOTP authenticator before they can use the panel.",
      },
    ],
  },
  {
    id: "logs",
    label: "Logs & Console",
    icon: ScrollText,
    fields: [
      {
        key: "max_log_lines", label: "Log buffer", kind: "number", min: 50, unit: "lines",
        hint: "Lines kept in memory per log stream; console and log views read from this buffer.",
      },
      {
        key: "virtual_terminal_lines", label: "Console scrollback", kind: "number", min: 10, unit: "lines",
        hint: "Lines kept in the live console of each server.",
      },
      {
        key: "terminal_keywords", label: "Highlight keywords", kind: "list", placeholder: "ERROR, WARN",
        hint: "Words highlighted in server consoles.",
      },
      {
        key: "panel_logs_delete_after_days", label: "Delete panel logs after", kind: "number", min: 0, unit: "days",
        hint: "Panel log files older than this are pruned at startup. 0 keeps them forever.",
      },
      {
        key: "general_user_log_access", label: "Let members read panel logs", kind: "bool",
        hint: "Allow non-superusers to open the Panel Logs page.",
      },
    ],
  },
  {
    id: "monitoring",
    label: "Monitoring",
    icon: Activity,
    fields: [
      {
        key: "stats_update_frequency_seconds", label: "Sample every", kind: "number", min: 1, unit: "seconds",
        hint: "How often CPU, memory and player counts are recorded.",
      },
      {
        key: "history_max_age", label: "Keep history", kind: "number", min: 1, unit: "days",
        hint: "Statistics older than this are deleted; it also caps the history charts.",
      },
      {
        key: "dir_size_poll_freq_minutes", label: "Measure world size every", kind: "number", min: 1, unit: "minutes",
        hint: "How often the size of each server directory is recalculated.",
      },
      {
        key: "monitored_mounts", label: "Disks", kind: "list", placeholder: "/, /mnt/data",
        hint: "Mount points shown in the host disk charts.",
      },
      {
        key: "sampling_fallback_divisor", label: "Chart fallback divisor", kind: "number", min: 1, advanced: true,
        hint: "Downsampling used for chart windows that match none of the tiers below.",
      },
      {
        key: "sampling_tiers", label: "History sampling tiers", kind: "tiers", advanced: true,
        hint: "Thin out chart points as the time range grows: up to N hours keep every Nth sample.",
      },
    ],
  },
  {
    id: "servers",
    label: "Servers",
    icon: ServerCog,
    fields: [
      {
        key: "big_bucket_repo", label: "Server catalog URL",
        hint: "Base URL of the BigBucket manifest used by the server creation wizard to download game builds.",
      },
      {
        key: "java_extra_paths", label: "Extra Java paths", kind: "list", placeholder: "/opt/java",
        hint: "Additional directories scanned for Java runtimes, besides the bundled and system JVMs.",
      },
      {
        key: "docker_steam_image", label: "SteamCMD image",
        hint: "Container image used to install and run SteamCMD games.",
      },
    ],
  },
  {
    id: "cluster",
    label: "Cluster",
    icon: Network,
    fields: [
      {
        key: "grpc_enabled", label: "Enable cluster nodes", kind: "bool", restart: true,
        hint: "Lets other machines join this panel as nodes. Each node gets its own credential through a join ticket — no shared secret.",
      },
      {
        key: "grpc_port", label: "Node API port", kind: "number", min: 1, max: 65535, restart: true,
        hint: "Port other machines must reach on this panel to join the cluster. Open it in your firewall.",
      },
    ],
  },
];

const RESTART_HINT = "Takes effect after restarting the panel.";

/* ------------------------------ small pieces ------------------------------- */

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
      <TooltipContent className="max-w-[260px] leading-relaxed">{text}</TooltipContent>
    </Tooltip>
  );
}

function IconAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label}
          onClick={onClick}
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function FieldLabel({ field }: { field: FieldDef }) {
  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <Label htmlFor={field.key} className="truncate text-xs font-medium">
        {field.label}
      </Label>
      {field.hint && <HelpHint text={field.hint} />}
      {field.restart && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge variant="muted" className="cursor-default px-1.5 py-0 text-[10px] font-normal">
              restart
            </Badge>
          </TooltipTrigger>
          <TooltipContent>{RESTART_HINT}</TooltipContent>
        </Tooltip>
      )}
    </div>
  );
}

/** Comma/Enter driven chip editor for the list settings. */
function TagsInput({ id, value, onChange, placeholder }: {
  id: string;
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
}) {
  const [text, setText] = useState("");
  const tags = Array.isArray(value) ? value : [];

  const commit = (raw: string) => {
    const parts = raw.split(",").map((s) => s.trim()).filter(Boolean);
    setText("");
    if (!parts.length) return;
    onChange([...tags, ...parts.filter((p) => !tags.includes(p))]);
  };

  return (
    <div className="flex min-h-9 flex-wrap items-center gap-1 rounded-md border border-input bg-background/60 px-1.5 py-1 focus-within:ring-1 focus-within:ring-ring">
      {tags.map((tag) => (
        <span key={tag} className="inline-flex items-center gap-1 rounded bg-muted py-0.5 pl-1.5 pr-1 font-mono text-[11px]">
          {tag}
          <button
            type="button"
            aria-label={`Remove ${tag}`}
            className="text-muted-foreground transition-colors hover:text-foreground"
            onClick={() => onChange(tags.filter((t) => t !== tag))}
          >
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
      <input
        id={id}
        value={text}
        onChange={(e) => (e.target.value.includes(",") ? commit(e.target.value) : setText(e.target.value))}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit(text);
          } else if (e.key === "Backspace" && !text && tags.length) {
            onChange(tags.slice(0, -1));
          }
        }}
        onBlur={() => commit(text)}
        placeholder={tags.length ? "" : placeholder}
        className="h-6 min-w-[7rem] flex-1 bg-transparent px-1 text-xs outline-none placeholder:text-muted-foreground"
      />
    </div>
  );
}

/** Row editor for sampling_tiers: [{ max_hours, sample_rate }]. */
function TiersInput({ value, onChange }: {
  value: { max_hours: number; sample_rate: number }[];
  onChange: (v: { max_hours: number; sample_rate: number }[]) => void;
}) {
  const tiers = Array.isArray(value) ? value : [];
  const patch = (i: number, p: Partial<{ max_hours: number; sample_rate: number }>) =>
    onChange(tiers.map((t, idx) => (idx === i ? { ...t, ...p } : t)));

  return (
    <div className="space-y-2">
      {tiers.map((tier, i) => (
        <div key={i} className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="shrink-0">up to</span>
          <Input
            type="number" min={1} value={tier.max_hours ?? 0}
            onChange={(e) => patch(i, { max_hours: Number(e.target.value) })}
            className="h-8 w-20"
          />
          <span className="shrink-0">h · keep every</span>
          <Input
            type="number" min={1} value={tier.sample_rate ?? 0}
            onChange={(e) => patch(i, { sample_rate: Number(e.target.value) })}
            className="h-8 w-16"
          />
          <span className="shrink-0">th sample</span>
          <button
            type="button"
            aria-label="Remove tier"
            className="ml-auto text-muted-foreground transition-colors hover:text-destructive"
            onClick={() => onChange(tiers.filter((_, idx) => idx !== i))}
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      ))}
      <Button variant="outline" size="sm" onClick={() => onChange([...tiers, { max_hours: 24, sample_rate: 2 }])}>
        <Plus /> Add tier
      </Button>
    </div>
  );
}

function SecretInput({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  const [shown, setShown] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value ?? "");
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Clipboard is not available");
    }
  };

  const generate = () => {
    const bytes = new Uint8Array(24);
    crypto.getRandomValues(bytes);
    onChange(Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join(""));
  };

  return (
    <div className="relative">
      <Input
        id={id}
        type={shown ? "text" : "password"}
        value={value ?? ""}
        onChange={(e) => onChange(e.target.value)}
        autoComplete="off"
        spellCheck={false}
        className="pr-24 font-mono text-xs"
      />
      <div className="absolute right-1 top-1/2 flex -translate-y-1/2 items-center">
        <IconAction label={shown ? "Hide" : "Show"} onClick={() => setShown(!shown)}>
          {shown ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </IconAction>
        <IconAction label={copied ? "Copied" : "Copy"} onClick={copy}>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </IconAction>
        <IconAction label="Generate new secret" onClick={generate}>
          <RefreshCw className="h-3.5 w-3.5" />
        </IconAction>
      </div>
    </div>
  );
}

/* --------------------------------- field row -------------------------------- */

function Field({
  field, value, changed, onChange, onRevert,
}: {
  field: FieldDef;
  value: any;
  changed: boolean;
  onChange: (v: any) => void;
  onRevert: () => void;
}) {
  const kind = field.kind ?? "text";
  const wide = kind !== "number";

  if (kind === "bool") {
    return (
      <div className={cn(
        "flex items-center justify-between gap-4 rounded-lg border px-3.5 py-2.5 transition-colors sm:col-span-2",
        changed ? "border-warning/40 bg-warning/[0.04]" : "border-border/60 bg-muted/10",
      )}>
        <div className="flex min-w-0 items-center gap-1.5">
          <Label htmlFor={field.key} className="cursor-pointer truncate text-xs font-medium">{field.label}</Label>
          {field.hint && <HelpHint text={field.hint} />}
          {changed && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {field.restart && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Badge variant="muted" className="cursor-default px-1.5 py-0 text-[10px] font-normal">restart</Badge>
              </TooltipTrigger>
              <TooltipContent>{RESTART_HINT}</TooltipContent>
            </Tooltip>
          )}
          <Switch id={field.key} checked={!!value} onCheckedChange={onChange} />
        </div>
      </div>
    );
  }

  return (
    <div className={cn(
      "space-y-2 rounded-lg border px-3.5 py-3 transition-colors",
      changed ? "border-warning/40 bg-warning/[0.04]" : "border-border/60 bg-muted/10",
      wide && "sm:col-span-2",
    )}>
      <div className="flex items-center gap-1.5">
        <FieldLabel field={field} />
        {changed && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />}
      </div>

      {kind === "number" ? (
        <div className="relative">
          <Input
            id={field.key}
            type="number"
            min={field.min}
            max={field.max}
            value={value ?? ""}
            onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
            onBlur={() => value === "" && onRevert()}
            className={cn("h-8", field.unit && "pr-20")}
          />
          {field.unit && (
            <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[11px] text-muted-foreground">
              {field.unit}
            </span>
          )}
        </div>
      ) : kind === "list" ? (
        <TagsInput id={field.key} value={value} onChange={onChange} placeholder={field.placeholder} />
      ) : kind === "tiers" ? (
        <TiersInput value={value} onChange={onChange} />
      ) : kind === "secret" ? (
        <SecretInput id={field.key} value={value} onChange={onChange} />
      ) : (
        <Input
          id={field.key}
          value={value ?? ""}
          onChange={(e) => onChange(e.target.value)}
          placeholder={field.placeholder}
          className="h-8 font-mono text-xs"
        />
      )}
    </div>
  );
}

/* -------------------------------- maintenance ------------------------------- */

function MaintenanceCard({ pending, busy, onRotate }: {
  pending: boolean;
  busy: boolean;
  onRotate: (v: boolean) => void;
}) {
  const [confirm, setConfirm] = useState(false);

  return (
    <Card>
      <CardHeader className="pb-4">
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldAlert className="h-4 w-4 text-muted-foreground" />
          Sessions &amp; secrets
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className={cn(
          "flex flex-wrap items-center justify-between gap-3 rounded-lg border px-3.5 py-2.5",
          pending ? "border-warning/40 bg-warning/[0.04]" : "border-border/60 bg-muted/10",
        )}>
          <div className="flex items-center gap-1.5 text-xs">
            <span className={pending ? "font-medium text-warning" : "text-muted-foreground"}>
              {pending ? "Rotation scheduled for the next restart" : "No rotation scheduled"}
            </span>
            <HelpHint text="Regenerating the panel secrets signs out every user and invalidates existing API keys the next time the panel starts." />
          </div>
          {pending ? (
            <Button variant="outline" size="sm" disabled={busy} onClick={() => onRotate(false)}>
              Cancel
            </Button>
          ) : (
            <Button variant="destructive" size="sm" disabled={busy} onClick={() => setConfirm(true)}>
              <RotateCcw /> Rotate
            </Button>
          )}
        </div>
      </CardContent>

      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Rotate panel secrets?</DialogTitle>
            <DialogDescription>
              On the next restart all sessions and API keys are regenerated — every user has to sign in again.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => {
                setConfirm(false);
                onRotate(true);
              }}
            >
              Schedule rotation
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

/* ----------------------------------- page ---------------------------------- */

export default function PanelConfigPage() {
  const { data, error, refetch } = useApi<{ categories: Record<string, Record<string, any>> }>("/panel/config");
  const [dirty, setDirty] = useState<Record<string, any>>({});
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState("general");
  const [advanced, setAdvanced] = useState<Record<string, boolean>>({});

  const values = useMemo(() => {
    const flat: Record<string, any> = {};
    for (const group of Object.values(data?.categories ?? {})) Object.assign(flat, group);
    return flat;
  }, [data]);

  const set = (key: string, value: any) =>
    setDirty((d) => {
      const next = { ...d, [key]: value };
      if (JSON.stringify(value) === JSON.stringify(values[key])) delete next[key];
      return next;
    });

  const revert = (key: string) =>
    setDirty((d) => {
      const next = { ...d };
      delete next[key];
      return next;
    });

  const dirtyKeys = Object.keys(dirty);

  const save = async () => {
    const patch: Record<string, any> = {};
    for (const [key, value] of Object.entries(dirty)) {
      if (value === "" || (typeof value === "number" && !Number.isFinite(value))) continue;
      patch[key] = value;
    }
    if (!Object.keys(patch).length) {
      setDirty({});
      return;
    }
    setBusy(true);
    try {
      await PATCH("/panel/config", patch);
      const needsRestart = SECTIONS.some((s) => s.fields.some((f) => f.restart && f.key in patch));
      toast.success(needsRestart ? "Saved — restart the panel to apply" : "Settings saved");
      setDirty({});
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };

  const rotate = async (value: boolean) => {
    setBusy(true);
    try {
      await PATCH("/panel/config", { reset_secrets_on_next_boot: value });
      refetch();
      toast.success(value ? "Secrets rotate on the next restart" : "Rotation cancelled");
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };

  if (error) {
    return (
      <div className="p-6">
        <Card className="mx-auto max-w-md">
          <CardContent className="space-y-3 pt-6 text-center text-sm text-muted-foreground">
            <ShieldAlert className="mx-auto h-6 w-6" />
            <p>{error}</p>
            <Button variant="outline" size="sm" onClick={refetch}>
              <RefreshCw /> Retry
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="p-6">
        <div className="mx-auto max-w-5xl space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
      </div>
    );
  }

  const nav = [...SECTIONS, { id: "maintenance", label: "Maintenance", icon: ShieldAlert, fields: [] }];

  const countFor = (s: SectionDef) => dirtyKeys.filter((k) => s.fields.some((f) => f.key === k)).length;

  const navButton = (item: SectionDef, compact = false) => {
    const modified = countFor(item);
    const active = tab === item.id;
    return (
      <button
        key={item.id}
        onClick={() => setTab(item.id)}
        className={cn(
          "flex items-center gap-2.5 whitespace-nowrap rounded-md px-3 py-2 text-sm transition-colors",
          compact && "shrink-0 border py-1.5 text-xs",
          active
            ? "bg-accent font-medium text-foreground"
            : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
        )}
      >
        <item.icon className="h-4 w-4 shrink-0" />
        <span className="truncate">{item.label}</span>
        {modified > 0 && (
          <span className="ml-auto rounded-full bg-warning px-1.5 text-[10px] font-semibold text-warning-foreground">
            {modified}
          </span>
        )}
      </button>
    );
  };

  const section = SECTIONS.find((s) => s.id === tab);
  const restarted = !!values.reset_secrets_on_next_boot;

  return (
    <div className="px-4 pb-10 md:px-6">
      <div className="mx-auto max-w-5xl">
        {/* header / save bar */}
        <header className="-mx-4 mb-5 flex flex-wrap items-center gap-3 border-b bg-background/85 px-4 py-3 backdrop-blur md:sticky md:top-0 md:z-10 md:-mx-6 md:px-6">
          <div className="flex items-center gap-2">
            <Settings2 className="h-5 w-5 text-muted-foreground" />
            <h1 className="text-xl font-bold tracking-tight">Panel Settings</h1>
          </div>
          <div className="ml-auto flex items-center gap-2">
            {dirtyKeys.length > 0 && (
              <span className="text-xs tabular-nums text-muted-foreground">{dirtyKeys.length} unsaved</span>
            )}
            {dirtyKeys.length > 0 && (
              <Button variant="ghost" size="sm" onClick={() => setDirty({})}>
                <RotateCcw /> Discard
              </Button>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon-sm" aria-label="Reload" onClick={refetch}>
                  <RefreshCw className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Reload</TooltipContent>
            </Tooltip>
            <Button size="sm" disabled={!dirtyKeys.length || busy} onClick={save}>
              <Save /> Save
            </Button>
          </div>
        </header>

        {/* mobile section picker */}
        <div className="-mx-4 mb-4 flex gap-1.5 overflow-x-auto px-4 pb-1 md:hidden">
          {nav.map((item) => navButton(item as SectionDef, true))}
        </div>

        <div className="grid gap-6 md:grid-cols-[190px_1fr]">
          {/* section nav */}
          <nav className="hidden md:block">
            <div className="sticky top-[72px] space-y-1">
              {nav.map((item) => navButton(item as SectionDef))}
            </div>
          </nav>

          {/* section content */}
          <div className="min-w-0 space-y-4">
            {section && (() => {
              const basic = section.fields.filter((f) => !f.advanced && f.key in values);
              const extra = section.fields.filter((f) => f.advanced && f.key in values);
              const open = !!advanced[section.id];
              return (
                <Card>
                  <CardHeader className="pb-4">
                    <CardTitle className="flex items-center gap-2 text-base">
                      <section.icon className="h-4 w-4 text-muted-foreground" />
                      {section.label}
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="grid gap-2 sm:grid-cols-2">
                    {basic.map((field) => (
                      <Field
                        key={field.key}
                        field={field}
                        value={dirty[field.key] !== undefined ? dirty[field.key] : values[field.key]}
                        changed={dirty[field.key] !== undefined}
                        onChange={(v) => set(field.key, v)}
                        onRevert={() => revert(field.key)}
                      />
                    ))}

                    {extra.length > 0 && (
                      <>
                        <button
                          type="button"
                          onClick={() => setAdvanced((a) => ({ ...a, [section.id]: !open }))}
                          className="flex items-center gap-1.5 pt-1 text-xs text-muted-foreground transition-colors hover:text-foreground sm:col-span-2"
                        >
                          <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", open && "rotate-180")} />
                          Advanced
                        </button>
                        {open && extra.map((field) => (
                          <Field
                            key={field.key}
                            field={field}
                            value={dirty[field.key] !== undefined ? dirty[field.key] : values[field.key]}
                            changed={dirty[field.key] !== undefined}
                            onChange={(v) => set(field.key, v)}
                            onRevert={() => revert(field.key)}
                          />
                        ))}
                      </>
                    )}
                  </CardContent>
                </Card>
              );
            })()}

            {tab === "maintenance" && (
              <MaintenanceCard pending={restarted} busy={busy} onRotate={rotate} />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
