"use client";

import * as React from "react";
import useSWR from "swr";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  listWidgetTokens, createWidgetToken, deleteWidgetToken, type WidgetTokenItem,
} from "@/lib/api";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { Globe2, Copy, Plus, Trash2, Code2, Loader2 } from "lucide-react";

export default function WidgetAdminPage() {
  const { t, tf } = useI18n();
  const { data: tokens, isLoading, mutate } = useSWR<WidgetTokenItem[]>("widget-tokens", listWidgetTokens);
  const [name, setName] = React.useState("");
  const [origins, setOrigins] = React.useState("");
  const [color, setColor] = React.useState("#4f46e5");
  const [questions, setQuestions] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [showForm, setShowForm] = React.useState(false);

  const apiBase = (process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1").replace(/\/$/, "");
  const appHost = typeof window !== "undefined" ? window.location.origin : "";

  const create = async () => {
    setBusy(true);
    try {
      const list = origins.split(/[,\s]+/).map((o) => o.trim()).filter(Boolean);
      const qlist = questions.split("\n").map((q) => q.trim()).filter(Boolean).slice(0, 4);
      await createWidgetToken({
        name: name.trim() || undefined,
        allowed_origins: list.length ? list : undefined,
        primary_color: color,
        suggested_questions: qlist.length ? qlist : undefined,
      });
      await mutate();
      setName("");
      setOrigins("");
      setQuestions("");
      setColor("#4f46e5");
      setShowForm(false);
      toast.success(t("widget.createdToast"));
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); }
  };

  const remove = async (id: number) => {
    // Deleting a token instantly kills every third-party site using it —
    // require an explicit confirmation.
    if (!window.confirm(t("widget.deleteConfirm"))) return;
    try { await deleteWidgetToken(id); toast.success(t("widget.deletedToast")); await mutate(); } catch (e) { toast.error((e as Error).message); }
  };

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t("widget.copiedToast"));
    } catch {
      toast.error(t("widget.copyFailed"));
    }
  };

  const embedSnippet = (token: string) =>
    `<script src="${appHost}/widget-embed.js"\n        data-token="${token}"\n        data-api="${apiBase}"\n        data-lang="km" defer></script>`;

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={Globe2}
        kicker={t("widget.kicker")}
        title={t("widget.title")}
        description={t("widget.description")}
        actions={
          <Button size="sm" variant="outline" onClick={() => setShowForm((v) => !v)} className="gap-1.5">
            <Plus className="size-3.5" /> {t("widget.newToken")}
          </Button>
        }
      />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-3xl mx-auto space-y-5">
          <p className="text-xs text-muted-foreground">{t("widget.notice")}</p>

          {showForm && (
            <Card>
              <CardHeader className="pb-3"><CardTitle className="text-sm">{t("widget.createTitle")}</CardTitle></CardHeader>
              <CardContent className="space-y-3">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div>
                    <Label className="text-xs text-muted-foreground">{t("widget.nameLabel")}</Label>
                    <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("widget.namePh")} className="mt-1 h-9 text-sm" />
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground">{t("widget.originsLabel")}</Label>
                    <Input value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="https://shop.example.com" className="mt-1 h-9 text-sm" />
                  </div>
                </div>
                <p className="text-[11px] text-muted-foreground">{t("widget.originsHint")}</p>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div>
                    <Label className="text-xs text-muted-foreground">{t("widget.colorLabel")}</Label>
                    <div className="mt-1 flex items-center gap-2">
                      <input
                        type="color"
                        value={color}
                        onChange={(e) => setColor(e.target.value)}
                        aria-label={t("widget.colorLabel")}
                        className="h-9 w-12 cursor-pointer rounded-md border border-border bg-background p-1"
                      />
                      <code className="text-xs text-muted-foreground">{color}</code>
                    </div>
                  </div>
                  <div>
                    <Label className="text-xs text-muted-foreground">{t("widget.questionsLabel")}</Label>
                    <Textarea
                      value={questions}
                      onChange={(e) => setQuestions(e.target.value)}
                      rows={2}
                      placeholder={t("widget.questionsPh")}
                      className="mt-1 text-sm resize-none"
                    />
                  </div>
                </div>
                <p className="text-[11px] text-muted-foreground">{t("widget.questionsHint")}</p>
                <Button onClick={create} disabled={busy} className="h-8 text-xs gap-1.5">
                  {busy ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />}{t("widget.create")}
                </Button>
              </CardContent>
            </Card>
          )}

          {isLoading ? (
            <div className="text-center py-12"><Loader2 className="size-5 animate-spin text-muted-foreground mx-auto" /></div>
          ) : (tokens ?? []).length === 0 ? (
            <Card className="border-dashed"><CardContent className="flex flex-col items-center gap-3 py-12">
              <p className="text-sm text-muted-foreground">{t("widget.empty")}</p>
              <Button size="sm" onClick={() => setShowForm(true)} disabled={busy} className="h-8 gap-1.5 text-xs">
                <Plus className="size-3.5" />
                {t("widget.newToken")}
              </Button>
            </CardContent></Card>
          ) : (
            (tokens ?? []).map((tk) => (
              <Card key={tk.token_id}>
                <CardContent className="pt-5 space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-medium">{tk.name}</p>
                      <Badge variant="success" className="h-4 text-[10px]">{t("widget.active")}</Badge>
                    </div>
                    <Button size="sm" variant="ghost" className="h-7 text-xs gap-1 text-destructive" onClick={() => void remove(tk.token_id)}>
                      <Trash2 className="size-3" />{t("widget.delete")}
                    </Button>
                  </div>
                  <div>
                    <p className="text-[11px] text-muted-foreground mb-1">{t("widget.tokenLabel")}</p>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 rounded bg-muted px-2 py-1 text-xs break-all">{tk.token}</code>
                      <Button size="icon-sm" variant="outline" onClick={() => copy(tk.token)} title={t("widget.copy")}><Copy className="size-3" /></Button>
                    </div>
                  </div>
                  <div>
                    <p className="text-[11px] text-muted-foreground mb-1 flex items-center gap-1"><Code2 className="size-3" />{t("widget.embedLabel")}</p>
                    <div className="flex items-start gap-2">
                      <pre className="flex-1 overflow-x-auto rounded bg-muted px-2 py-1.5 text-[10px] leading-relaxed text-muted-foreground">{embedSnippet(tk.token)}</pre>
                      <Button size="icon-sm" variant="outline" onClick={() => copy(embedSnippet(tk.token))} title={t("widget.copy")}><Copy className="size-3" /></Button>
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 px-2 py-0.5 text-[10px] text-muted-foreground">
                      <span className="size-2 rounded-full" style={{ background: tk.primary_color }} />
                      {tk.primary_color}
                    </span>
                    <span className="rounded-full border border-border/70 px-2 py-0.5 text-[10px] text-muted-foreground">{tk.theme}</span>
                    {(tk.suggested_questions ?? []).length > 0 && (
                      <span className="rounded-full border border-border/70 px-2 py-0.5 text-[10px] text-muted-foreground">
                        {tf("widget.questionsCount", { n: tk.suggested_questions.length })}
                      </span>
                    )}
                    {(tk.allowed_origins ?? []).length > 0 && (
                      <span className="rounded-full border border-border/70 px-2 py-0.5 text-[10px] text-muted-foreground">
                        {tf("widget.originsCount", { n: tk.allowed_origins.length })}
                      </span>
                    )}
                  </div>
                  {tk.allowed_origins && tk.allowed_origins.length > 0 && (
                    <p className="text-[11px] text-muted-foreground">{tf("widget.allowedFor", { origins: tk.allowed_origins.join(", ") })}</p>
                  )}
                </CardContent>
              </Card>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
