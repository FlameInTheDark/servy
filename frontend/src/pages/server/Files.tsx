import { useState } from "react";
import { toast } from "sonner";
import {
  Folder, FileText, ChevronRight, Upload, Download, Trash2, Pencil,
  FolderPlus, FilePlus, FolderUp, RefreshCw, HardDriveDownload,
} from "lucide-react";
import { GET, api, FileEntry } from "@/api/client";
import { Button } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/overlays";
import { formatBytes, timeAgo } from "@/lib/utils";

function FileIcon({ entry }: { entry: FileEntry }) {
  if (entry.dir) return <Folder className="h-4 w-4 text-primary/80" />;
  return <FileText className="h-4 w-4 text-muted-foreground" />;
}

export default function Files({ serverId }: { serverId: string }) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<{ path: string; content: string; isNew: boolean } | null>(null);
  const [uploadInfo, setUploadInfo] = useState<{ name: string; progress: number } | null>(null);

  const load = async (p = path) => {
    setLoading(true);
    try {
      const res = await GET<any>(`/servers/${serverId}/files?path=${encodeURIComponent(p)}`);
      setEntries(res.entries || []);
    } catch (e: any) {
      toast.error(e.message);
    } finally {
      setLoading(false);
    }
  };
  useState(() => { load(""); });

  const crumbs = path ? path.split("/") : [];

  const openFile = async (rel: string) => {
    try {
      const res = await GET<any>(`/servers/${serverId}/files/${rel.split("/").map(encodeURIComponent).join("/")}`);
      setEditing({ path: rel, content: res.content, isNew: false });
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const saveFile = async () => {
    if (!editing) return;
    try {
      await api("PUT", `/servers/${serverId}/files/${editing.path.split("/").map(encodeURIComponent).join("/")}`,
        { content: editing.content });
      toast.success("Saved");
      setEditing(null);
      load();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const del = async (rel: string) => {
    if (!confirm(`Delete ${rel}?`)) return;
    try {
      await api("DELETE", `/servers/${serverId}/files/${rel.split("/").map(encodeURIComponent).join("/")}`);
      load();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const createEntry = async (name: string, kind: "file" | "dir") => {
    const target = path ? `${path}/${name}` : name;
    try {
      await api("POST", `/servers/${serverId}/files/${target.split("/").map(encodeURIComponent).join("/")}?action=create&kind=${kind}`, {});
      load();
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  const uploadFile = async (file: File) => {
    const target = path ? `${path}/${file.name}` : file.name;
    const CHUNK = 512 * 1024;
    const total = Math.ceil(file.size / CHUNK) || 1;
    const uploadId = Math.random().toString(36).slice(2);
    setUploadInfo({ name: file.name, progress: 0 });
    try {
      for (let i = 0; i < total; i++) {
        const chunk = file.slice(i * CHUNK, (i + 1) * CHUNK);
        const res = await fetch(
          `/api/v1/servers/${serverId}/files/${target.split("/").map(encodeURIComponent).join("/")}?action=upload&upload_id=${uploadId}&chunk=${i}&total=${total}`,
          {
            method: "POST",
            headers: { Authorization: `Bearer ${localStorage.getItem("servy_token")}`, "Content-Type": "application/octet-stream" },
            body: chunk,
          }
        );
        // Surface failures instead of showing a fake "Uploaded" toast.
        const j = await res.json().catch(() => null);
        if (!res.ok || !j || j.status !== "ok") {
          throw new Error(j?.error_data || `upload failed (HTTP ${res.status})`);
        }
        setUploadInfo({ name: file.name, progress: Math.round(((i + 1) / total) * 100) });
      }
      toast.success(`Uploaded ${file.name}`);
      load();
    } catch (e: any) {
      toast.error(e?.message || "upload failed");
    } finally {
      setUploadInfo(null);
    }
  };

  const download = (rel: string) => {
    const token = localStorage.getItem("servy_token");
    window.open(`/api/v1/servers/${serverId}/files/${rel.split("/").map(encodeURIComponent).join("/")}?action=download&token=${token}`, "_blank");
  };

  return (
    <div className="space-y-3">
      {/* toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => { setPath(""); load(""); }}>
          <HardDriveDownload /> root
        </Button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="h-3 w-3 text-muted-foreground" />
            <button
              className="text-sm text-muted-foreground hover:text-foreground"
              onClick={() => { const p = crumbs.slice(0, i + 1).join("/"); setPath(p); load(p); }}
            >{c}</button>
          </span>
        ))}
        <div className="ml-auto flex gap-1.5">
          <Button size="sm" variant="outline" onClick={() => { const n = prompt("New folder name"); if (n) createEntry(n, "dir"); }}>
            <FolderPlus /> Folder
          </Button>
          <Button size="sm" variant="outline" onClick={() => { const n = prompt("New file name"); if (n) createEntry(n, "file"); }}>
            <FilePlus /> File
          </Button>
          <label className="inline-flex">
            <input type="file" className="hidden" onChange={(e) => e.target.files?.[0] && uploadFile(e.target.files[0])} />
            <span className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-xs font-medium cursor-pointer hover:bg-accent">
              <Upload /> Upload
            </span>
          </label>
          <Button size="sm" variant="outline" onClick={() => load()}><RefreshCw /></Button>
        </div>
      </div>

      {uploadInfo && (
        <div className="rounded-md border p-2 text-xs">
          Uploading {uploadInfo.name}… {uploadInfo.progress}%
          <div className="mt-1 h-1.5 rounded-full bg-muted overflow-hidden">
            <div className="h-full bg-primary transition-all" style={{ width: `${uploadInfo.progress}%` }} />
          </div>
        </div>
      )}

      {/* listing */}
      <div className="rounded-xl border bg-card overflow-hidden">
        {path && (
          <button
            className="flex w-full items-center gap-3 border-b px-4 py-2.5 text-sm hover:bg-accent/50"
            onClick={() => { const parent = crumbs.slice(0, -1).join("/"); setPath(parent); load(parent); }}
          >
            <FolderUp className="h-4 w-4" /> ..
          </button>
        )}
        {loading ? (
          <div className="p-8 text-center text-sm text-muted-foreground">Loading…</div>
        ) : entries.length === 0 ? (
          <div className="p-8 text-center text-sm text-muted-foreground">Empty directory</div>
        ) : (
          entries.map((e) => {
            const rel = path ? `${path}/${e.name}` : e.name;
            return (
              <div key={e.name} className="group flex items-center gap-3 border-b last:border-0 px-4 py-2.5 hover:bg-accent/40">
                <FileIcon entry={e} />
                <button
                  className="flex-1 truncate text-left text-sm"
                  onClick={() => {
                    if (e.dir) { setPath(rel); load(rel); }
                    else if (/\.(txt|json|yml|yaml|properties|log|ini|cfg|conf|xml|sh|bat|toml)$/i.test(e.name) && e.size < 2_000_000) openFile(rel);
                    else download(rel);
                  }}
                >{e.name}</button>
                <span className="hidden w-20 text-right text-xs text-muted-foreground sm:block">{formatBytes(e.size)}</span>
                <span className="hidden w-24 text-right text-xs text-muted-foreground md:block">{timeAgo(e.modified)}</span>
                <div className="flex gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
                  <Button size="icon-sm" variant="ghost" title="Download" onClick={() => download(rel)}>
                    <Download className="h-3.5 w-3.5" />
                  </Button>
                  <Button size="icon-sm" variant="ghost" title="Edit" onClick={() => openFile(rel)}>
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>
                  <Button size="icon-sm" variant="ghost" title="Delete" onClick={() => del(rel)}>
                    <Trash2 className="h-3.5 w-3.5 text-destructive" />
                  </Button>
                </div>
              </div>
            );
          })
        )}
      </div>

      {/* editor dialog */}
      <Dialog open={!!editing} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle className="font-mono text-sm">{editing?.path}</DialogTitle>
          </DialogHeader>
          <textarea
            className="h-[55vh] w-full rounded-md border bg-black/40 p-3 font-mono text-xs focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            value={editing?.content || ""}
            onChange={(e) => setEditing(editing && { ...editing, content: e.target.value })}
            spellCheck={false}
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button onClick={saveFile}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
