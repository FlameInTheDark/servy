import { useMemo, useState } from "react";
import { toast } from "sonner";
import {
  Save, RefreshCw, RotateCcw, Settings2, ShieldCheck, ScrollText, Activity,
  Wrench, ServerCog, Palette, Link2, Globe,
} from "lucide-react";
import { useApi } from "@/hooks/useApi";
import { PATCH, POST } from "@/api/client";
import { Button, Card, CardContent, CardHeader, CardTitle, CardDescription, Input, Switch, Label, Skeleton, Badge } from "@/components/ui/primitives";
import { cn } from "@/lib/utils";

/* ------------------------------ field metadata ----------------------------- */

const CATEGORY_META: Record<string, { label: string; icon: any; description: string }> = {
  general: { label: "General", icon: Globe, description: "Panel basics: URLs, language and feature switches." },
  security: { label: "Security", icon: ShieldCheck, description: "Login hardening, sessions and MFA policy." },
  logs: { label: "Logs", icon: ScrollText, description: "Buffer sizes, retention and log visibility." },
  monitoring: { label: "Monitoring", icon: Activity, description: "Stats collection frequency and history retention." },
  server: { label: "Server & Cluster", icon: ServerCog, description: "Directories, gRPC cluster and node role." },
  miscellaneous: { label: "Miscellaneous", icon: Wrench, description: "Everything else the panel can tune." },
};

const FIELD_LABELS: Record<string, string> = {
  https_port: "HTTP port", http_host: "Bind address", language: "Default language",
  show_errors: "Show errors to users", show_contribute_link: "Show contribute link",
  disabled_language_files: "Disabled languages (csv)", big_bucket_repo: "BigBucket repository URL",
  enable_user_self_delete: "Allow users to delete themselves", base_url: "Base URL",
  experimental: "Experimental features", max_image_upload_size_mb: "Max image upload (MB)",
  allow_nsfw_profile_pictures: "Allow NSFW profile pictures", cookie_expire: "Session expiry (days)",
  reset_secrets_on_next_boot: "Reset secrets on next boot", enable_otp_skew: "Allow TOTP time skew",
  superMFA: "Require MFA for superusers", max_login_attempts: "Max login attempts",
  enable_passkey_auth: "Enable passkey auth", passkey_rp_name: "Passkey RP name",
  trusted_proxies: "Trusted proxies (csv)", max_log_lines: "Max log lines (buffer)",
  max_audit_entries: "Max audit entries", history_max_age: "Stats history retention (days)",
  panel_logs_delete_after_days: "Delete panel logs after (days, 0=never)",
  virtual_terminal_lines: "Virtual terminal buffer (lines)", terminal_keywords: "Terminal highlight keywords (csv)",
  general_user_log_access: "Users can view panel logs", silent_notif_fields: "Silent notification fields",
  monitored_mounts: "Monitored mounts (csv)", dir_size_poll_freq_minutes: "World size poll (minutes)",
  stats_update_frequency_seconds: "Stats save frequency (seconds)",
  sampling_fallback_divisor: "Chart sampling fallback divisor",
  delete_default_json: "Delete default JSON files", grpc_enabled: "Cluster gRPC enabled",
  grpc_port: "Cluster gRPC port", node_role: "Node role", node_name: "Node name",
  servers_dir: "Servers directory", backups_dir: "Backups directory", steamcmd_dir: "SteamCMD directory",
};

const FIELD_HINTS: Record<string, string> = {
  https_port: "Port the web panel listens on (restart required).",
  http_host: "Interface to bind — 0.0.0.0 exposes the panel on all interfaces.",
  language: "Fallback language for users without a preference.",
  show_errors: "Expose raw error details in API responses — leave off in production.",
  base_url: "Public URL used in links, webhooks and QR codes.",
  cookie_expire: "How long sessions stay valid before re-login.",
  max_login_attempts: "Failed logins before lockout.",
  trusted_proxies: "Reverse proxy IPs allowed to set X-Forwarded-For.",
  max_log_lines: "In-memory tail per server console.",
  virtual_terminal_lines: "Scrollback kept per server terminal.",
  terminal_keywords: "Words highlighted in consoles (e.g. WARN, ERROR).",
  history_max_age: "Older stats rows are pruned on boot.",
  stats_update_frequency_seconds: "How often CPU/RAM samples are persisted.",
  monitored_mounts: "Mount points tracked on the host stats charts.",
  grpc_enabled: "Enable the gRPC endpoint agents connect to (restart required).",
  grpc_port: "TCP port for cluster gRPC.",
  servers_dir: "Root directory holding all server folders.",
  backups_dir: "Where zip backups are written.",
  steamcmd_dir: "SteamCMD install location.",
  node_role: "panel, agent or standalone.",
  reset_secrets_on_next_boot: "Regenerates JWT/cookie secrets — invalidates all sessions.",
  superMFA: "Superusers must configure TOTP before using the panel.",
};

const BOOL_FIELDS = new Set(["show_errors", "show_contribute_link", "enable_user_self_delete", "experimental",
  "allow_nsfw_profile_pictures", "reset_secrets_on_next_boot", "enable_otp_skew", "superMFA",
  "enable_passkey_auth", "general_user_log_access", "delete_default_json", "grpc_enabled"]);
const NUMBER_FIELDS = new Set(["https_port", "cookie_expire", "max_login_attempts", "max_image_upload_size_mb",
  "max_log_lines", "max_audit_entries", "history_max_age", "panel_logs_delete_after_days",
  "virtual_terminal_lines", "dir_size_poll_freq_minutes", "stats_update_frequency_seconds",
  "sampling_fallback_divisor", "grpc_port"]);

/* --------------------------------- page ------------------------------------ */

export default function PanelConfigPage() {
  const { data: cfg, refetch } = useApi<any>("/panel/config");
  const [dirty, setDirty] = useState<Record<string, any>>({});
  const [busy, setBusy] = useState(false);
  const [section, setSection] = useState("general");

  const sections = useMemo(() => {
    const cats = Object.keys(cfg?.categories || {});
    // ordered: known categories first, unknown appended
    const known = Object.keys(CATEGORY_META).filter((c) => cats.includes(c));
    const extra = cats.filter((c) => !CATEGORY_META[c]);
    return [...known, ...extra];
  }, [cfg]);

  if (!cfg) {
    return (
      <div className="p-6">
        <Skeleton className="mx-auto h-96 max-w-5xl" />
      </div>
    );
  }

  const dirtyCount = Object.keys(dirty).length;
  const set = (k: string, v: any) => setDirty((d) => ({ ...d, [k]: v }));

  const save = async () => {
    setBusy(true);
    try {
      await PATCH("/panel/config", dirty);
      toast.success("Panel config saved (some settings apply after restart)");
      setDirty({});
      refetch();
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setBusy(false);
    }
  };

  const renderField = (k: string, entries: Record<string, any>) => {
    const value = dirty[k] !== undefined ? dirty[k] : entries[k];
    const orig = entries[k];
    const changed = dirty[k] !== undefined && JSON.stringify(dirty[k]) !== JSON.stringify(orig);
    const label = FIELD_LABELS[k] || k;
    const hint = FIELD_HINTS[k];
    return (
      <div
        key={k}
        className={cn(
          "space-y-1.5 rounded-lg border p-3.5 transition-colors",
          changed ? "border-ring/60 bg-accent/20" : "border-transparent bg-muted/20"
        )}
      >
        <div className="flex items-center justify-between gap-2">
          <Label className="text-xs font-medium">{label}</Label>
          {changed && <Badge variant="outline" className="text-[10px] text-muted-foreground">modified</Badge>}
        </div>
        {BOOL_FIELDS.has(k) ? (
          <Switch checked={!!value} onCheckedChange={(v) => set(k, v)} />
        ) : NUMBER_FIELDS.has(k) ? (
          <Input type="number" value={value ?? 0} onChange={(e) => set(k, +e.target.value)} className="bg-background" />
        ) : Array.isArray(orig) ? (
          <Input value={Array.isArray(value) ? value.join(", ") : String(value)} onChange={(e) => set(k, e.target.value.split(",").map((s) => s.trim()))} className="bg-background font-mono text-xs" />
        ) : (
          <Input value={String(value ?? "")} onChange={(e) => set(k, e.target.value)} className="bg-background font-mono text-xs" />
        )}
        {hint && <p className="text-[11px] leading-snug text-muted-foreground">{hint}</p>}
      </div>
    );
  };

  const meta = CATEGORY_META[section];
  const Icon = meta?.icon || Settings2;

  return (
    <div className="p-6">
      <div className="mx-auto max-w-5xl">
        {/* header */}
        <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
              <Settings2 className="h-6 w-6 text-muted-foreground" /> Panel Settings
            </h1>
            <p className="text-sm text-muted-foreground">Runtime configuration of the control panel.</p>
          </div>
          <div className="flex items-center gap-2">
            {dirtyCount > 0 && (
              <Button variant="ghost" size="sm" onClick={() => setDirty({})}>
                <RotateCcw /> Reset
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={refetch}>
              <RefreshCw /> Reload
            </Button>
            <Button size="sm" onClick={save} disabled={busy || dirtyCount === 0}>
              <Save /> Save {dirtyCount > 0 && `(${dirtyCount})`}
            </Button>
          </div>
        </div>

        <div className="grid gap-6 md:grid-cols-[200px_1fr]">
          {/* section nav */}
          <nav className="hidden space-y-1 md:block">
            <div className="sticky top-6 space-y-1">
              {sections.map((cat) => {
                const m = CATEGORY_META[cat];
                const CIcon = m?.icon || Settings2;
                const count = Object.keys(cfg.categories?.[cat] || {}).length;
                const modified = Object.keys(dirty).filter((k) => cfg.categories?.[cat]?.[k] !== undefined).length;
                return (
                  <button
                    key={cat}
                    onClick={() => setSection(cat)}
                    className={cn(
                      "flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
                      section === cat && "bg-accent font-medium text-foreground"
                    )}
                  >
                    <CIcon className="h-4 w-4 shrink-0" />
                    <span className="truncate">{m?.label || cat}</span>
                    {modified > 0 && <span className="ml-auto rounded-full bg-foreground px-1.5 text-[10px] font-semibold text-background">{modified}</span>}
                    {modified === 0 && count > 0 && section !== cat && <span className="ml-auto text-[10px] tabular-nums text-muted-foreground/60">{count}</span>}
                  </button>
                );
              })}
              {(["branding", "embed"] as const).map((extra) => {
                const labels: Record<string, string> = { branding: "Branding", embed: "Embeds" };
                const icons: Record<string, any> = { branding: Palette, embed: Link2 };
                const EIcon = icons[extra];
                return (
                  <button
                    key={extra}
                    onClick={() => setSection(extra)}
                    className={cn(
                      "flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
                      section === extra && "bg-accent font-medium text-foreground"
                    )}
                  >
                    <EIcon className="h-4 w-4 shrink-0" />
                    <span className="truncate">{labels[extra]}</span>
                  </button>
                );
              })}
            </div>
          </nav>

          {/* content */}
          <div className="min-w-0 space-y-4">
            {/* config sections */}
            {sections.map((cat) =>
              section === cat ? (
                <Card key={cat}>
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2 text-base">
                      <Icon className="h-4 w-4 text-muted-foreground" />
                      {meta?.label || cat}
                    </CardTitle>
                    <CardDescription>{meta?.description || "Panel configuration values."}</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <div className="grid gap-3 md:grid-cols-2">
                      {Object.keys(cfg.categories?.[cat] || {}).map((k) => renderField(k, cfg.categories[cat]))}
                    </div>
                    {Object.keys(cfg.categories?.[cat] || {}).length === 0 && (
                      <p className="p-4 text-sm text-muted-foreground">Nothing in this section.</p>
                    )}
                  </CardContent>
                </Card>
              ) : null
            )}

            {section === "branding" && <BrandingPanel branding={cfg.branding || {}} />}
            {section === "embed" && <EmbedPanel embed={cfg.embed || {}} />}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ------------------------------- sub-panels -------------------------------- */

function BrandingPanel({ branding }: { branding: any }) {
  const [b, setB] = useState(branding);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base"><Palette className="h-4 w-4 text-muted-foreground" /> Login & logo branding</CardTitle>
        <CardDescription>Customize the login page appearance and panel logos.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 md:grid-cols-2">
        <div className="space-y-1.5">
          <Label>Login background photo URL</Label>
          <Input value={b.login_photo || ""} onChange={(e) => setB({ ...b, login_photo: e.target.value })} placeholder="https://…" className="font-mono text-xs" />
        </div>
        <div className="space-y-1.5">
          <Label>Login background opacity ({b.login_opacity ?? 1})</Label>
          <Input type="number" step="0.05" min="0" max="1" value={b.login_opacity ?? 1} onChange={(e) => setB({ ...b, login_opacity: +e.target.value })} />
        </div>
        <div className="space-y-1.5">
          <Label>Full logo URL</Label>
          <Input value={b.logo_full || ""} onChange={(e) => setB({ ...b, logo_full: e.target.value })} className="font-mono text-xs" />
        </div>
        <div className="space-y-1.5">
          <Label>Square logo URL</Label>
          <Input value={b.logo_square || ""} onChange={(e) => setB({ ...b, logo_square: e.target.value })} className="font-mono text-xs" />
        </div>
        <div className="flex justify-end md:col-span-2">
          <Button onClick={async () => {
            try {
              await POST("/panel/config/customize", b);
              toast.success("Branding saved");
            } catch (e: any) { toast.error(e.message); }
          }}><Save /> Save branding</Button>
        </div>
      </CardContent>
    </Card>
  );
}

function EmbedPanel({ embed }: { embed: any }) {
  const [e2, setE2] = useState(embed);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base"><Link2 className="h-4 w-4 text-muted-foreground" /> OpenGraph embeds</CardTitle>
        <CardDescription>Link previews for the public pages (Discord, Slack, …).</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 md:grid-cols-2">
        <div className="flex items-center justify-between rounded-lg border border-transparent bg-muted/20 p-3.5 md:col-span-2">
          <div>
            <div className="text-sm font-medium">Embeds enabled</div>
            <div className="text-xs text-muted-foreground">Publish OpenGraph tags on public pages</div>
          </div>
          <Switch checked={!!e2.og_enabled} onCheckedChange={(v) => setE2({ ...e2, og_enabled: v })} />
        </div>
        <div className="space-y-1.5"><Label>Title</Label><Input value={e2.og_title || ""} onChange={(ev) => setE2({ ...e2, og_title: ev.target.value })} /></div>
        <div className="space-y-1.5"><Label>Theme color</Label><Input value={e2.og_color || ""} onChange={(ev) => setE2({ ...e2, og_color: ev.target.value })} placeholder="#71717a" className="font-mono text-xs" /></div>
        <div className="space-y-1.5 md:col-span-2"><Label>Description</Label><Input value={e2.og_description || ""} onChange={(ev) => setE2({ ...e2, og_description: ev.target.value })} /></div>
        <div className="space-y-1.5 md:col-span-2"><Label>Image URL</Label><Input value={e2.og_image || ""} onChange={(ev) => setE2({ ...e2, og_image: ev.target.value })} className="font-mono text-xs" /></div>
        <div className="flex justify-end md:col-span-2">
          <Button onClick={async () => {
            try {
              await PATCH("/panel/config/embed", e2);
              toast.success("Embeds saved");
            } catch (err: any) { toast.error(err.message); }
          }}><Save /> Save embeds</Button>
        </div>
      </CardContent>
    </Card>
  );
}
