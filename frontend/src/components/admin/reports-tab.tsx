"use client";

import * as React from "react";
import { downloadReport } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Loader2, Download, FileSpreadsheet, MessageSquare, Coins } from "lucide-react";
import { toast } from "sonner";

/**
 * CSV report export (sessions / messages / tokens) for a date range.
 */
export function ReportsTab() {
  const [from, setFrom] = React.useState(() => {
    const d = new Date(); d.setDate(d.getDate() - 30); return d.toISOString().slice(0, 10);
  });
  const [to, setTo] = React.useState(() => new Date().toISOString().slice(0, 10));
  const [busy, setBusy] = React.useState<string | null>(null);

  const run = async (kind: "sessions" | "messages" | "tokens") => {
    if (!from || !to || from > to) { toast.error("Invalid date range"); return; }
    setBusy(kind);
    try {
      await downloadReport(kind, new Date(`${from}T00:00:00Z`).toISOString(), new Date(`${to}T23:59:59Z`).toISOString());
      toast.success(`${kind} report downloaded`);
    } catch (e) { toast.error((e as Error).message); }
    finally { setBusy(null); }
  };

  const reports = [
    { kind: "sessions" as const, label: "Sessions", icon: FileSpreadsheet, desc: "会话明细: 平台/状态/情绪/消息数" },
    { kind: "messages" as const, label: "Messages", icon: MessageSquare, desc: "消息明细: 角色/类型/token/模型" },
    { kind: "tokens" as const, label: "Token usage", icon: Coins, desc: "Token 用量: 模型/成本/缓存命中" },
  ];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-2">
            <Download className="size-4 text-primary" /> CSV reports <span className="text-[10px] text-muted-foreground">(your tenant only)</span>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-2 gap-2 max-w-md">
            <div>
              <label className="text-[11px] text-muted-foreground block mb-1">From</label>
              <Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="h-8 text-xs" />
            </div>
            <div>
              <label className="text-[11px] text-muted-foreground block mb-1">To</label>
              <Input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="h-8 text-xs" />
            </div>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 max-w-2xl">
            {reports.map((r) => (
              <div key={r.kind} className="rounded-md border border-border p-3 space-y-2">
                <div className="flex items-center gap-2">
                  <r.icon className="size-4 text-muted-foreground" />
                  <p className="text-xs font-medium">{r.label}</p>
                </div>
                <p className="text-[11px] text-muted-foreground">{r.desc}</p>
                <Button size="sm" variant="outline" className="h-7 text-xs gap-1 w-full" onClick={() => run(r.kind)} disabled={busy !== null}>
                  {busy === r.kind ? <Loader2 className="size-3 animate-spin" /> : <Download className="size-3" />}
                  {busy === r.kind ? "Exporting…" : "Export CSV"}
                </Button>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
