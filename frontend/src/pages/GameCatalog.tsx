import { useMemo, useState } from "react";
import { toast } from "sonner";
import { Check, Code2, FilePlus2, Gamepad2, RefreshCw, Search, ShieldAlert, Trash2 } from "lucide-react";
import { api, SteamGame } from "@/api/client";
import { useApi } from "@/hooks/useApi";
import { useAuth } from "@/hooks/AuthContext";
import { Badge, Button, Card, CardContent, CardDescription, CardHeader, CardTitle, Input, Skeleton, Textarea } from "@/components/ui/primitives";

const starterManifest: SteamGame = {
  app_id: 999999,
  slug: "my-dedicated-server",
  name: "My Dedicated Server",
  type: "steam_cmd",
  run_command: "./server -queryport {QUERY_PORT} -savedir {SAVE_DIR}",
  executable: "server",
  stop_command: "quit",
  port: 27015,
  port_name: "game",
  expose: [{ proto: "udp" }, { offset: 1, proto: "udp", optional: true }],
  save_dirs: ["saves"],
  backup_dirs: ["saves"],
  config_files: ["server.cfg"],
  launch_options: [
    { key: "server_name", label: "Server name", type: "text", source: "server_name", flag: "-name" },
    { key: "port", label: "Game port", type: "number", source: "port", flag: "-port", min: 1, max: 65535 },
  ],
};

export default function GameCatalogPage() {
  const { user } = useAuth();
  const { data: manifests, loading, error, refetch } = useApi<SteamGame[]>("/panel/steam/catalog/custom");
  const [draft, setDraft] = useState(() => JSON.stringify(starterManifest, null, 2));
  const [editingSlug, setEditingSlug] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<"validate" | "save" | "delete" | null>(null);
  const [validation, setValidation] = useState<SteamGame | null>(null);

  const filtered = useMemo(() => (manifests || []).filter((game) => {
    const q = query.trim().toLowerCase();
    return !q || game.name.toLowerCase().includes(q) || game.slug.toLowerCase().includes(q) || String(game.app_id).includes(q);
  }), [manifests, query]);

  if (!user?.superuser) {
    return (
      <div className="mx-auto max-w-3xl p-6">
        <Card>
          <CardContent className="flex items-start gap-3 p-6">
            <ShieldAlert className="mt-0.5 h-5 w-5 text-destructive" />
            <div>
              <h1 className="font-semibold">Administrator access required</h1>
              <p className="mt-1 text-sm text-muted-foreground">Only panel administrators can manage game-server manifests.</p>
            </div>
          </CardContent>
        </Card>
      </div>
    );
  }

  const selectManifest = (game: SteamGame) => {
    const { custom: _custom, ...manifest } = game;
    setEditingSlug(game.slug);
    setDraft(JSON.stringify(manifest, null, 2));
    setValidation(null);
  };

  const newManifest = () => {
    setEditingSlug(null);
    setDraft(JSON.stringify(starterManifest, null, 2));
    setValidation(null);
  };

  const validate = async () => {
    setBusy("validate");
    setValidation(null);
    try {
      const parsed = await api<SteamGame>("POST", "/panel/steam/catalog/custom/validate", { manifest: draft });
      setValidation(parsed);
      toast.success(`Manifest is valid: ${parsed.name}`);
    } catch (e: any) {
      toast.error(e.message || "Manifest validation failed");
    } finally {
      setBusy(null);
    }
  };

  const save = async () => {
    setBusy("save");
    try {
      const saved = await api<SteamGame>("POST", "/panel/steam/catalog/custom", { manifest: draft });
      setEditingSlug(saved.slug);
      const { custom: _custom, ...manifest } = saved;
      setDraft(JSON.stringify(manifest, null, 2));
      setValidation(saved);
      await refetch();
      toast.success(`Saved ${saved.name}; it is now available in Create Server.`);
    } catch (e: any) {
      toast.error(e.message || "Could not save manifest");
    } finally {
      setBusy(null);
    }
  };

  const remove = async (game: SteamGame) => {
    if (!window.confirm(`Remove the custom manifest for ${game.name}? Existing servers will not be changed.`)) return;
    setBusy("delete");
    try {
      await api("DELETE", `/panel/steam/catalog/custom/${encodeURIComponent(game.slug)}`);
      if (editingSlug === game.slug) newManifest();
      await refetch();
      toast.success(`Removed ${game.name} from the game catalog.`);
    } catch (e: any) {
      toast.error(e.message || "Could not remove manifest");
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="p-4 sm:p-6">
      <div className="mx-auto max-w-7xl space-y-6">
        <header className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">Game manifests</h1>
            <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
              Add SteamCMD server definitions to this panel's active catalog. Saved manifests take effect immediately and survive restarts; they are templates for new servers, not changes to existing installs.
            </p>
          </div>
          <Button onClick={newManifest}><FilePlus2 /> New manifest</Button>
        </header>

        <div className="grid items-start gap-5 xl:grid-cols-[minmax(260px,0.8fr)_minmax(0,1.7fr)]">
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between gap-3">
                <div>
                  <CardTitle className="text-base">Custom catalog</CardTitle>
                  <CardDescription className="mt-1">{manifests?.length ?? 0} custom {manifests?.length === 1 ? "definition" : "definitions"}</CardDescription>
                </div>
                <Button variant="ghost" size="icon-sm" title="Refresh catalog" onClick={() => refetch()}>
                  <RefreshCw className="h-4 w-4" />
                </Button>
              </div>
              <div className="relative pt-2">
                <Search className="absolute left-2.5 top-4.5 h-4 w-4 text-muted-foreground" />
                <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search name, slug, or app ID" className="pl-8" />
              </div>
            </CardHeader>
            <CardContent className="space-y-2">
              {loading && !manifests ? (
                <div className="space-y-2"><Skeleton className="h-16" /><Skeleton className="h-16" /><Skeleton className="h-16" /></div>
              ) : error ? (
                <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
              ) : filtered.length === 0 ? (
                <div className="rounded-lg border border-dashed p-5 text-center">
                  <Gamepad2 className="mx-auto h-7 w-7 text-muted-foreground" />
                  <p className="mt-2 text-sm font-medium">{query ? "No matches" : "No custom manifests yet"}</p>
                  <p className="mt-1 text-xs text-muted-foreground">Built-in games are preserved separately. Add your first custom game definition here.</p>
                </div>
              ) : filtered.map((game) => (
                <div key={game.slug} className={`rounded-lg border p-3 transition-colors ${editingSlug === game.slug ? "border-primary bg-accent/50" : "hover:bg-accent/30"}`}>
                  <button className="w-full text-left" onClick={() => selectManifest(game)}>
                    <span className="flex items-center justify-between gap-2">
                      <span className="truncate font-medium">{game.name}</span>
                      <Badge variant="muted" className="shrink-0 font-mono">{game.app_id}</Badge>
                    </span>
                    <span className="mt-1 block truncate font-mono text-xs text-muted-foreground">{game.slug} · port {game.port}/{game.expose?.[0]?.proto || "tcp"}</span>
                  </button>
                  <div className="mt-2 flex justify-end">
                    <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" disabled={busy !== null} onClick={() => remove(game)}>
                      <Trash2 /> Remove
                    </Button>
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base"><Code2 className="h-4 w-4" />{editingSlug ? `Edit ${editingSlug}` : "Manifest editor"}</CardTitle>
              <CardDescription>
                Paste YAML or JSON. Validate before saving. The server downloads these files using SteamCMD and runs the command you specify.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <Textarea
                value={draft}
                onChange={(e) => { setDraft(e.target.value); setValidation(null); }}
                spellCheck={false}
                aria-label="SteamCMD game manifest YAML or JSON"
                className="min-h-[440px] resize-y font-mono text-xs leading-5"
              />
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0 text-xs text-muted-foreground" aria-live="polite">
                  {validation ? <span className="flex items-center gap-1.5 text-success"><Check className="h-3.5 w-3.5" /> Valid: {validation.name} · Steam app {validation.app_id} · port {validation.port}</span> : "Custom definitions are saved in config/steamgames.json."}
                </div>
                <div className="flex gap-2">
                  <Button variant="outline" onClick={validate} disabled={busy !== null || !draft.trim()}>{busy === "validate" ? "Validating…" : "Validate"}</Button>
                  <Button onClick={save} disabled={busy !== null || !draft.trim()}>{busy === "save" ? "Saving…" : editingSlug ? "Save changes" : "Save manifest"}</Button>
                </div>
              </div>
              <div className="rounded-md border bg-muted/30 p-3 text-xs leading-relaxed text-muted-foreground">
                <b className="text-foreground">Only add manifests you trust.</b> A manifest supplies a command that Servy executes when the server starts. Port declarations affect Docker-backed servers; host firewall and router rules may still need configuration.
                {editingSlug && <span> Changing a saved slug creates a new catalog entry; remove the old one if you no longer need it.</span>}
              </div>
            </CardContent>
          </Card>
        </div>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">How custom manifests work</CardTitle>
            <CardDescription>They become available in the SteamCMD game picker immediately; no image rebuild or panel restart is required.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm text-muted-foreground md:grid-cols-3">
            <div><b className="text-foreground">1. Describe the server.</b><br />Steam App ID, slug, executable, run/stop commands, and default port.</div>
            <div><b className="text-foreground">2. Declare launch and config needs.</b><br />Use <code className="rounded bg-muted px-1">launch_options</code> for typed parameters, <code className="rounded bg-muted px-1">config_editors</code> for labeled/list editors, and <code className="rounded bg-muted px-1">expose</code> for Docker ports. Backup selection and Workshop actions are not wired yet.</div>
            <div><b className="text-foreground">3. Validate and save.</b><br />The overlay is local to this panel's data directory. New servers keep the manifest snapshot used at creation; later edits affect future servers only.</div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
