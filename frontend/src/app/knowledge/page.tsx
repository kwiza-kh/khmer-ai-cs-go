"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  deleteKnowledge, dismissKnowledgeContradiction, getKnowledgeDocument, getRagSettings, knowledgeDocQuality,
  knowledgeGaps, knowledgeGapDraft, listKnowledge, listKnowledgeContradictions, ragQuery,
  resolveKnowledgeContradiction, retryKnowledge, updateKnowledgeDocument, updateRagSettings,
  type KnowledgeContradiction, type KnowledgeDocument, type KnowledgeDocQuality, type KnowledgeGap,
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
  FileType2, GitCompareArrows, Loader2, Pencil, RotateCw, Save, Search, Sparkles, ThumbsDown, ThumbsUp,
  Trash2, Plus, Wand2, Check, X, type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { KnowledgeUploadDialog } from "@/components/knowledge/upload-dialog";
import { MarkdownKnowledgeEditor } from "@/components/knowledge/markdown-editor";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { useAuth } from "@/lib/auth-client";

interface SrcItem { title: string; score: number; content: string }

type FileKind = "txt" | "md" | "csv" | "docx" | "pdf" | "other";

interface FileGroup {
  kind: FileKind;
  labelKey: string;
  extension?: string;
  icon: LucideIcon;
  iconClass: string;
}

const FILE_GROUPS: FileGroup[] = [
  { kind: "txt", labelKey: "kb.groupTxt", extension: "TXT", icon: FileText, iconClass: "bg-sky-500/10 text-sky-600 dark:text-sky-400" },
  { kind: "md", labelKey: "kb.groupMd", extension: "MD", icon: FileCode2, iconClass: "bg-violet-500/10 text-violet-600 dark:text-violet-400" },
  { kind: "csv", labelKey: "kb.groupCsv", extension: "CSV", icon: FileSpreadsheet, iconClass: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" },
  { kind: "docx", labelKey: "kb.groupDocx", extension: "DOCX", icon: FileType2, iconClass: "bg-blue-500/10 text-blue-600 dark:text-blue-400" },
  { kind: "pdf", labelKey: "kb.groupPdf", extension: "PDF", icon: FileText, iconClass: "bg-rose-500/10 text-rose-600 dark:text-rose-400" },
  { kind: "other", labelKey: "kb.groupOther", icon: File, iconClass: "bg-muted text-muted-foreground" },
];

function fileKindFromTitle(title: string): FileKind {
  const extension = title.trim().split(".").pop()?.toLowerCase();
  return extension === "txt" || extension === "md" || extension === "csv" || extension === "docx" || extension === "pdf"
    ? extension
    : "other";
}

function formatDate(value: string | undefined, unavailable: string): string {
  if (!value) return unavailable;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return unavailable;
  // Pin to English so dates render consistently regardless of browser locale
  // (the browser may be localized to 中文, which produced "2026年8月11日").
  return new Intl.DateTimeFormat("en-US", { year: "numeric", month: "short", day: "numeric" }).format(date);
}

function isEditableFileKind(kind: FileKind): boolean {
  return kind === "txt" || kind === "md" || kind === "csv" || kind === "docx";
}

export default function KnowledgePage() {
  const { t, tf } = useI18n();
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
  // Prefills a fresh create form (AI draft generated from a knowledge gap).
  const [editorDraft, setEditorDraft] = useState<{ title: string; content: string; language: string } | null>(null);

  // Knowledge-quality & gap insights + contradiction review queue.
  const [quality, setQuality] = useState<KnowledgeDocQuality[]>([]);
  const [gaps, setGaps] = useState<KnowledgeGap[]>([]);
  const [contradictions, setContradictions] = useState<KnowledgeContradiction[]>([]);
  const [workingContradictionId, setWorkingContradictionId] = useState<number | null>(null);
  const [drafting, setDrafting] = useState<string | null>(null);
  // Ingest-time compile toggle (admin-only; null until loaded).
  const [compileEnabled, setCompileEnabled] = useState<boolean | null>(null);
  const [toggleSaving, setToggleSaving] = useState(false);

  const loadInsights = useCallback(async () => {
    try {
      const [q, g, c] = await Promise.all([knowledgeDocQuality(), knowledgeGaps(), listKnowledgeContradictions("pending")]);
      setQuality(q.data || []);
      setGaps(g.data || []);
      setContradictions(c.data || []);
    } catch {
      // Insights are auxiliary — never break the page over them.
    }
  }, []);

  const handleContradiction = async (id: number, action: "resolve" | "dismiss") => {
    setWorkingContradictionId(id);
    try {
      if (action === "resolve") await resolveKnowledgeContradiction(id);
      else await dismissKnowledgeContradiction(id);
      setContradictions((cur) => cur.filter((c) => c.id !== id));
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setWorkingContradictionId(null);
    }
  };

  const handleToggleCompile = async () => {
    if (compileEnabled === null) return;
    setToggleSaving(true);
    try {
      const res = await updateRagSettings(!compileEnabled);
      setCompileEnabled(res.compile_enabled);
      toast.success(t("kb.settingsSaved"));
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setToggleSaving(false);
    }
  };

  const openCreateEditor = () => {
    setEditorDoc(null);
    setEditorDraft(null);
    setEditorKey((k) => k + 1);
    setEditorOpen(true);
  };

  const handleDraft = async (gapQuery: string) => {
    setDrafting(gapQuery);
    try {
      const draft = await knowledgeGapDraft(gapQuery);
      setEditorDraft({ title: draft.title, content: draft.content, language: draft.language });
      setEditorDoc(null);
      setEditorKey((k) => k + 1);
      setEditorOpen(true);
    } catch (err: unknown) {
      toast.error((err as Error).message || t("kb.gapDraftFailed"));
    } finally {
      setDrafting(null);
    }
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
      toast.error((err as Error).message || t("kb.loadFailed"));
    } finally {
      if (showLoading) setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void loadDocs();
      void loadInsights();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [loadDocs, loadInsights]);

  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "platform_admin";
  useEffect(() => {
    if (!isAdmin) return;
    getRagSettings()
      .then((s) => setCompileEnabled(s.compile_enabled))
      .catch(() => { /* settings are admin-only; hide the toggle on failure */ });
  }, [isAdmin]);

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
      toast.success(t("kb.requeued"));
      await loadDocs(false);
    } catch (err: unknown) {
      toast.error((err as Error).message || t("kb.requeueFailed"));
    } finally {
      setRetryingDocId(null);
    }
  };

  const handleDelete = async (doc: KnowledgeDocument): Promise<boolean> => {
    if (!window.confirm(tf("kb.deleteConfirm", { title: doc.title }))) return false;
    setDeletingDocId(doc.doc_id);
    try {
      await deleteKnowledge(doc.doc_id);
      setAnswer("");
      setSources([]);
      if (previewDoc?.doc_id === doc.doc_id) closePreview();
      toast.success(t("kb.deleted"));
      await loadDocs(false);
      return true;
    } catch (err: unknown) {
      toast.error((err as Error).message || t("kb.deleteFailed"));
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
      toast.error((err as Error).message || t("kb.loadDocFailed"));
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
      toast.success(t("kb.savedReindex"));
      await loadDocs(false);
    } catch (err: unknown) {
      toast.error((err as Error).message || t("kb.saveFailed"));
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
        kicker={t("kb.kicker")}
        title={t("kb.title")}
        description={tf("kb.readyCount", { ready: readyCount, total })}
        actions={
          <>
            {isAdmin && compileEnabled !== null && (
              <button
                type="button"
                role="switch"
                aria-checked={compileEnabled}
                onClick={() => void handleToggleCompile()}
                disabled={toggleSaving}
                title={t("kb.compileToggleHint")}
                className={cn(
                  "inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-card px-2 text-xs transition-colors disabled:opacity-50",
                  compileEnabled ? "text-foreground" : "text-muted-foreground",
                )}
              >
                <Wand2 className={cn("size-3.5", compileEnabled && "text-primary")} />
                <span className="hidden sm:inline">{t("kb.compileToggle")}</span>
                <span className={cn("ml-0.5 h-3.5 w-6 rounded-full transition-colors", compileEnabled ? "bg-primary" : "bg-muted-foreground/30")}>
                  <span className={cn("block size-3.5 rounded-full bg-background transition-transform", compileEnabled && "translate-x-2.5")} />
                </span>
              </button>
            )}
            <Button size="sm" variant="outline" className="gap-1.5" onClick={openCreateEditor}>
              <Plus className="size-3.5" />{t("kb.newDoc")}
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
                  placeholder={t("kb.searchPh")}
                  className="h-10 flex-1 border-0 bg-transparent text-sm shadow-none focus-visible:ring-0"
                />
                <Button onClick={handleSearch} disabled={searching || !query.trim()} className="h-10 gap-2 px-3">
                  {searching ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                  <span className="hidden sm:inline">{t("nav.search")}</span>
                </Button>
              </div>
              <p className="pt-3 text-xs text-muted-foreground">{t("kb.searchHint")}</p>
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
                <CardTitle className="text-sm">{t("kb.groundedAnswer")}</CardTitle>
              </CardHeader>
              <CardContent className="pt-5"><p className="text-sm leading-7">{answer}</p></CardContent>
            </Card>
          )}

          {sources.length > 0 && !searching && (
            <section className="space-y-2">
              <p className="text-xs font-bold uppercase tracking-[0.1em] text-muted-foreground">{tf("kb.sources", { n: sources.length })}</p>
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
              {t("kb.searchNote")}
            </div>
          )}

          {(quality.some((q) => q.uses > 0 || q.thumbs_up > 0 || q.thumbs_down > 0) || gaps.length > 0 || contradictions.length > 0) && (
            <div className="grid gap-4 lg:grid-cols-2">
              {contradictions.length > 0 && (
                <Card className="lg:col-span-2 border-warning/40">
                  <CardHeader className="border-b border-border pb-2">
                    <CardTitle className="flex items-center gap-2 text-sm">
                      <GitCompareArrows className="size-3.5 text-warning" />{t("kb.contradictionTitle")}
                      <Badge variant="warning" className="h-4 px-1.5 text-[10px]">{contradictions.length}</Badge>
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-2 pt-4">
                    {contradictions.map((c) => (
                      <div key={c.id} className="rounded-lg border border-warning/30 bg-warning/5 p-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <p className="min-w-0 truncate text-xs font-semibold">{c.new_title}</p>
                          <div className="flex shrink-0 items-center gap-1.5">
                            <Button variant="outline" size="sm" className="h-6 gap-1 px-2 text-[11px]"
                              onClick={() => void handleContradiction(c.id, "resolve")}
                              disabled={workingContradictionId === c.id}>
                              {workingContradictionId === c.id ? <Loader2 className="size-3 animate-spin" /> : <Check className="size-3" />}
                              {t("kb.contradictionResolve")}
                            </Button>
                            <Button variant="ghost" size="sm" className="h-6 gap-1 px-2 text-[11px] text-muted-foreground"
                              onClick={() => void handleContradiction(c.id, "dismiss")}
                              disabled={workingContradictionId === c.id}>
                              <X className="size-3" />{t("kb.contradictionDismiss")}
                            </Button>
                          </div>
                        </div>
                        <div className="mt-2 space-y-1.5">
                          {c.items.map((item, i) => (
                            <div key={i} className="grid gap-1.5 sm:grid-cols-2">
                              <p className="rounded border border-border/60 bg-background/60 px-2 py-1 text-[11px] leading-relaxed">
                                <span className="font-medium text-success">{t("kb.contradictionNew")}：</span>{item.new_claim}
                              </p>
                              <p className="rounded border border-border/60 bg-background/60 px-2 py-1 text-[11px] leading-relaxed">
                                <span className="font-medium text-destructive">{t("kb.contradictionOld")}：</span>{item.old_claim}
                                {item.old_doc_title && <span className="block text-[10px] text-muted-foreground">{item.old_doc_title}</span>}
                              </p>
                            </div>
                          ))}
                        </div>
                      </div>
                    ))}
                  </CardContent>
                </Card>
              )}
              <Card>
                <CardHeader className="border-b border-border pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <ThumbsUp className="size-3.5 text-success" />{t("kb.qualityTitle")}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-1.5 pt-4">
                  {quality.filter((q) => q.uses > 0 || q.thumbs_up > 0 || q.thumbs_down > 0).length === 0 ? (
                    <p className="text-xs text-muted-foreground">{t("kb.qualityEmpty")}</p>
                  ) : (
                    quality
                      .filter((q) => q.uses > 0 || q.thumbs_up > 0 || q.thumbs_down > 0)
                      .slice(0, 6)
                      .map((q) => (
                        <div key={q.doc_id} className="flex items-center justify-between gap-2 rounded-lg border border-border/60 px-3 py-1.5">
                          <p className="min-w-0 truncate text-xs font-medium">{q.title}</p>
                          <div className="flex shrink-0 items-center gap-2 text-[11px] text-muted-foreground">
                            <span>{tf("kb.qualityUses", { n: q.uses })}</span>
                            <span className="text-success">👍{q.thumbs_up}</span>
                            <span className={q.thumbs_down > 0 ? "font-medium text-destructive" : ""}>👎{q.thumbs_down}</span>
                          </div>
                        </div>
                      ))
                  )}
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="border-b border-border pb-2">
                  <CardTitle className="flex items-center gap-2 text-sm">
                    <Sparkles className="size-3.5 text-warning" />{t("kb.gapsTitle")}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-1.5 pt-4">
                  {gaps.length === 0 ? (
                    <p className="text-xs text-muted-foreground">{t("kb.gapsEmpty")}</p>
                  ) : (
                    gaps.slice(0, 6).map((gap) => (
                      <div key={gap.query} className="flex items-center justify-between gap-2 rounded-lg border border-border/60 px-3 py-1.5">
                        <p className="min-w-0 truncate text-xs">{gap.query}</p>
                        <div className="flex shrink-0 items-center gap-2">
                          <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{tf("kb.qualityUses", { n: gap.hits })}</Badge>
                          <Button
                            variant="outline"
                            size="sm"
                            className="h-6 gap-1 px-2 text-[11px]"
                            onClick={() => { void handleDraft(gap.query); }}
                            disabled={drafting !== null}
                          >
                            {drafting === gap.query ? <Loader2 className="size-3 animate-spin" /> : <Sparkles className="size-3" />}
                            {t("kb.gapDraft")}
                          </Button>
                        </div>
                      </div>
                    ))
                  )}
                </CardContent>
              </Card>
            </div>
          )}

          <section className="space-y-4 pt-2">
            <div className="flex items-end justify-between gap-3">
              <div>
                <p className="text-xs font-bold uppercase tracking-[0.1em] text-muted-foreground">{t("kb.savedKnowledge")}</p>
                <h2 className="mt-1 text-lg font-semibold tracking-tight">{t("kb.docLibrary")}</h2>
              </div>
              <Badge variant="secondary" className="h-6 px-2.5 text-xs">{tf("kb.docCount", { n: total })}</Badge>
            </div>

            {loading ? (
              <div className="space-y-4">
                {[1, 2, 3].map((index) => <Skeleton key={index} className="h-44 rounded-xl" />)}
              </div>
            ) : documentGroups.length === 0 ? (
              <Card><CardContent className="py-12"><EmptyState icon={FileText} title={t("kb.emptyTitle")} description={t("kb.emptyDesc")} action={<KnowledgeUploadDialog onUploaded={loadDocs} />} /></CardContent></Card>
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
                        <CardTitle className="text-sm">{t(group.labelKey)}</CardTitle>
                        <p className="mt-0.5 text-xs text-muted-foreground">{tf("kb.groupSaved", { n: group.documents.length })}</p>
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
                      {collapsed ? t("kb.expand") : t("kb.collapse")}
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
                                {doc.origin === "compiled" && doc.compiled_from_title && (
                                  <p className="mt-0.5 truncate text-[10px] text-muted-foreground">{tf("kb.compiledFrom", { title: doc.compiled_from_title })}</p>
                                )}
                                <p className="mt-2 text-xs text-muted-foreground">{tf("kb.created", { date: formatDate(doc.created_at, t("kb.dateUnavailable")) })}</p>
                                <div className="mt-3 flex flex-wrap items-center gap-1.5">
                                  <IndexStatusBadge status={doc.index_status} />
                                  {doc.origin === "compiled" && (
                                    <Badge variant="info" className="h-4 gap-1 px-1.5 text-[10px]">
                                      <Wand2 className="size-2.5" />{t("kb.compiledBadge")}
                                    </Badge>
                                  )}
                                  {doc.compile_status === "failed" && (
                                    <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{t("kb.compileFailed")}</Badge>
                                  )}
                                  {doc.category && <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{doc.category}</Badge>}
                                  <span className="text-[11px] text-muted-foreground">{tf("kb.chunks", { n: doc.chunk_count })}</span>
                                </div>
                              </button>
                              <button
                                type="button"
                                title={tf("kb.deleteTitle", { title: doc.title })}
                                aria-label={tf("kb.deleteTitle", { title: doc.title })}
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
                                  {t("kb.retry")}
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
                <DialogTitle className="truncate text-sm">{previewDoc?.title || t("kb.previewTitle")}</DialogTitle>
                <DialogDescription className="mt-1 text-xs">
                  {previewCanEdit ? t("kb.previewEditDesc") : t("kb.previewReadDesc")}
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
                  <span>{tf("kb.created", { date: formatDate(previewDoc.created_at, t("kb.dateUnavailable")) })}</span>
                  <span>{tf("kb.chunks", { n: previewDoc.chunk_count })}</span>
                  <IndexStatusBadge status={previewDoc.index_status} />
                </div>
                {editing ? (
                  <Textarea
                    value={editContent}
                    onChange={(event) => setEditContent(event.target.value)}
                    className="min-h-[52vh] resize-y font-mono text-xs leading-6"
                    aria-label={t("kb.contentAria")}
                  />
                ) : (
                  <pre className="max-h-[52vh] overflow-auto whitespace-pre-wrap rounded-xl border border-border bg-muted/30 p-4 text-xs leading-6 text-foreground">{previewDoc.content || t("kb.noExtract")}</pre>
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
                {t("kb.delete")}
              </Button>
              <div className="flex items-center gap-2">
                {previewCanEdit && !editing && (
                  <>
                    <Button variant="outline" size="sm" onClick={() => { closePreview(); openEditEditor(previewDoc); }}>
                      <Pencil className="size-3.5" />{t("kb.editInEditor")}
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setEditing(true)}><Pencil className="size-3.5" />{t("kb.quickEdit")}</Button>
                  </>
                )}
                {previewCanEdit && editing && (
                  <>
                    <Button variant="ghost" size="sm" onClick={() => { setEditing(false); setEditContent(previewDoc.content || ""); }}>{t("common.cancel")}</Button>
                    <Button size="sm" onClick={handleSave} disabled={saving || !editContent.trim()}>
                      {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      {t("kb.saveReindex")}
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
        initialTitle={editorDraft?.title}
        initialContent={editorDraft?.content}
        initialLanguage={editorDraft?.language}
        onSaved={() => {
          void loadDocs(false);
          void loadInsights();
        }}
      />
    </div>
  );
}

function IndexStatusBadge({ status }: { status: KnowledgeDocument["index_status"] }) {
  const { t } = useI18n();
  const labels: Record<string, readonly [string, "secondary" | "info" | "success" | "destructive"]> = {
    pending: ["kb.statusQueued", "secondary"],
    indexing: ["kb.statusIndexing", "info"],
    ready: ["kb.statusReady", "success"],
    failed: ["kb.statusFailed", "destructive"],
  };
  const [labelKey, variant] = labels[status] ?? ["kb.statusUnknown", "secondary"];
  return <Badge variant={variant} className="h-4 px-1.5 text-[10px]">{t(labelKey)}</Badge>;
}
