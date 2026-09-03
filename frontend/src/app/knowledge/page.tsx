"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  deleteKnowledge, getKnowledgeDocument, listKnowledge, ragQuery, retryKnowledge,
  updateKnowledgeDocument, type KnowledgeDocument,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  BookOpen, ChevronDown, ChevronUp, File, FileCode2, FileSpreadsheet, FileText,
  FileType2, Loader2, Pencil, RotateCw, Save, Search, Trash2, Plus, type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { KnowledgeUploadDialog } from "@/components/knowledge/upload-dialog";
import { MarkdownKnowledgeEditor } from "@/components/knowledge/markdown-editor";
import { cn } from "@/lib/utils";

interface SrcItem { title: string; score: number; content: string }

type FileKind = "txt" | "md" | "csv" | "docx" | "pdf" | "other";

interface FileGroup {
  kind: FileKind;
  label: string;
  extension?: string;
  icon: LucideIcon;
  iconClass: string;
}

const FILE_GROUPS: FileGroup[] = [
  { kind: "txt", label: "Text files", extension: "TXT", icon: FileText, iconClass: "bg-sky-500/10 text-sky-600 dark:text-sky-400" },
  { kind: "md", label: "Markdown", extension: "MD", icon: FileCode2, iconClass: "bg-violet-500/10 text-violet-600 dark:text-violet-400" },
  { kind: "csv", label: "Spreadsheets", extension: "CSV", icon: FileSpreadsheet, iconClass: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" },
  { kind: "docx", label: "Word documents", extension: "DOCX", icon: FileType2, iconClass: "bg-blue-500/10 text-blue-600 dark:text-blue-400" },
  { kind: "pdf", label: "PDF documents", extension: "PDF", icon: FileText, iconClass: "bg-rose-500/10 text-rose-600 dark:text-rose-400" },
  { kind: "other", label: "Other entries", icon: File, iconClass: "bg-muted text-muted-foreground" },
];

function fileKindFromTitle(title: string): FileKind {
  const extension = title.trim().split(".").pop()?.toLowerCase();
  return extension === "txt" || extension === "md" || extension === "csv" || extension === "docx" || extension === "pdf"
    ? extension
    : "other";
}

function formatDate(value?: string): string {
  if (!value) return "Date unavailable";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Date unavailable";
  // Pin to English so dates render consistently regardless of browser locale
  // (the browser may be localized to 中文, which produced "2026年8月11日").
  return new Intl.DateTimeFormat("en-US", { year: "numeric", month: "short", day: "numeric" }).format(date);
}

function isEditableFileKind(kind: FileKind): boolean {
  return kind === "txt" || kind === "md" || kind === "csv" || kind === "docx";
}

export default function KnowledgePage() {
  const [documents, setDocuments] = useState<KnowledgeDocument[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState<SrcItem[]>([]);
  const [searching, setSearching] = useState(false);
  const [retryingDocId, setRetryingDocId] = useState<number | null>(null);
  const [deletingDocId, setDeletingDocId] = useState<number | null>(null);
  const [collapsedGroups, setCollapsedGroups] = useState<Set<FileKind>>(() => new Set());
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewDoc, setPreviewDoc] = useState<KnowledgeDocument | null>(null);
  const [editing, setEditing] = useState(false);
  const [editContent, setEditContent] = useState("");
  const [saving, setSaving] = useState(false);

  // Markdown editor state (create + edit).
  const [editorOpen, setEditorOpen] = useState(false);
  const [editorDoc, setEditorDoc] = useState<KnowledgeDocument | null>(null);
  // Incremented on each open to remount the editor and reset its form state.
  const [editorKey, setEditorKey] = useState(0);

  const openCreateEditor = () => {
    setEditorDoc(null);
    setEditorKey((k) => k + 1);
    setEditorOpen(true);
  };

  const openEditEditor = (doc: KnowledgeDocument) => {
    setEditorDoc(doc);
    setEditorKey((k) => k + 1);
    setEditorOpen(true);
  };

  const loadDocs = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    try {
      const response = await listKnowledge(1, 100);
      setDocuments(response.data || []);
      setTotal(response.total || 0);
    } catch (err: unknown) {
      toast.error((err as Error).message || "Failed to load documents");
    } finally {
      if (showLoading) setLoading(false);
    }
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadDocs(); }, 0);
    return () => window.clearTimeout(timer);
  }, [loadDocs]);

  const hasActiveIndexing = documents.some((doc) => doc.index_status === "pending" || doc.index_status === "indexing");
  useEffect(() => {
    if (!hasActiveIndexing) return;
    const interval = window.setInterval(() => { void loadDocs(false); }, 3000);
    return () => window.clearInterval(interval);
  }, [hasActiveIndexing, loadDocs]);

  const documentGroups = useMemo(() => FILE_GROUPS
    .map((group) => ({
      ...group,
      documents: documents.filter((doc) => fileKindFromTitle(doc.title) === group.kind),
    }))
    .filter((group) => group.documents.length > 0), [documents]);

  const readyCount = documents.filter((doc) => doc.index_status === "ready").length;
  const previewKind = previewDoc ? fileKindFromTitle(previewDoc.title) : "other";
  const previewGroup = FILE_GROUPS.find((group) => group.kind === previewKind) ?? FILE_GROUPS[5];
  const PreviewIcon = previewGroup.icon;
  const previewCanEdit = isEditableFileKind(previewKind);

  const closePreview = () => {
    setPreviewOpen(false);
    setPreviewLoading(false);
    setPreviewDoc(null);
    setEditing(false);
    setEditContent("");
  };

  const handleSearch = async () => {
    if (!query.trim()) return;
    setSearching(true);
    setAnswer("");
    setSources([]);
    try {
      const result = await ragQuery(query);
      setAnswer(result.answer);
      setSources(result.sources || []);
    } catch (err: unknown) {
      toast.error((err as Error).message);
    } finally {
      setSearching(false);
    }
  };

  const handleRetry = async (docId: number) => {
    setRetryingDocId(docId);
    try {
      await retryKnowledge(docId);
      toast.success("Document requeued for indexing");
      await loadDocs(false);
    } catch (err: unknown) {
      toast.error((err as Error).message || "Failed to requeue document");
    } finally {
      setRetryingDocId(null);
    }
  };

  const handleDelete = async (doc: KnowledgeDocument): Promise<boolean> => {
    if (!window.confirm(`Delete "${doc.title}"? This cannot be undone.`)) return false;
    setDeletingDocId(doc.doc_id);
    try {
      await deleteKnowledge(doc.doc_id);
      setAnswer("");
      setSources([]);
      if (previewDoc?.doc_id === doc.doc_id) closePreview();
      toast.success("Document deleted");
      await loadDocs(false);
      return true;
    } catch (err: unknown) {
      toast.error((err as Error).message || "Failed to delete document");
      return false;
    } finally {
      setDeletingDocId(null);
    }
  };

  const handlePreview = async (doc: KnowledgeDocument) => {
    setPreviewOpen(true);
    setPreviewLoading(true);
    setPreviewDoc(doc);
    setEditing(false);
    setEditContent("");
    try {
      const detail = await getKnowledgeDocument(doc.doc_id);
      setPreviewDoc(detail);
      setEditContent(detail.content || "");
    } catch (err: unknown) {
      toast.error((err as Error).message || "Failed to load document");
    } finally {
      setPreviewLoading(false);
    }
  };

  const handleSave = async () => {
    if (!previewDoc || !editContent.trim()) return;
    setSaving(true);
    try {
      const updated = await updateKnowledgeDocument(previewDoc.doc_id, { content: editContent });
      setPreviewDoc(updated);
      setEditContent(updated.content || editContent);
      setEditing(false);
      toast.success("Document saved and queued for re-indexing");
      await loadDocs(false);
    } catch (err: unknown) {
      toast.error((err as Error).message || "Failed to save document");
    } finally {
      setSaving(false);
    }
  };

  const toggleGroup = (kind: FileKind) => {
    setCollapsedGroups((current) => {
      const next = new Set(current);
      if (next.has(kind)) next.delete(kind);
      else next.add(kind);
      return next;
    });
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={BookOpen}
        kicker="Retrieval library"
        title="Knowledge base"
        description={`${readyCount} of ${total} documents ready`}
        actions={
          <>
            <Button size="sm" variant="outline" className="gap-1.5" onClick={openCreateEditor}>
              <Plus className="size-3.5" />New document
            </Button>
            <KnowledgeUploadDialog onUploaded={loadDocs} />
          </>
        }
      />

      <main className="flex-1 overflow-auto">
        <div className="mx-auto w-full max-w-7xl space-y-6 p-5 sm:p-8">
          <Card className="border-border/80 shadow-sm">
            <CardContent className="p-3 sm:p-4">
              <div className="flex gap-2">
                <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                  <Search className="size-4" />
                </div>
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  onKeyDown={(event) => event.key === "Enter" && handleSearch()}
                  placeholder="Search the knowledge base..."
                  className="h-10 flex-1 border-0 bg-transparent text-sm shadow-none focus-visible:ring-0"
                />
                <Button onClick={handleSearch} disabled={searching || !query.trim()} className="h-10 gap-2 px-3">
                  {searching ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                  <span className="hidden sm:inline">Search</span>
                </Button>
              </div>
              <p className="pt-3 text-xs text-muted-foreground">Ask in Khmer, English, or Chinese across your indexed documents.</p>
            </CardContent>
          </Card>

          {searching && (
            <div className="space-y-3">
              <Skeleton className="h-28 rounded-xl" />
              <Skeleton className="h-16 rounded-xl" />
            </div>
          )}

          {answer && !searching && (
            <Card className="border-primary/20">
              <CardHeader className="border-b border-border pb-3">
                <CardTitle className="text-sm">Grounded answer</CardTitle>
              </CardHeader>
              <CardContent className="pt-5"><p className="text-sm leading-7">{answer}</p></CardContent>
            </Card>
          )}

          {sources.length > 0 && !searching && (
            <section className="space-y-2">
              <p className="text-xs font-bold uppercase tracking-[0.1em] text-muted-foreground">Sources ({sources.length})</p>
              <div className="grid gap-2 md:grid-cols-2">
                {sources.map((source, index) => (
                  <Card key={`${source.title}-${index}`}>
                    <CardContent className="p-3">
                      <div className="mb-1 flex items-center justify-between gap-2">
                        <p className="truncate text-xs font-medium">{source.title}</p>
                        <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{((source.score ?? 0) * 100).toFixed(0)}%</Badge>
                      </div>
                      <p className="line-clamp-2 text-xs leading-5 text-muted-foreground">{source.content}</p>
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          )}

          {!answer && !searching && (
            <div className="flex items-center gap-3 rounded-xl border border-dashed border-border px-4 py-3 text-sm text-muted-foreground">
              <Search className="size-4 shrink-0" />
              Search returns answers grounded only in the documents below.
            </div>
          )}

          <section className="space-y-4 pt-2">
            <div className="flex items-end justify-between gap-3">
              <div>
                <p className="text-xs font-bold uppercase tracking-[0.1em] text-muted-foreground">Saved knowledge</p>
                <h2 className="mt-1 text-lg font-semibold tracking-tight">Document library</h2>
              </div>
              <Badge variant="secondary" className="h-6 px-2.5 text-xs">{total} documents</Badge>
            </div>

            {loading ? (
              <div className="space-y-4">
                {[1, 2, 3].map((index) => <Skeleton key={index} className="h-44 rounded-xl" />)}
              </div>
            ) : documentGroups.length === 0 ? (
              <Card><CardContent className="py-12"><EmptyState icon={FileText} title="No documents yet" description="Upload a file, paste text, or import a public web page." /></CardContent></Card>
            ) : documentGroups.map((group) => {
              const collapsed = collapsedGroups.has(group.kind);
              const GroupIcon = group.icon;
              return (
                <Card key={group.kind} className="overflow-hidden border-border/80">
                  <CardHeader className="flex flex-row items-center justify-between gap-3 border-b border-border bg-muted/20 px-4 py-3">
                    <div className="flex min-w-0 items-center gap-3">
                      <div className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg", group.iconClass)}>
                        <GroupIcon className="size-4" />
                      </div>
                      <div className="min-w-0">
                        <CardTitle className="text-sm">{group.label}</CardTitle>
                        <p className="mt-0.5 text-xs text-muted-foreground">{group.documents.length} saved {group.documents.length === 1 ? "document" : "documents"}</p>
                      </div>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => toggleGroup(group.kind)}
                      aria-expanded={!collapsed}
                      className="h-8 shrink-0 gap-1.5 px-2 text-xs"
                    >
                      {collapsed ? <ChevronDown className="size-3.5" /> : <ChevronUp className="size-3.5" />}
                      {collapsed ? "Expand" : "Collapse"}
                    </Button>
                  </CardHeader>
                  {!collapsed && (
                    <CardContent className="p-3 sm:p-4">
                      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                        {group.documents.map((doc) => {
                          const kind = fileKindFromTitle(doc.title);
                          const metadata = FILE_GROUPS.find((entry) => entry.kind === kind) ?? FILE_GROUPS[5];
                          const DocumentIcon = metadata.icon;
                          return (
                            <div key={doc.doc_id} className="relative rounded-xl border border-border bg-background transition-colors hover:border-primary/35 hover:bg-muted/30">
                              <button
                                type="button"
                                onClick={() => { void handlePreview(doc); }}
                                className="block w-full p-4 pr-11 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                              >
                                <div className="mb-4 flex items-center gap-2.5">
                                  <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", metadata.iconClass)}>
                                    <DocumentIcon className="size-4" />
                                  </div>
                                  {metadata.extension && <Badge variant="outline" className="h-5 px-1.5 font-mono text-[10px]">{metadata.extension}</Badge>}
                                </div>
                                <p className="line-clamp-2 min-h-10 text-sm font-semibold leading-5">{doc.title}</p>
                                <p className="mt-2 text-xs text-muted-foreground">Created {formatDate(doc.created_at)}</p>
                                <div className="mt-3 flex flex-wrap items-center gap-1.5">
                                  <IndexStatusBadge status={doc.index_status} />
                                  {doc.category && <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{doc.category}</Badge>}
                                  <span className="text-[11px] text-muted-foreground">{doc.chunk_count} chunks</span>
                                </div>
                              </button>
                              <button
                                type="button"
                                title={`Delete ${doc.title}`}
                                aria-label={`Delete ${doc.title}`}
                                onClick={() => { void handleDelete(doc); }}
                                disabled={deletingDocId === doc.doc_id}
                                className="absolute right-2 top-2 flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive disabled:opacity-50"
                              >
                                {deletingDocId === doc.doc_id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                              </button>
                              {doc.index_status === "failed" && (
                                <Button
                                  variant="outline"
                                  size="sm"
                                  className="absolute bottom-3 right-3 h-6 px-2 text-[11px]"
                                  onClick={() => { void handleRetry(doc.doc_id); }}
                                  disabled={retryingDocId === doc.doc_id}
                                >
                                  {retryingDocId === doc.doc_id ? <Loader2 className="size-3 animate-spin" /> : <RotateCw className="size-3" />}
                                  Retry
                                </Button>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    </CardContent>
                  )}
                </Card>
              );
            })}
          </section>
        </div>
      </main>

      <Dialog open={previewOpen} onOpenChange={(open) => open || closePreview()}>
        <DialogContent className="max-h-[calc(100vh-2rem)] gap-0 overflow-hidden p-0 sm:max-w-4xl">
          <DialogHeader className="border-b border-border px-5 py-4 pr-12">
            <div className="flex items-center gap-3">
              <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", previewGroup.iconClass)}>
                <PreviewIcon className="size-4" />
              </div>
              <div className="min-w-0">
                <DialogTitle className="truncate text-sm">{previewDoc?.title || "Document preview"}</DialogTitle>
                <DialogDescription className="mt-1 text-xs">
                  {previewCanEdit ? "Edit extracted text and re-index the document." : "Read-only extracted text preview."}
                </DialogDescription>
              </div>
            </div>
          </DialogHeader>

          <div className="min-h-0 flex-1 overflow-auto p-5">
            {previewLoading ? (
              <div className="space-y-3"><Skeleton className="h-5 w-1/3" /><Skeleton className="h-72" /></div>
            ) : previewDoc ? (
              <div className="space-y-4">
                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  {previewGroup.extension && <Badge variant="outline" className="h-5 px-1.5 font-mono text-[10px]">{previewGroup.extension}</Badge>}
                  <span>Created {formatDate(previewDoc.created_at)}</span>
                  <span>{previewDoc.chunk_count} chunks</span>
                  <IndexStatusBadge status={previewDoc.index_status} />
                </div>
                {editing ? (
                  <Textarea
                    value={editContent}
                    onChange={(event) => setEditContent(event.target.value)}
                    className="min-h-[52vh] resize-y font-mono text-xs leading-6"
                    aria-label="Document content"
                  />
                ) : (
                  <pre className="max-h-[52vh] overflow-auto whitespace-pre-wrap rounded-xl border border-border bg-muted/30 p-4 text-xs leading-6 text-foreground">{previewDoc.content || "No extractable text is available for this document."}</pre>
                )}
              </div>
            ) : null}
          </div>

          {previewDoc && !previewLoading && (
            <DialogFooter className="flex-row items-center justify-between gap-3 border-t border-border p-4 sm:justify-between">
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                onClick={() => { void handleDelete(previewDoc); }}
                disabled={deletingDocId === previewDoc.doc_id}
              >
                {deletingDocId === previewDoc.doc_id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                Delete
              </Button>
              <div className="flex items-center gap-2">
                {previewCanEdit && !editing && (
                  <>
                    <Button variant="outline" size="sm" onClick={() => { closePreview(); openEditEditor(previewDoc); }}>
                      <Pencil className="size-3.5" />Edit in editor
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setEditing(true)}><Pencil className="size-3.5" />Quick edit</Button>
                  </>
                )}
                {previewCanEdit && editing && (
                  <>
                    <Button variant="ghost" size="sm" onClick={() => { setEditing(false); setEditContent(previewDoc.content || ""); }}>Cancel</Button>
                    <Button size="sm" onClick={handleSave} disabled={saving || !editContent.trim()}>
                      {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      Save & re-index
                    </Button>
                  </>
                )}
              </div>
            </DialogFooter>
          )}
        </DialogContent>
      </Dialog>

      {/* Markdown knowledge editor (create + edit) */}
      <MarkdownKnowledgeEditor
        key={editorKey}
        open={editorOpen}
        onOpenChange={(open) => {
          setEditorOpen(open);
          if (!open) setEditorDoc(null);
        }}
        document={editorDoc}
        onSaved={() => void loadDocs(false)}
      />
    </div>
  );
}

function IndexStatusBadge({ status }: { status: KnowledgeDocument["index_status"] }) {
  const labels: Record<string, readonly [string, "secondary" | "info" | "success" | "destructive"]> = {
    pending: ["Queued", "secondary"],
    indexing: ["Indexing", "info"],
    ready: ["Ready", "success"],
    failed: ["Failed", "destructive"],
  };
  const [label, variant] = labels[status] ?? ["Unknown", "secondary"];
  return <Badge variant={variant} className="h-4 px-1.5 text-[10px]">{label}</Badge>;
}
