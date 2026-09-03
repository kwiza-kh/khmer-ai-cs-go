"use client";

import * as React from "react";
import { uploadKnowledge, uploadKnowledgeFile, ingestKnowledgeURL, getAcceptedFileTypes, type AcceptedFileTypes } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Loader2, UploadCloud, FileText, FileUp, X, Globe } from "lucide-react";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

interface Props {
  /** Called after a successful upload so the parent can refresh its list. */
  onUploaded: () => void;
  /** Optional trigger element; defaults to a labeled Upload button. */
  trigger?: React.ReactElement;
}

/**
 * Upload dialog with two tabs:
 *   - Paste text: title + content textarea (the original flow)
 *   - Upload file: drag-drop / browse for PDF, DOCX, TXT, MD, CSV
 *
 * The file is sent as multipart/form-data to /knowledge/upload/file, where the
 * backend extracts text + chunks + embeds it through the same RAG pipeline.
 */
export function KnowledgeUploadDialog({ onUploaded, trigger }: Props) {
  const [open, setOpen] = React.useState(false);
  const [tab, setTab] = React.useState<"text" | "file" | "url">("file");

  // Text-mode state
  const [title, setTitle] = React.useState("");
  const [content, setContent] = React.useState("");

  // File-mode state
  const [file, setFile] = React.useState<File | null>(null);
  const [category, setCategory] = React.useState("");
  const [dragOver, setDragOver] = React.useState(false);
  const inputRef = React.useRef<HTMLInputElement>(null);

  // URL-mode state (website crawl → clean text → RAG pipeline)
  const [url, setUrl] = React.useState("");

  const [uploading, setUploading] = React.useState(false);
  const [accepted, setAccepted] = React.useState<AcceptedFileTypes | null>(null);

  // Lazy-load the accepted extensions + size cap so the UI can validate before
  // the request and show helpful labels.
  React.useEffect(() => {
    if (!open) return;
    if (accepted) return;
    getAcceptedFileTypes().then(setAccepted).catch(() => setAccepted(null));
  }, [open, accepted]);

  const reset = () => {
    setTitle(""); setContent(""); setFile(null); setCategory(""); setUrl("");
    setUploading(false);
  };

  const handleClose = (next: boolean) => {
    setOpen(next);
    if (!next && !uploading) reset();
  };

  const handleFile = (f: File | null) => {
    if (!f) return;
    if (accepted) {
      const ext = "." + (f.name.split(".").pop() || "").toLowerCase();
      // Accept any extension whose label is set server-side.
      const known = Object.keys(accepted.extensions).includes(ext);
      if (!known) {
        toast.error(`Unsupported file type: ${ext}`, {
          description: `Allowed: ${Object.keys(accepted.extensions).join(", ")}`,
        });
        return;
      }
      if (f.size > accepted.max_bytes) {
        toast.error(`File too large (${(f.size / 1024 / 1024).toFixed(1)} MB)`,
          { description: `Max: ${accepted.max_megabytes} MB` });
        return;
      }
    }
    setFile(f);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const f = e.dataTransfer.files?.[0];
    if (f) handleFile(f);
  };

  const submitText = async () => {
    if (!title.trim() || !content.trim()) return;
    setUploading(true);
    try {
      await uploadKnowledge({ title, content });
      toast.success("Document queued for indexing");
      handleClose(false);
      onUploaded();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setUploading(false);
    }
  };

  const submitFile = async () => {
    if (!file) return;
    setUploading(true);
    try {
      const doc = await uploadKnowledgeFile(file, { category });
      toast.success(`"${doc.title}" queued for indexing`);
      handleClose(false);
      onUploaded();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setUploading(false);
    }
  };

  const submitUrl = async () => {
    const trimmed = url.trim();
    if (!trimmed) return;
    // Light client-side validation; the backend does the real fetch + parsing.
    try { new URL(trimmed); } catch {
      toast.error("Enter a valid URL (e.g. https://example.com/faq)");
      return;
    }
    setUploading(true);
    try {
      const doc = await ingestKnowledgeURL({
        url: trimmed, category,
        title: title.trim() || undefined,
      });
      toast.success(`"${doc.title}" queued for indexing`);
      handleClose(false);
      onUploaded();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setUploading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogTrigger render={
        trigger ?? (
          <Button size="sm" className="gap-1.5">
            <UploadCloud className="size-3.5" />Upload
          </Button>
        )
      } />
      <DialogContent className="max-w-lg">
        <DialogHeader><DialogTitle className="text-sm">Add to knowledge base</DialogTitle></DialogHeader>

        <Tabs value={tab} onValueChange={(v) => setTab((v || "file") as "text" | "file" | "url")}>
          <TabsList className="w-full">
            <TabsTrigger value="file" className="flex-1 text-xs gap-1.5"><FileUp className="size-3" />Upload file</TabsTrigger>
            <TabsTrigger value="url" className="flex-1 text-xs gap-1.5"><Globe className="size-3" />From URL</TabsTrigger>
            <TabsTrigger value="text" className="flex-1 text-xs gap-1.5"><FileText className="size-3" />Paste text</TabsTrigger>
          </TabsList>

          {/* ---------- File upload tab ---------- */}
          <TabsContent value="file" className="mt-4 space-y-3">
            <input
              ref={inputRef}
              type="file"
              accept={(accepted && Object.keys(accepted.extensions).join(",")) || ".txt,.md,.csv,.pdf,.docx"}
              onChange={(e) => handleFile(e.target.files?.[0] ?? null)}
              className="hidden"
            />

            {!file ? (
              <button
                type="button"
                onClick={() => inputRef.current?.click()}
                onDragOver={(e) => { e.preventDefault(); setDragOver(true); }}
                onDragLeave={() => setDragOver(false)}
                onDrop={handleDrop}
                className={cn(
                  "w-full rounded-md border-2 border-dashed border-border px-4 py-8 text-center transition-colors",
                  dragOver ? "border-primary bg-accent/50" : "hover:border-primary/50 hover:bg-accent/20",
                )}
              >
                <FileUp className="mx-auto size-7 text-muted-foreground mb-2" />
                <p className="text-sm font-medium">Drop a file here or click to browse</p>
                <p className="text-xs text-muted-foreground mt-1">
                  PDF · DOCX · TXT · MD · CSV
                  {accepted && ` · up to ${accepted.max_megabytes} MB`}
                </p>
              </button>
            ) : (
              <div className="rounded-md border border-border bg-card p-3 flex items-center gap-2.5">
                <div className="size-9 rounded-md bg-primary/10 flex items-center justify-center flex-shrink-0">
                  <FileText className="size-4 text-primary" />
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium truncate">{file.name}</p>
                  <p className="text-xs text-muted-foreground">{(file.size / 1024).toFixed(1)} KB</p>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setFile(null)}
                  disabled={uploading}
                  aria-label="Remove file"
                >
                  <X className="size-3.5" />
                </Button>
              </div>
            )}

            <div>
              <label className="text-xs text-muted-foreground block mb-1">Category (optional)</label>
              <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder="FAQ, policy…" className="h-8 text-xs" />
            </div>
            <p className="text-[11px] text-muted-foreground -mt-1">
              Language is auto-detected from the document content.
            </p>

            <Button onClick={submitFile} disabled={uploading || !file} className="w-full h-9 text-xs gap-1.5">
              {uploading
                ? <><Loader2 className="size-3.5 animate-spin" /> Uploading…</>
                : <><UploadCloud className="size-3.5" /> Upload file</>}
            </Button>
            <p className="text-xs text-muted-foreground text-center">
              Title is derived from the filename. Indexing starts after upload.
            </p>
          </TabsContent>

          {/* ---------- Paste text tab ---------- */}
          <TabsContent value="text" className="mt-4 space-y-3">
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Title</label>
              <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Document title" className="h-9 text-sm" />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Content</label>
              <Textarea value={content} onChange={(e) => setContent(e.target.value)} placeholder="Paste your content here…" rows={6} className="text-sm resize-none" />
            </div>
            <Button onClick={submitText} disabled={uploading || !title.trim() || !content.trim()} className="w-full h-9 text-xs gap-1.5">
              {uploading
                ? <><Loader2 className="size-3.5 animate-spin" /> Uploading…</>
                : <><UploadCloud className="size-3.5" /> Upload</>}
            </Button>
          </TabsContent>

          {/* ---------- URL ingest tab ---------- */}
          <TabsContent value="url" className="mt-4 space-y-3">
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Page URL</label>
              <Input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://example.com/faq"
                className="h-9 text-sm"
                inputMode="url"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Title (optional)</label>
              <Input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Defaults to the page’s <title> tag"
                className="h-9 text-sm"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">Category (optional)</label>
              <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder="FAQ, policy…" className="h-8 text-xs" />
            </div>
            <p className="text-[11px] text-muted-foreground -mt-1">
              Language is auto-detected from the page content.
            </p>
            <Button onClick={submitUrl} disabled={uploading || !url.trim()} className="w-full h-9 text-xs gap-1.5">
              {uploading
                ? <><Loader2 className="size-3.5 animate-spin" /> Fetching…</>
                : <><Globe className="size-3.5" /> Ingest page</>}
            </Button>
            <p className="text-xs text-muted-foreground text-center">
              The page is fetched and prepared before it is queued for indexing.
            </p>
          </TabsContent>
        </Tabs>

        {accepted && (
          <div className="flex flex-wrap gap-1 pt-1 border-t border-border">
            {Object.entries(accepted.extensions).map(([ext, label]) => (
              <Badge key={ext} variant="outline" className="text-[10px] h-4 px-1.5 font-mono">
                {ext} <span className="text-muted-foreground ml-1 font-sans">{label}</span>
              </Badge>
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
