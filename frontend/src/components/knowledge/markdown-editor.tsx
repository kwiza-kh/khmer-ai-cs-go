"use client";

import * as React from "react";
import {
  uploadKnowledge, updateKnowledgeDocument, ragQuery,
  type KnowledgeDocument,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter,
} from "@/components/ui/dialog";
import { Markdown } from "@/components/markdown";
import { Loader2, Save, Eye, PenLine, Search, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** When set, the editor is in "edit" mode and loads this document's content. */
  document?: KnowledgeDocument | null;
  /** Called after a successful create/update so the parent can refresh. */
  onSaved: () => void;
}

/**
 * Full-screen Markdown knowledge editor:
 *   - Create a document directly (title / category / language / tags / body)
 *   - Edit an existing document (loads content + metadata)
 *   - Split-pane: raw Markdown on the left, live rendered preview on the right
 *   - "Test retrieval" probes the current RAG index for the document's terms
 *
 * Markdown headings (`#`, `##`, `###`) drive the backend chunking, so the
 * retrieval quality is directly affected by how you structure the document.
 */
export function MarkdownKnowledgeEditor({ open, onOpenChange, document, onSaved }: Props) {
  const isEdit = Boolean(document?.doc_id);

  // State is initialized lazily from props. The parent passes a changing `key`
  // to force a remount whenever a different document (or a fresh create) opens,
  // which is the idiomatic way to reset form state without an effect.
  const [title, setTitle] = React.useState(document?.title ?? "");
  const [category, setCategory] = React.useState(document?.category ?? "");
  const [tags, setTags] = React.useState((document?.tags ?? []).join(", "));
  const [content, setContent] = React.useState(document?.content ?? "");
  const [saving, setSaving] = React.useState(false);
  const [showPreview, setShowPreview] = React.useState(true);

  // Test-retrieval state
  const [testQuery, setTestQuery] = React.useState("");
  const [testing, setTesting] = React.useState(false);
  const [testSources, setTestSources] = React.useState<{ title: string; score: number; content: string }[]>([]);

  const parsedTags = React.useMemo(
    () => tags.split(",").map((t) => t.trim()).filter(Boolean),
    [tags],
  );

  const canSave = title.trim().length > 0 && content.trim().length > 0 && !saving;

  const handleSave = async () => {
    if (!canSave) return;
    setSaving(true);
    try {
      if (isEdit && document?.doc_id) {
        await updateKnowledgeDocument(document.doc_id, {
          content,
          title,
          category,
          tags: parsedTags,
        });
        toast.success("Document saved and queued for re-indexing");
      } else {
        await uploadKnowledge({ title, content, category, tags: parsedTags });
        toast.success("Document created and queued for indexing");
      }
      onOpenChange(false);
      onSaved();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async () => {
    if (!testQuery.trim()) return;
    setTesting(true);
    setTestSources([]);
    try {
      const result = await ragQuery(testQuery);
      setTestSources(result.sources || []);
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setTesting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-[calc(100vw-2rem)] sm:max-w-6xl h-[90vh] max-h-[900px] p-0 gap-0 overflow-hidden flex flex-col"
      >
        <DialogHeader className="border-b border-border px-5 py-4 pr-14 shrink-0">
          <DialogTitle className="text-sm">
            {isEdit ? "Edit knowledge document" : "Create knowledge document"}
          </DialogTitle>
        </DialogHeader>

        <div className="flex-1 min-h-0 overflow-auto">
          <div className="space-y-4 p-5">
            {/* Metadata */}
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="sm:col-span-2">
                <label className="text-xs text-muted-foreground block mb-1">Title *</label>
                <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. Shipping policy" className="h-9 text-sm" />
              </div>
              <div className="sm:col-span-2">
                <label className="text-xs text-muted-foreground block mb-1">Category</label>
                <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder="FAQ, policy, product…" className="h-9 text-sm" />
              </div>
              <div className="sm:col-span-2">
                <label className="text-xs text-muted-foreground block mb-1">Tags (comma-separated)</label>
                <Input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="shipping, returns, faq" className="h-9 text-sm" />
                {parsedTags.length > 0 && (
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    {parsedTags.map((t) => <Badge key={t} variant="secondary" className="h-4 px-1.5 text-[10px]">{t}</Badge>)}
                  </div>
                )}
              </div>
            </div>
            <p className="text-[11px] text-muted-foreground -mt-1">
              Language is auto-detected from the document content.
            </p>

            {/* Markdown split-pane */}
            <div>
              <div className="mb-2 flex items-center justify-between">
                <label className="text-xs text-muted-foreground">Content (Markdown) *</label>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setShowPreview((v) => !v)}
                  className="h-7 gap-1.5 text-xs"
                >
                  {showPreview ? <PenLine className="size-3.5" /> : <Eye className="size-3.5" />}
                  {showPreview ? "Hide preview" : "Show preview"}
                </Button>
              </div>
              <div className={cn("grid gap-3", showPreview && "lg:grid-cols-2")}>
                <Textarea
                  value={content}
                  onChange={(e) => setContent(e.target.value)}
                  placeholder={"# 标题\n\n用 Markdown 编写知识内容…\n\n## 小节\n- 要点 1\n- 要点 2\n\n## 另一小节\n详细说明…"}
                  className="min-h-[40vh] resize-y font-mono text-xs leading-6"
                  aria-label="Markdown content"
                />
                {showPreview && (
                  <div className="min-h-[40vh] max-h-[60vh] overflow-auto rounded-lg border border-border bg-muted/20 p-4">
                    {content.trim() ? (
                      <Markdown>{content}</Markdown>
                    ) : (
                      <p className="text-xs text-muted-foreground">预览将显示在这里…</p>
                    )}
                  </div>
                )}
              </div>
            </div>

            {/* Test retrieval */}
            <div className="rounded-lg border border-border bg-muted/20 p-3">
              <div className="mb-2 flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <Search className="size-3.5" /> Test retrieval
                <span className="text-[10px] font-normal">（仅对已索引的文档有效，保存前可先存草稿再测）</span>
              </div>
              <div className="flex gap-2">
                <Input
                  value={testQuery}
                  onChange={(e) => setTestQuery(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && handleTest()}
                  placeholder="输入一个问题，测试它能否命中你的知识库…"
                  className="h-8 text-xs"
                />
                <Button size="sm" onClick={handleTest} disabled={testing || !testQuery.trim()} className="h-8 gap-1.5 text-xs">
                  {testing ? <Loader2 className="size-3 animate-spin" /> : <Sparkles className="size-3" />}测试
                </Button>
              </div>
              {testSources.length > 0 && (
                <div className="mt-3 space-y-2">
                  {testSources.map((s, i) => (
                    <div key={`${s.title}-${i}`} className="rounded-md border border-border bg-background p-2.5">
                      <div className="mb-1 flex items-center justify-between">
                        <p className="truncate text-xs font-medium">{s.title}</p>
                        <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{((s.score ?? 0) * 100).toFixed(0)}%</Badge>
                      </div>
                      <p className="line-clamp-2 text-xs leading-5 text-muted-foreground">{s.content}</p>
                    </div>
                  ))}
                </div>
              )}
              {testing && <p className="mt-2 text-xs text-muted-foreground">检索中…</p>}
            </div>
          </div>
        </div>

        <DialogFooter className="px-5 py-3 shrink-0">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>取消</Button>
          <Button onClick={handleSave} disabled={!canSave} className="gap-1.5">
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
            {isEdit ? "保存并重新索引" : "创建并索引"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
