"use client";

import * as React from "react";
import useSWR from "swr";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  listWidgetTokens, createWidgetToken, deleteWidgetToken, type WidgetTokenItem,
} from "@/lib/api";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { Globe2, Copy, Plus, Trash2, Code2, Loader2 } from "lucide-react";

export default function WidgetAdminPage() {
  const { t, tf } = useI18n();
  const { data: tokens, isLoading, mutate } = useSWR<WidgetTokenItem[]>("widget-tokens", listWidgetTokens);
  const [creating, setCreating] = React.useState(false);
  const [name, setName] = React.useState("");
  const [origins, setOrigins] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [showForm, setShowForm] = React.useState(false);

  const apiBase = (process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1").replace(/\/$/, "");
  const appHost = typeof window !== "undefined" ? window.location.origin : "";

  const create = async () => {
    setBusy(true);
    setCreating(true);
    try {
      const list = origins.split(/[,\s]+/).map((o) => o.trim()).filter(Boolean);
      await createWidgetToken({ name: name.trim() || undefined, allowed_origins: list.length ? list : undefined });
      await mutate();
      setName("");
      setOrigins("");
      setShowForm(false);
      toast.success(t("widget.createdToast"));
    } catch (e) { toast.error((e as Error).message); } finally { setBusy(false); setCreating(false); }
  };

  const remove = async (id: number) => {
    try { await deleteWidgetToken(id); await mutate(); } catch (e) { toast.error((e as Error).message); }
  };

  const copy = (text: string) => {
    void navigator.clipboard.writeText(text);
    toast.success(t("widget.copiedToast"));
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
                <Button onClick={create} disabled={busy || creating} className="h-8 text-xs gap-1.5">
                  {busy ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />}{t("widget.create")}
                </Button>
              </CardContent>
            </Card>
          )}

          {isLoading ? (
            <div className="text-center py-12"><Loader2 className="size-5 animate-spin text-muted-foreground mx-auto" /></div>
          ) : (tokens ?? []).length === 0 ? (
            <Card className="border-dashed"><CardContent className="py-12 text-center text-sm text-muted-foreground">
              {t("widget.empty")}
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
