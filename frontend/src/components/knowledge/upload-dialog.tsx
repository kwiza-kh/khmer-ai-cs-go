"use client";

import * as React from "react";
import { uploadKnowledgeFile, ingestKnowledgeURL, getAcceptedFileTypes, type AcceptedFileTypes } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Loader2, UploadCloud, FileText, FileUp, X, Globe } from "lucide-react";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";

interface Props {
  /** Called after a successful upload so the parent can refresh its list. */
  onUploaded: () => void;
  /** Optional trigger element; defaults to a labeled Upload button. */
  trigger?: React.ReactElement;
}

/**
 * Upload dialog with two sources:
 *   - Upload file: drag-drop / browse; multipart to /knowledge/upload/file
 *   - From URL: the backend fetches the page, strips it to text and runs it
 *     through the same chunk → embed pipeline (/knowledge/upload/url)
 *
 * Pasting raw text deliberately lives in the Markdown editor ("New document")
 * instead: it validates Markdown and keeps the document editable afterwards.
 */
export function KnowledgeUploadDialog({ onUploaded, trigger }: Props) {
  const { t, tf } = useI18n();
  const [open, setOpen] = React.useState(false);
  const [tab, setTab] = React.useState<"file" | "url">("file");

  // Optional title override for URL ingests (defaults to the page's <title>).
  const [title, setTitle] = React.useState("");

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
    setTitle(""); setFile(null); setCategory(""); setUrl("");
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
        toast.error(tf("kbup.unsupported", { ext }), {
          description: tf("kbup.allowed", { list: Object.keys(accepted.extensions).join(", ") }),
        });
        return;
      }
      if (f.size > accepted.max_bytes) {
        toast.error(tf("kbup.tooLarge", { size: (f.size / 1024 / 1024).toFixed(1) }),
          { description: tf("kbup.maxSize", { max: accepted.max_megabytes }) });
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

  const submitFile = async () => {
    if (!file) return;
    setUploading(true);
    try {
      const doc = await uploadKnowledgeFile(file, { category });
      toast.success(tf("kbup.queuedNamed", { title: doc.title }));
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
      toast.error(t("kbup.invalidUrl"));
      return;
    }
    setUploading(true);
    try {
      const doc = await ingestKnowledgeURL({
        url: trimmed, category,
        title: title.trim() || undefined,
      });
      toast.success(tf("kbup.queuedNamed", { title: doc.title }));
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
            <UploadCloud className="size-3.5" />{t("kbup.upload")}
          </Button>
        )
      } />
      <DialogContent className="max-w-lg">
        <DialogHeader><DialogTitle className="text-sm">{t("kbup.title")}</DialogTitle></DialogHeader>

        <Tabs value={tab} onValueChange={(v) => setTab((v || "file") as "file" | "url")}>
          <TabsList className="w-full">
            <TabsTrigger value="file" className="flex-1 text-xs gap-1.5"><FileUp className="size-3" />{t("kbup.tabFile")}</TabsTrigger>
            <TabsTrigger value="url" className="flex-1 text-xs gap-1.5"><Globe className="size-3" />{t("kbup.tabUrl")}</TabsTrigger>
          </TabsList>

          {/* ---------- File upload tab ---------- */}
          <TabsContent value="file" className="mt-4 space-y-3">
            <input
              ref={inputRef}
              type="file"
              accept={(accepted && Object.keys(accepted.extensions).join(",")) || ".txt,.md,.csv,.pdf,.docx,.xlsx,.wps,.et,.jpg,.jpeg,.png,.webp"}
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
                <p className="text-sm font-medium">{t("kbup.dropHint")}</p>
                <p className="text-xs text-muted-foreground mt-1">
                  {accepted
                    ? Object.keys(accepted.extensions).map((ext) => ext.slice(1).toUpperCase()).join(" · ")
                    : "PDF · DOCX · XLSX · WPS · ET · TXT · MD · CSV · JPG/PNG"}
                  {accepted && ` ${tf("kbup.upTo", { max: accepted.max_megabytes })}`}
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
                  aria-label={t("kbup.removeFile")}
                >
                  <X className="size-3.5" />
                </Button>
              </div>
            )}

            <div>
              <label className="text-xs text-muted-foreground block mb-1">{t("kbup.category")}</label>
              <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder={t("kbup.categoryPh")} className="h-8 text-xs" />
            </div>
            <p className="text-label text-muted-foreground -mt-1">
              {t("kbup.langAuto")}
            </p>

            <Button onClick={submitFile} disabled={uploading || !file} className="w-full h-9 text-xs gap-1.5">
              {uploading
                ? <><Loader2 className="size-3.5 animate-spin" /> {t("kbup.uploading")}</>
                : <><UploadCloud className="size-3.5" /> {t("kbup.uploadFileBtn")}</>}
            </Button>
            <p className="text-xs text-muted-foreground text-center">
              {t("kbup.titleFromFilename")}
            </p>
          </TabsContent>

          {/* ---------- URL ingest tab ---------- */}
          <TabsContent value="url" className="mt-4 space-y-3">
            <div>
              <label className="text-xs text-muted-foreground block mb-1">{t("kbup.urlLabel")}</label>
              <Input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://example.com/faq"
                className="h-9 text-sm"
                inputMode="url"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">{t("kbup.urlTitleLabel")}</label>
              <Input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder={t("kbup.urlTitlePh")}
                className="h-9 text-sm"
              />
            </div>
            <div>
              <label className="text-xs text-muted-foreground block mb-1">{t("kbup.category")}</label>
              <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder={t("kbup.categoryPh")} className="h-8 text-xs" />
            </div>
            <p className="text-label text-muted-foreground -mt-1">
              {t("kbup.urlLangAuto")}
            </p>
            <Button onClick={submitUrl} disabled={uploading || !url.trim()} className="w-full h-9 text-xs gap-1.5">
              {uploading
                ? <><Loader2 className="size-3.5 animate-spin" /> {t("kbup.fetching")}</>
                : <><Globe className="size-3.5" /> {t("kbup.ingest")}</>}
            </Button>
            <p className="text-xs text-muted-foreground text-center">
              {t("kbup.ingestNote")}
            </p>
          </TabsContent>
        </Tabs>

        {accepted && (
          <div className="flex flex-wrap gap-1 pt-1 border-t border-border">
            {Object.entries(accepted.extensions).map(([ext, label]) => (
              <Badge key={ext} variant="outline" className="text-label h-4 px-1.5 font-mono">
                {ext} <span className="text-muted-foreground ml-1 font-sans">{label}</span>
              </Badge>
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
