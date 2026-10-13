import { useEffect, useState } from "react";
import { toast } from "sonner";
import { FileCog, Code2, Eye, Save, CheckCircle2, Plus, Trash2 } from "lucide-react";
import { GET, PUT } from "@/api/client";
import { Button, Input, Switch, Badge, Skeleton } from "@/components/ui/primitives";
import { SimpleSelect, Tabs, TabsList, TabsTrigger } from "@/components/ui/overlays";
import { cn } from "@/lib/utils";

interface FormField {
  key: string; value: string; type: string;
  options?: { value: string; label: string }[];
  label?: string; help?: string; section?: string;
}
interface FormSection { id: string; label: string; fields: FormField[] }
interface ConfigFileInfo { label?: string; description?: string; editor?: "lines" }

export default function ConfigEditor({ serverId, file, configInfo }: { serverId: string; file: string; configInfo?: ConfigFileInfo }) {
  const [sections, setSections] = useState<FormSection[]>([]);
  const [raw, setRaw] = useState("");
  const [listEntries, setListEntries] = useState<string[]>([]);
  const [mode, setMode] = useState<"visual" | "raw">("visual");
  const [loading, setLoading] = useState(true);
  const [dirty, setDirty] = useState(false);
  const [jsonError, setJsonError] = useState<string | null>(null);
  const configEndpoint = `/servers/${serverId}/config-file?path=${encodeURIComponent(file)}`;
  const isLines = configInfo?.editor === "lines";

  useEffect(() => {
    setLoading(true);
    GET<any>(configEndpoint)
      .then((res) => {
        if (res.form) setSections(res.form.sections || []);
        const content = res.content || "";
        setRaw(content);
        setListEntries(content.replace(/\r\n?/g, "\n").split("\n").filter((line: string) => line.trim() !== ""));
        setMode(res.form ? "visual" : "raw");
        setDirty(false);
      })
      .catch((e) => toast.error(e.message))
      .finally(() => setLoading(false));
  }, [serverId, file]);

  const setField = (sectionId: string, key: string, value: string) => {
    setSections((secs) =>
      secs.map((s) =>
        s.id !== sectionId ? s : { ...s, fields: s.fields.map((f) => (f.key !== key ? f : { ...f, value })) }
      )
    );
    setDirty(true);
  };

  const save = async () => {
    if (isLines) {
      const content = listEntries.map((entry) => entry.trim()).filter(Boolean).join("\n");
      try {
        await PUT(configEndpoint, { mode: "raw", content: content ? `${content}\n` : "" });
        setRaw(content ? `${content}\n` : "");
        toast.success("Saved list");
        setDirty(false);
      } catch (e: any) {
        toast.error(e.message);
      }
      return;
    }
    if (mode === "visual") {
      const values: Record<string, string> = {};
      sections.forEach((s) => s.fields.forEach((f) => (values[f.key] = f.value)));
      try {
        await PUT(configEndpoint, { mode: "visual", values });
        toast.success("Saved (visual)");
        setDirty(false);
      } catch (e: any) {
        toast.error(e.message);
      }
    } else {
      if (file.endsWith(".json")) {
        try {
          JSON.parse(raw);
          setJsonError(null);
        } catch (e: any) {
          setJsonError(e.message);
          toast.error("Invalid JSON — not saved");
          return;
        }
      }
      try {
        await PUT(configEndpoint, {
          mode: file.endsWith(".json") ? "json" : "raw", content: raw,
        });
        toast.success("Saved");
        setDirty(false);
      } catch (e: any) {
        toast.error(e.message);
      }
    }
  };

  const isProperties = file.toLowerCase().endsWith(".properties");

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div className="flex min-w-0 items-center gap-2">
          <FileCog className="h-5 w-5 shrink-0 text-primary" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium">{configInfo?.label || file}</div>
            {configInfo?.label && <div className="truncate font-mono text-xs text-muted-foreground">{file}</div>}
          </div>
          {dirty && <Badge variant="warning">unsaved</Badge>}
        </div>
        <div className="flex items-center gap-2">
          {isProperties && (
            <Tabs value={mode} onValueChange={(v) => setMode(v as any)}>
              <TabsList>
                <TabsTrigger value="visual"><Eye className="h-3.5 w-3.5" /> Visual</TabsTrigger>
                <TabsTrigger value="raw"><Code2 className="h-3.5 w-3.5" /> Raw</TabsTrigger>
              </TabsList>
            </Tabs>
          )}
          <Button size="sm" onClick={save} disabled={!dirty}>
            <Save /> Save
          </Button>
        </div>
      </div>

      {loading ? (
        <Skeleton className="h-96" />
      ) : isLines ? (
        <div className="space-y-3">
          {configInfo?.description && <p className="text-sm text-muted-foreground">{configInfo.description}</p>}
          {listEntries.length === 0 ? (
            <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No entries yet. Add a player ID below.</div>
          ) : (
            <div className="space-y-2">
              {listEntries.map((entry, index) => (
                <div key={`${index}-${entry}`} className="flex items-center gap-2">
                  <span className="w-7 shrink-0 text-right text-xs text-muted-foreground">{index + 1}</span>
                  <Input
                    value={entry}
                    aria-label={`List entry ${index + 1}`}
                    onChange={(e) => {
                      setListEntries((entries) => entries.map((current, i) => i === index ? e.target.value : current));
                      setDirty(true);
                    }}
                    className="font-mono text-sm"
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Remove list entry ${index + 1}`}
                    onClick={() => {
                      setListEntries((entries) => entries.filter((_, i) => i !== index));
                      setDirty(true);
                    }}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}
          <Button variant="outline" size="sm" onClick={() => { setListEntries((entries) => [...entries, ""]); setDirty(true); }}>
            <Plus className="h-4 w-4" /> Add entry
          </Button>
          <p className="text-xs text-muted-foreground">One value per line. Save when finished; the file itself is not changed until you do.</p>
        </div>
      ) : mode === "visual" && sections.length > 0 ? (
        <div className="space-y-5">
          {sections.map((sec) => (
            <div key={sec.id} className="rounded-xl border bg-card">
              <div className="border-b px-4 py-2.5 text-sm font-semibold">{sec.label}</div>
              <div className="grid gap-4 p-4 md:grid-cols-2">
                {sec.fields.map((f) => (
                  <div key={f.key} className="space-y-1.5">
                    <label className="text-xs font-medium text-muted-foreground" title={f.key}>
                      {f.label || f.key}
                    </label>
                    {f.type === "boolean" ? (
                      <div className="flex h-9 items-center">
                        <Switch checked={f.value === "true"} onCheckedChange={(v) => setField(sec.id, f.key, v ? "true" : "false")} />
                      </div>
                    ) : f.type === "select" && f.options?.length ? (
                      <SimpleSelect value={f.value} onValueChange={(v) => setField(sec.id, f.key, v)}
                        options={f.options.map((o) => ({ value: o.value, label: o.label || o.value }))} />
                    ) : (
                      <Input
                        type={f.type === "integer" || f.type === "float" ? "number" : "text"}
                        value={f.value}
                        onChange={(e) => setField(sec.id, f.key, e.target.value)}
                        className="font-mono text-xs"
                      />
                    )}
                    {f.help && <p className="text-[11px] text-muted-foreground/70">{f.help}</p>}
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="space-y-2">
          <textarea
            className="h-[60vh] w-full rounded-xl border bg-black/40 p-4 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            value={raw}
            onChange={(e) => { setRaw(e.target.value); setDirty(true); if (file.endsWith(".json")) {
              try { JSON.parse(e.target.value); setJsonError(null); } catch (err: any) { setJsonError(err.message); }
            }}}
            spellCheck={false}
          />
          {file.endsWith(".json") && (
            <div className={cn("flex items-center gap-2 text-xs", jsonError ? "text-destructive" : "text-success")}>
              {!jsonError && <CheckCircle2 className="h-3.5 w-3.5" />}
              {jsonError ? `Invalid JSON: ${jsonError}` : "Valid JSON"}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
