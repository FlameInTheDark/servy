import { useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronRight,
  Code2,
  FilePlus2,
  Gamepad2,
  LockKeyhole,
  Plus,
  RefreshCw,
  Search,
  ShieldAlert,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { api, SteamGame } from "@/api/client";
import { useApi } from "@/hooks/useApi";
import { useAuth } from "@/hooks/AuthContext";
import { Badge, Button, Card, CardContent, Input, Skeleton, Textarea } from "@/components/ui/primitives";
import ManifestForm from "@/pages/game-catalog/ManifestForm";

const starterManifest: SteamGame = {
  app_id: 999999,
  slug: "my-dedicated-server",
  name: "My Dedicated Server",
  type: "steam_cmd",
  run_command: "./server -port {PORT} -savedir {SAVE_DIR}",
  executable: "server",
  stop_command: "quit",
  port: 27015,
  port_name: "game",
  expose: [{ proto: "udp" }],
  save_dirs: ["saves"],
  backup_dirs: ["saves"],
  launch_options: [
    { key: "server_name", label: "Server name", type: "text", source: "server_name", flag: "-name" },
    { key: "port", label: "Game port", type: "number", source: "port", flag: "-port", min: 1, max: 65535 },
  ],
};

const starterDraft = JSON.stringify(starterManifest, null, 2);

type EditorMode = "form" | "source";
type BusyAction = "validate" | "save" | "delete" | null;

function withoutCatalogFields(game: SteamGame): SteamGame {
  const { custom: _custom, ...manifest } = game;
  return manifest;
}

function parseJSON(text: string): SteamGame | null {
  try {
    const value = JSON.parse(text);
    return value && typeof value === "object" && !Array.isArray(value) ? value as SteamGame : null;
  } catch {
    return null;
  }
}

function LivePreview({ game }: { game: SteamGame }) {
  const queryPort = game.query_port || (game.port > 0 && game.port < 65535 ? game.port + 1 : undefined);
  const options = game.launch_options || [];
  const configCount = (game.config_files?.length || 0) + (game.config_editors?.length || 0);

  return (
    <aside className="space-y-3 xl:sticky xl:top-28">
      <Card className="overflow-hidden">
        <div className="border-b bg-gradient-to-br from-primary/10 via-card to-card p-4">
          <div className="flex items-center gap-2 text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
            <span className="h-1.5 w-1.5 rounded-full bg-success" /> Live preview
          </div>
          <div className="mt-3 flex items-start gap-3">
            <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl border bg-background/80 text-primary shadow-sm">
              <Gamepad2 className="h-5 w-5" />
            </div>
            <div className="min-w-0">
              <h3 className="truncate text-base font-semibold">{game.name || "Untitled game"}</h3>
              <p className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">{game.slug || "your-game-slug"}</p>
            </div>
          </div>
          <div className="mt-3 flex flex-wrap gap-1.5">
            <Badge variant="muted">App {game.app_id || "—"}</Badge>
            {game.windows_only ? <Badge variant="warning">Windows / Wine</Badge> : <Badge variant="outline">{game.platform || "Linux"}</Badge>}
          </div>
        </div>

        <CardContent className="space-y-4 p-4 pt-4">
          <div className="grid grid-cols-2 gap-2">
            <div className="rounded-lg border bg-muted/20 p-2.5">
              <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Game port</div>
              <div className="mt-1 font-mono text-sm font-semibold">{game.port || "—"}<span className="ml-1 text-[10px] font-normal text-muted-foreground">{game.expose?.[0]?.proto || "tcp"}</span></div>
            </div>
            <div className="rounded-lg border bg-muted/20 p-2.5">
              <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Query port</div>
              <div className="mt-1 font-mono text-sm font-semibold">{queryPort || "—"}<span className="ml-1 text-[10px] font-normal text-muted-foreground">{game.query_port ? "explicit" : "auto"}</span></div>
            </div>
            <div className="rounded-lg border bg-muted/20 p-2.5">
              <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Parameters</div>
              <div className="mt-1 text-sm font-semibold">{options.length}</div>
            </div>
            <div className="rounded-lg border bg-muted/20 p-2.5">
              <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Config entries</div>
              <div className="mt-1 text-sm font-semibold">{configCount}</div>
            </div>
          </div>

          <div>
            <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Start command</div>
            <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-lg border bg-black/30 p-2.5 font-mono text-[10px] leading-relaxed text-foreground/90">{game.run_command || "Add a run command in Startup."}</pre>
          </div>

          {options.length > 0 && (
            <div>
              <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">Creation form fields</div>
              <div className="space-y-1.5">
                {options.slice(0, 5).map((option) => (
                  <div key={option.key} className="flex items-center justify-between gap-2 rounded-md bg-muted/30 px-2.5 py-1.5">
                    <span className="truncate text-xs">{option.label || option.key}</span>
                    <span className="shrink-0 text-[10px] text-muted-foreground">{option.source ? "automatic" : option.type}</span>
                  </div>
                ))}
                {options.length > 5 && <p className="px-1 text-[10px] text-muted-foreground">and {options.length - 5} more…</p>}
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <div className="rounded-xl border border-amber-500/20 bg-amber-500/5 p-3.5">
        <div className="flex items-start gap-2.5">
          <LockKeyhole className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <div>
            <div className="text-xs font-semibold">Trusted administrator content</div>
            <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">Commands and runtime settings are executed by Servy. Saved catalog edits affect new servers only; existing servers keep their creation snapshot.</p>
          </div>
        </div>
      </div>
    </aside>
  );
}

export default function GameCatalogPage() {
  const { user } = useAuth();
  const { data: manifests, loading, error, refetch } = useApi<SteamGame[]>("/panel/steam/catalog/custom");
  const [draft, setDraft] = useState(starterDraft);
  const [savedDraft, setSavedDraft] = useState(starterDraft);
  const [editingSlug, setEditingSlug] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<BusyAction>(null);
  const [validation, setValidation] = useState<SteamGame | null>(null);
  const [serverError, setServerError] = useState<string | null>(null);
  const [editorMode, setEditorMode] = useState<EditorMode>("form");

  const filtered = useMemo(() => (manifests || []).filter((game) => {
    const q = query.trim().toLowerCase();
    return !q || game.name.toLowerCase().includes(q) || game.slug.toLowerCase().includes(q) || String(game.app_id).includes(q);
  }), [manifests, query]);
  const parsedGame = useMemo(() => parseJSON(draft), [draft]);
  const dirty = draft !== savedDraft;

  if (!user?.superuser) {
    return (
      <div className="mx-auto max-w-3xl p-6">
        <Card>
          <CardContent className="flex items-start gap-3 p-6">
            <ShieldAlert className="mt-0.5 h-5 w-5 text-destructive" />
            <div>
              <h1 className="font-semibold">Administrator access required</h1>
              <p className="mt-1 text-sm text-muted-foreground">Only panel administrators can manage SteamCMD game catalog manifests.</p>
            </div>
          </CardContent>
        </Card>
      </div>
    );
  }

  const confirmDiscard = () => !dirty || window.confirm("Discard unsaved manifest changes?");

  const selectManifest = (game: SteamGame) => {
    if (!confirmDiscard()) return;
    const json = JSON.stringify(withoutCatalogFields(game), null, 2);
    setEditingSlug(game.slug);
    setDraft(json);
    setSavedDraft(json);
    setValidation(null);
    setServerError(null);
    setEditorMode("form");
  };

  const newManifest = () => {
    if (!confirmDiscard()) return;
    setEditingSlug(null);
    setDraft(starterDraft);
    setSavedDraft(starterDraft);
    setValidation(null);
    setServerError(null);
    setEditorMode("form");
  };

  const updateDraft = (value: string) => {
    setDraft(value);
    setValidation(null);
    setServerError(null);
  };

  const updateGame = (game: SteamGame) => {
    updateDraft(JSON.stringify(withoutCatalogFields(game), null, 2));
  };

  const changeEditorMode = (next: EditorMode) => {
    if (next === "form" && !parsedGame) {
      if (validation) {
        updateDraft(JSON.stringify(withoutCatalogFields(validation), null, 2));
      } else {
        setServerError("The visual form needs valid JSON. Validate YAML first to convert it, or fix the source text.");
        return;
      }
    }
    setServerError(null);
    setEditorMode(next);
  };

  const validate = async () => {
    setBusy("validate");
    setValidation(null);
    setServerError(null);
    try {
      const parsed = await api<SteamGame>("POST", "/panel/steam/catalog/custom/validate", { manifest: draft });
      setValidation(parsed);
      toast.success(`Manifest is valid: ${parsed.name}`);
    } catch (e: any) {
      const message = e.message || "Manifest validation failed";
      setServerError(message);
      toast.error(message);
    } finally {
      setBusy(null);
    }
  };

  const save = async () => {
    setBusy("save");
    setServerError(null);
    try {
      const saved = await api<SteamGame>("POST", "/panel/steam/catalog/custom", { manifest: draft });
      const canonical = JSON.stringify(withoutCatalogFields(saved), null, 2);
      setEditingSlug(saved.slug);
      setDraft(canonical);
      setSavedDraft(canonical);
      setValidation(saved);
      setEditorMode("form");
      await refetch();
      toast.success(editingSlug && saved.slug !== editingSlug
        ? `Saved as ${saved.slug}. The previous ${editingSlug} entry is unchanged.`
        : `Saved ${saved.name}; it is ready for new servers.`);
    } catch (e: any) {
      const message = e.message || "Could not save manifest";
      setServerError(message);
      toast.error(message);
    } finally {
      setBusy(null);
    }
  };

  const remove = async (game: SteamGame) => {
    if (!window.confirm(`Remove ${game.name} from the custom catalog? Existing servers will not be changed.`)) return;
    setBusy("delete");
    try {
      await api("DELETE", `/panel/steam/catalog/custom/${encodeURIComponent(game.slug)}`);
      if (editingSlug === game.slug) {
        setEditingSlug(null);
        setDraft(starterDraft);
        setSavedDraft(starterDraft);
        setValidation(null);
        setServerError(null);
      }
      await refetch();
      toast.success(`Removed ${game.name} from the catalog.`);
    } catch (e: any) {
      toast.error(e.message || "Could not remove manifest");
    } finally {
      setBusy(null);
    }
  };

  const listContent = loading && !manifests ? (
    <div className="space-y-2 p-3"><Skeleton className="h-16" /><Skeleton className="h-16" /><Skeleton className="h-16" /></div>
  ) : error ? (
    <div className="m-3 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
  ) : filtered.length === 0 ? (
    <div className="mx-3 my-4 rounded-xl border border-dashed p-5 text-center">
      <Gamepad2 className="mx-auto h-7 w-7 text-muted-foreground" />
      <p className="mt-2 text-sm font-semibold">{query ? "No matches" : "No custom games yet"}</p>
      <p className="mt-1 text-xs leading-relaxed text-muted-foreground">Built-in entries stay separate. Create a custom catalog definition for another Steam server.</p>
      {!query && <Button variant="outline" size="sm" className="mt-3" onClick={newManifest}><Plus className="h-3.5 w-3.5" /> Start a manifest</Button>}
    </div>
  ) : filtered.map((game) => (
    <div key={game.slug} className={`group mx-2 rounded-xl border transition-all ${editingSlug === game.slug ? "border-primary/50 bg-primary/[0.06] shadow-sm" : "border-transparent hover:border-border hover:bg-muted/30"}`}>
      <button className="w-full rounded-xl p-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => selectManifest(game)}>
        <span className="flex items-start justify-between gap-2">
          <span className="flex min-w-0 items-start gap-2.5">
            <span className={`mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${editingSlug === game.slug ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}><Gamepad2 className="h-4 w-4" /></span>
            <span className="min-w-0">
              <span className="block truncate text-sm font-semibold">{game.name}</span>
              <span className="mt-0.5 block truncate font-mono text-[10px] text-muted-foreground">{game.slug}</span>
            </span>
          </span>
          {editingSlug === game.slug && <ChevronRight className="mt-1 h-4 w-4 shrink-0 text-primary" />}
        </span>
        <span className="mt-3 flex flex-wrap items-center gap-1.5 pl-[42px]">
          <Badge variant="muted" className="text-[10px]">App {game.app_id}</Badge>
          <span className="text-[10px] text-muted-foreground">Port {game.port} · {(game.launch_options || []).length} params</span>
        </span>
      </button>
      <div className="flex items-center justify-between px-3 pb-2 pl-[54px]">
        <span className="text-[10px] text-muted-foreground">Custom SteamCMD catalog</span>
        <Button variant="ghost" size="icon-sm" title={`Remove ${game.name}`} aria-label={`Remove ${game.name}`} className="h-7 w-7 text-muted-foreground opacity-70 hover:text-destructive sm:opacity-0 sm:group-hover:opacity-100" disabled={busy !== null} onClick={() => remove(game)}>
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  ));

  return (
    <main className="min-h-full p-4 sm:p-6">
      <div className="mx-auto max-w-[1600px] space-y-5">
        <header className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-sm shadow-primary/20"><Gamepad2 className="h-6 w-6" /></div>
            <div className="min-w-0">
              <div className="mb-1 flex flex-wrap items-center gap-2 text-[10px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
                <span>Administration</span><ChevronRight className="h-3 w-3" /><span>SteamCMD catalog</span>
              </div>
              <h1 className="text-2xl font-bold tracking-tight sm:text-3xl">Game manifests</h1>
              <p className="mt-1 max-w-3xl text-sm text-muted-foreground">Build SteamCMD install and launch definitions. This is separate from reusable server-creation recipes.</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <div className="hidden rounded-lg border bg-card px-3 py-2 text-right sm:block">
              <div className="text-sm font-semibold tabular-nums">{manifests?.length ?? 0}</div>
              <div className="text-[10px] text-muted-foreground">custom entries</div>
            </div>
            <Button onClick={newManifest} disabled={busy !== null}><FilePlus2 className="h-4 w-4" /> New manifest</Button>
          </div>
        </header>

        <div className="grid items-start gap-4 xl:grid-cols-[290px_minmax(0,1fr)]">
          <aside className="space-y-3">
            <Card className="overflow-hidden">
              <div className="border-b p-4">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <div className="text-sm font-semibold">Custom catalog</div>
                    <div className="mt-0.5 text-xs text-muted-foreground">{manifests?.length ?? 0} {manifests?.length === 1 ? "manifest" : "manifests"}</div>
                  </div>
                  <Button variant="ghost" size="icon-sm" title="Refresh catalog" onClick={() => refetch()} disabled={busy !== null}><RefreshCw className="h-4 w-4" /></Button>
                </div>
                <div className="relative mt-3">
                  <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                  <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search name, slug, app ID" className="pl-8 text-xs" aria-label="Search custom manifests" />
                </div>
              </div>
              <div className="max-h-[calc(100vh-290px)] min-h-[180px] space-y-1 overflow-y-auto py-2">
                {listContent}
              </div>
            </Card>

            <div className="rounded-xl border bg-card/60 p-3.5">
              <div className="flex items-start gap-2.5">
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                <div>
                  <div className="text-xs font-semibold">Safe to iterate</div>
                  <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">Catalog edits update the picker immediately. Existing servers retain their original snapshot.</p>
                </div>
              </div>
            </div>
          </aside>

          <section className="min-w-0 space-y-4">
            <div className="sticky top-0 z-20 overflow-hidden rounded-2xl border bg-card/95 shadow-md shadow-black/5 backdrop-blur supports-[backdrop-filter]:bg-card/90">
              <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5 sm:px-5">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="hidden h-9 w-9 items-center justify-center rounded-xl bg-muted text-muted-foreground sm:flex"><Code2 className="h-4 w-4" /></div>
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="truncate text-sm font-semibold">{editingSlug ? `Editing ${editingSlug}` : "New game manifest"}</h2>
                      {dirty && <Badge variant="warning" className="text-[10px]">Unsaved changes</Badge>}
                      {validation && !dirty && <Badge variant="success" className="text-[10px]"><Check className="mr-1 h-3 w-3" />Valid</Badge>}
                    </div>
                    <p className="mt-0.5 text-[11px] text-muted-foreground">{editorMode === "form" ? "Guided editor · no YAML knowledge required" : "Source editor · YAML or JSON"}</p>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  {dirty && <Button variant="ghost" size="sm" onClick={() => { if (confirmDiscard()) { setDraft(savedDraft); setValidation(null); setServerError(null); } }}>Discard</Button>}
                  <Button variant="outline" size="sm" onClick={validate} disabled={busy !== null || !draft.trim()}>
                    {busy === "validate" ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
                    <span className="hidden sm:inline">{busy === "validate" ? "Validating…" : "Validate"}</span>
                  </Button>
                  <Button size="sm" onClick={save} disabled={busy !== null || !draft.trim()}>
                    {busy === "save" ? <RefreshCw className="h-3.5 w-3.5 animate-spin" /> : <Check className="h-3.5 w-3.5" />}
                    <span>{busy === "save" ? "Saving…" : editingSlug ? "Save changes" : "Save manifest"}</span>
                  </Button>
                </div>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 border-t bg-muted/15 px-4 py-2.5 sm:px-5">
                <div className="inline-flex items-center gap-1 rounded-lg border bg-background p-1" role="tablist" aria-label="Manifest editor mode">
                  <button type="button" role="tab" aria-selected={editorMode === "form"} onClick={() => changeEditorMode("form")} className={`rounded-md px-3 py-1.5 text-xs font-semibold transition ${editorMode === "form" ? "bg-primary text-primary-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"}`}>
                    Guided form
                  </button>
                  <button type="button" role="tab" aria-selected={editorMode === "source"} onClick={() => changeEditorMode("source")} className={`rounded-md px-3 py-1.5 text-xs font-semibold transition ${editorMode === "source" ? "bg-primary text-primary-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"}`}>
                    YAML / JSON
                  </button>
                </div>
                <div className="text-[11px] text-muted-foreground">
                  {validation ? <span className="inline-flex items-center gap-1.5 text-success"><Check className="h-3.5 w-3.5" /> {validation.name} · App {validation.app_id}</span> : "Review each section, then validate before saving."}
                </div>
              </div>
            </div>

            {serverError && (
              <div className="flex items-start gap-2.5 rounded-xl border border-destructive/30 bg-destructive/5 p-3.5 text-sm" role="alert">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
                <div><div className="font-semibold text-destructive">Could not complete this action</div><p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{serverError}</p></div>
              </div>
            )}

            {editorMode === "form" ? (
              parsedGame ? (
                <div className="grid min-w-0 items-start gap-4 2xl:grid-cols-[minmax(0,1fr)_280px]">
                  <ManifestForm game={parsedGame} onChange={updateGame} />
                  <LivePreview game={parsedGame} />
                </div>
              ) : (
                <div className="rounded-2xl border border-dashed bg-card p-8 text-center">
                  <Code2 className="mx-auto h-7 w-7 text-muted-foreground" />
                  <h3 className="mt-3 text-sm font-semibold">This source needs conversion</h3>
                  <p className="mx-auto mt-1 max-w-lg text-xs leading-relaxed text-muted-foreground">The guided editor works with JSON. Validate this YAML source first, then switch back to convert it into an editable form.</p>
                  <Button variant="outline" size="sm" className="mt-4" onClick={validate} disabled={busy !== null}><CheckCircle2 className="h-3.5 w-3.5" /> Validate YAML</Button>
                </div>
              )
            ) : (
              <div className="overflow-hidden rounded-2xl border bg-[#0b1017] shadow-sm">
                <div className="flex items-center justify-between border-b border-white/10 bg-white/[0.03] px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <span className="h-2 w-2 rounded-full bg-red-400/80" /><span className="h-2 w-2 rounded-full bg-amber-300/80" /><span className="h-2 w-2 rounded-full bg-emerald-400/80" />
                    <span className="ml-2 font-mono text-[11px] text-slate-400">game-manifest.yaml</span>
                  </div>
                  <Badge variant="muted" className="border border-white/10 bg-white/5 text-[10px] text-slate-300">Source</Badge>
                </div>
                <Textarea
                  value={draft}
                  onChange={(event) => updateDraft(event.target.value)}
                  spellCheck={false}
                  aria-label="SteamCMD game manifest YAML or JSON"
                  className="min-h-[70vh] resize-y rounded-none border-0 bg-transparent px-5 py-4 font-mono text-xs leading-6 text-slate-100 shadow-none placeholder:text-slate-500 focus-visible:ring-0"
                />
                <div className="flex flex-wrap items-center justify-between gap-3 border-t border-white/10 px-4 py-2.5 text-[11px] text-slate-400">
                  <span>Full control for advanced edits. YAML and JSON are both accepted.</span>
                  {validation && !parsedGame && <button className="font-semibold text-primary hover:underline" onClick={() => changeEditorMode("form")}>Convert validated manifest to form</button>}
                </div>
              </div>
            )}
          </section>
        </div>
      </div>
    </main>
  );
}
