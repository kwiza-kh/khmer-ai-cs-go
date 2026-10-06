"use client";

import * as React from "react";
import { downloadReport } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Loader2, Download, FileSpreadsheet, MessageSquare, Coins } from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { useAuth } from "@/lib/auth-client";

/**
 * CSV report export (sessions / messages; tokens for platform admins only) for
 * a date range. The token export is the platform's cost basis — the same rule
 * the API enforces — so it is not offered to tenant owners.
 */
export function ReportsTab() {
  const { t, tf } = useI18n();
  const { user } = useAuth();
  const isPlatformAdmin = user?.role === "platform_admin";
  const [from, setFrom] = React.useState(() => {
    const d = new Date(); d.setDate(d.getDate() - 30); return d.toISOString().slice(0, 10);
  });
  const [to, setTo] = React.useState(() => new Date().toISOString().slice(0, 10));
  const [busy, setBusy] = React.useState<string | null>(null);

  const run = async (kind: "sessions" | "messages" | "tokens") => {
    if (!from || !to || from > to) { toast.error(t("rp.invalidRange")); return; }
    setBusy(kind);
    try {
      await downloadReport(kind, new Date(`${from}T00:00:00Z`).toISOString(), new Date(`${to}T23:59:59Z`).toISOString());
      toast.success(tf("rp.downloaded", { kind: t(`rp.${kind}`) }));
    } catch (e) { toast.error((e as Error).message); }
    finally { setBusy(null); }
  };

  const reports = [
    { kind: "sessions" as const, labelKey: "rp.sessions", icon: FileSpreadsheet, descKey: "rp.sessionsDesc" },
    { kind: "messages" as const, labelKey: "rp.messages", icon: MessageSquare, descKey: "rp.messagesDesc" },
    ...(isPlatformAdmin
      ? [{ kind: "tokens" as const, labelKey: "rp.tokenUsage", icon: Coins, descKey: "rp.tokensDesc" }]
      : []),
  ];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-2">
            <Download className="size-4 text-primary" /> {t("rp.title")} <span className="text-[10px] text-muted-foreground">{t("rp.yourTenant")}</span>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-2 gap-2 max-w-md">
            <div>
              <label className="text-[11px] text-muted-foreground block mb-1">{t("rp.from")}</label>
              <Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="h-8 text-xs" />
            </div>
            <div>
              <label className="text-[11px] text-muted-foreground block mb-1">{t("rp.to")}</label>
              <Input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="h-8 text-xs" />
            </div>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 max-w-2xl">
            {reports.map((r) => (
              <div key={r.kind} className="rounded-md border border-border p-3 space-y-2">
                <div className="flex items-center gap-2">
                  <r.icon className="size-4 text-muted-foreground" />
                  <p className="text-xs font-medium">{t(r.labelKey)}</p>
                </div>
                <p className="text-[11px] text-muted-foreground">{t(r.descKey)}</p>
                <Button size="sm" variant="outline" className="h-7 text-xs gap-1 w-full" onClick={() => run(r.kind)} disabled={busy !== null}>
                  {busy === r.kind ? <Loader2 className="size-3 animate-spin" /> : <Download className="size-3" />}
                  {busy === r.kind ? t("rp.exporting") : t("rp.exportCsv")}
                </Button>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
