"use client";

import * as React from "react";
import useSWR from "swr";
import {
  getPlatformChannels, getPlatformKnowledgeGaps, getPlatformRevenue, getPlatformSpend, getPlatformSupport, getPlatformTodo,
  retryPlatformChannelFailed,
  type KnowledgeGapTenant, type PlatformChannel, type RevenuePayment, type RevenueTenant, type SupportMessage,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/empty-state";
import {
  AlertTriangle, Building2, Coins, CreditCard, Headset, Loader2, MessageSquare, RefreshCw, RotateCcw, Zap,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { fmtDateTime, fmtInt } from "@/lib/format";
import { toast } from "sonner";

/** Section keys the todo card can jump to. */
export type PlatformSection = "support" | "revenue" | "channels" | "gaps";

function Tile({
  label, value, tone = "muted", onClick, hint,
}: {
  label: string;
  value: number | string;
  tone?: "muted" | "warn" | "danger" | "ok";
  onClick?: () => void;
  hint?: string;
}) {
  const toneClass =
    tone === "danger" ? "text-danger" : tone === "warn" ? "text-warning" : tone === "ok" ? "text-success" : "text-foreground";
  const inner = (
    <>
      <p className="truncate text-[10px] font-medium uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
      <p className={`text-xl font-semibold leading-tight tabular-nums ${toneClass}`}>{value}</p>
      {hint && <p className="truncate text-[10px] text-muted-foreground">{hint}</p>}
    </>
  );
  if (onClick) {
    return (
      <button
        type="button"
        onClick={onClick}
        className="rounded-md border border-border p-2.5 text-left transition-colors hover:border-primary/40 hover:bg-muted/40"
      >
        {inner}
      </button>
    );
  }
  return <div className="rounded-md border border-border p-2.5">{inner}</div>;
}

/**
 * "What needs me today" — the counts from the three ops surfaces, on one line.
 * Every tile jumps to the section that explains it; the numbers come from the
 * same predicates those sections use, so they cannot disagree.
 */
export function TodoCard({ onNavigate }: { onNavigate: (s: PlatformSection) => void }) {
  const { t, tf } = useI18n();
  const { data, error } = useSWR("platform-todo", getPlatformTodo);
  const spend = useSWR("platform-spend", getPlatformSpend);

  const support = data?.support_open ?? 0;
  const channels = data?.channel_errors ?? 0;
  const failed = data?.outbox_failed ?? 0;
  const backlog = data?.outbox_backlog ?? 0;
  const expiring = data?.expiring_paid ?? 0;
  const quota = data?.quota_pressure ?? 0;
  const clear = data && support + channels + failed + expiring + quota === 0;

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Zap className="size-4 text-primary" /> {t("po.todo.title")}
          {clear && <Badge variant="success" className="h-5 px-1.5 text-[10px]">{t("po.todo.allClear")}</Badge>}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {error ? (
          <EmptyState icon={AlertTriangle} title={t("po.todo.failed")} />
        ) : !data ? (
          <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="size-3 animate-spin" /> {t("pa.loadingStats")}</p>
        ) : (
          <>
            <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2">
              <Tile label={t("po.todo.support")} value={fmtInt(support)} tone={support > 0 ? "warn" : "muted"} onClick={() => onNavigate("support")} />
              <Tile label={t("po.todo.channels")} value={fmtInt(channels)} tone={channels > 0 ? "danger" : "ok"} onClick={() => onNavigate("channels")} />
              <Tile label={t("po.todo.outboxFailed")} value={fmtInt(failed)} tone={failed > 0 ? "danger" : "ok"} onClick={() => onNavigate("channels")} />
              <Tile label={t("po.todo.outboxBacklog")} value={fmtInt(backlog)} tone={backlog > 0 ? "warn" : "muted"} onClick={() => onNavigate("channels")} />
              <Tile label={t("po.todo.expiring")} value={fmtInt(expiring)} hint={tf("po.revenue.withinDays", { days: data.lapse_days })} tone={expiring > 0 ? "warn" : "muted"} onClick={() => onNavigate("revenue")} />
              <Tile label={t("po.todo.quota")} value={fmtInt(quota)} tone={quota > 0 ? "warn" : "muted"} />
            </div>
            {/* The spend window is not a count, so it reads as a bar. */}
            {spend.data && (
              <div className="rounded-md border border-border p-2.5">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <p className="text-[10px] font-medium uppercase tracking-[0.1em] text-muted-foreground">
                    {t("po.spend.title")} · {tf("po.spend.window", { minutes: spend.data.window_minutes })}
                  </p>
                  <p className="text-xs tabular-nums">
                    ${spend.data.spent_usd.toFixed(4)}
                    <span className="text-muted-foreground"> / ${spend.data.limit_usd.toFixed(2)}</span>
                    {spend.data.over_gate && <Badge variant="destructive" className="ml-2 h-4 px-1 text-[10px]">{t("po.spend.over")}</Badge>}
                  </p>
                </div>
                <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
                  <div
                    className={`h-full rounded-full ${spend.data.over_gate ? "bg-danger" : "bg-primary"}`}
                    style={{ width: `${Math.min(100, Math.round(spend.data.used_ratio * 100))}%` }}
                  />
                </div>
                <p className="mt-1 text-[10px] text-muted-foreground">
                  {tf("po.spend.gate", { ratio: Math.round(spend.data.gate_ratio * 100) })}
                </p>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** Merchant messages to the platform bot. The reply happens in Telegram. */
export function SupportPanel() {
  const { t } = useI18n();
  const [status, setStatus] = React.useState<"open" | "all">("open");
  const { data, isLoading, mutate } = useSWR(["platform-support", status], () => getPlatformSupport(status));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">{t("po.support.title")}</h3>
          <p className="text-[11px] text-muted-foreground">{t("po.support.hint")}</p>
        </div>
        <div className="flex items-center gap-2">
          {data && (
            <Badge variant={data.open > 0 ? "warning" : "outline"} className="h-6 px-2 text-[11px] font-normal">
              {t("po.support.open")} {data.open} / {data.total}
            </Badge>
          )}
          {(["open", "all"] as const).map((s) => (
            <Button key={s} size="sm" variant={status === s ? "default" : "outline"} className="h-7 px-2 text-[11px]" onClick={() => setStatus(s)}>
              {t(s === "open" ? "po.support.filterOpen" : "po.support.filterAll")}
            </Button>
          ))}
          <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void mutate()} disabled={isLoading}>
            <RefreshCw className={isLoading ? "size-3 animate-spin" : "size-3"} />
          </Button>
        </div>
      </div>

      <Card>
        <CardContent className="pt-4">
          {!data || data.data.length === 0 ? (
            <EmptyState icon={Headset} title={t("po.support.empty")} />
          ) : (
            <div className="divide-y divide-border/60">
              {data.data.map((m: SupportMessage) => (
                <div key={m.message_id} className="flex items-start gap-3 py-2.5">
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-1.5 text-xs font-medium">
                      <span className="truncate">{m.display_name || m.telegram_username || m.telegram_user_id || `#${m.message_id}`}</span>
                      {m.plan && <Badge variant="outline" className="h-4 px-1.5 text-[10px] font-normal">{m.plan}</Badge>}
                      {m.tenant_username ? (
                        <span className="text-[11px] font-normal text-muted-foreground">· {m.tenant_username}</span>
                      ) : (
                        <span className="text-[11px] font-normal text-muted-foreground">· {t("po.support.noTenant")}</span>
                      )}
                      {m.replied_at
                        ? <Badge variant="success" className="h-4 px-1.5 text-[10px] font-normal">{t("po.support.replied")}</Badge>
                        : <Badge variant="warning" className="h-4 px-1.5 text-[10px] font-normal">{t("po.support.waiting")}</Badge>}
                    </p>
                    <p className="mt-0.5 whitespace-pre-wrap break-words text-xs text-foreground">{m.body}</p>
                  </div>
                  <div className="shrink-0 text-right text-[10px] tabular-nums text-muted-foreground">
                    <p>{fmtDateTime(m.created_at)}</p>
                    {m.replied_at && <p>{t("po.support.repliedAt")} {fmtDateTime(m.replied_at)}</p>}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function TenantLapseRow({ row, days }: { row: RevenueTenant; days: number }) {
  const { t, tf } = useI18n();
  // state comes from the API (overdue | expiring | active | no_clock) — the same
  // classification the summary tiles count, so the badge and the count cannot
  // disagree, and this component does not read the clock during render.
  return (
    <tr className="border-t border-border/60">
      <td className="py-1.5 pr-3 text-xs font-medium text-foreground">{row.username}</td>
      <td className="py-1.5 pr-3"><Badge variant="outline" className="h-5 px-1.5 text-[10px] font-normal">{row.plan}</Badge></td>
      <td className="py-1.5 pr-3 text-[11px] tabular-nums text-muted-foreground">
        {row.paid_until ? fmtDateTime(row.paid_until) : t("po.revenue.noClock")}
      </td>
      <td className="py-1.5 pr-3 text-right">
        {row.state === "overdue"
          ? <Badge variant="destructive" className="h-5 px-1.5 text-[10px] font-normal">{t("po.revenue.overdueTag")}</Badge>
          : row.state === "expiring"
            ? <Badge variant="warning" className="h-5 px-1.5 text-[10px] font-normal">{tf("po.revenue.withinDays", { days })}</Badge>
            : null}
      </td>
      <td className="py-1.5 text-right text-[11px] tabular-nums text-muted-foreground">
        {fmtInt(row.messages_used)} / {fmtInt(row.message_quota)}
      </td>
    </tr>
  );
}

/** Captured money and who is about to lapse. */
export function RevenuePanel() {
  const { t, tf } = useI18n();
  const { data, isLoading, mutate } = useSWR("platform-revenue", getPlatformRevenue);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-foreground">{t("po.revenue.title")}</h3>
        <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void mutate()} disabled={isLoading}>
          <RefreshCw className={isLoading ? "size-3 animate-spin" : "size-3"} />
        </Button>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2">
        <Tile label={t("po.revenue.gross")} value={`$${(data?.gross_usd ?? 0).toFixed(2)}`} tone="ok" />
        <Tile label={t("po.revenue.mrr")} value={`$${(data?.mrr_estimate_usd ?? 0).toFixed(2)}`} hint={t("po.revenue.mrrHint")} />
        <Tile label={t("po.revenue.captured")} value={fmtInt(data?.captured_payments)} />
        <Tile label={t("po.revenue.paidTenants")} value={fmtInt(data?.paid_tenants)} />
        <Tile label={t("po.revenue.expiring")} value={fmtInt(data?.expiring_soon)} hint={tf("po.revenue.withinDays", { days: data?.lapse_days ?? 14 })} tone={(data?.expiring_soon ?? 0) > 0 ? "warn" : "muted"} />
        <Tile label={t("po.revenue.overdue")} value={fmtInt(data?.overdue)} tone={(data?.overdue ?? 0) > 0 ? "danger" : "ok"} />
      </div>

      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-sm">{t("po.revenue.tenants")}</CardTitle></CardHeader>
        <CardContent>
          {!data || data.tenants.length === 0 ? (
            <EmptyState icon={CreditCard} title={t("po.revenue.empty")} />
          ) : (
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                  <th className="py-1.5 pr-3">{t("pt.tenant")}</th>
                  <th className="py-1.5 pr-3">{t("pt.plan")}</th>
                  <th className="py-1.5 pr-3">{t("po.revenue.until")}</th>
                  <th className="py-1.5 pr-3 text-right">{t("po.revenue.state")}</th>
                  <th className="py-1.5 text-right">{t("pt.tokens")}</th>
                </tr>
              </thead>
              <tbody>
                {data.tenants.map((row) => <TenantLapseRow key={row.user_id} row={row} days={data.lapse_days} />)}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-sm">{t("po.revenue.recent")}</CardTitle></CardHeader>
        <CardContent>
          {!data || data.recent.length === 0 ? (
            <EmptyState icon={Coins} title={t("po.revenue.empty")} />
          ) : (
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                  <th className="py-1.5 pr-3">{t("po.revenue.date")}</th>
                  <th className="py-1.5 pr-3">{t("pt.tenant")}</th>
                  <th className="py-1.5 pr-3">{t("pt.plan")}</th>
                  <th className="py-1.5 pr-3 text-right">{t("po.revenue.amount")}</th>
                  <th className="py-1.5 text-right">{t("po.revenue.status")}</th>
                </tr>
              </thead>
              <tbody>
                {data.recent.map((p: RevenuePayment) => (
                  <tr key={p.payment_id} className="border-t border-border/60">
                    <td className="py-1.5 pr-3 text-[11px] tabular-nums text-muted-foreground">{fmtDateTime(p.created_at)}</td>
                    <td className="py-1.5 pr-3 font-medium text-foreground">{p.username || `#${p.user_id}`}</td>
                    <td className="py-1.5 pr-3"><Badge variant="outline" className="h-5 px-1.5 text-[10px] font-normal">{p.plan}</Badge></td>
                    <td className="py-1.5 pr-3 text-right tabular-nums">${p.amount.toFixed(2)} {p.currency}</td>
                    <td className="py-1.5 text-right">
                      <Badge variant={p.status === "captured" ? "success" : "secondary"} className="h-5 px-1.5 text-[10px] font-normal">{p.status}</Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** Every tenant's channel state plus its outbound backlog. */
export function ChannelsPanel() {
  const { t, tf } = useI18n();
  const { data, isLoading, mutate } = useSWR("platform-channels", getPlatformChannels);
  const [busy, setBusy] = React.useState<number | null>(null);

  // Requeue this channel's failed sends. The operator reaches for this after
  // fixing the cause — the tenant-side retry button cannot be used on someone
  // else's tenant.
  const retryFailed = async (configID: number) => {
    setBusy(configID);
    try {
      const res = await retryPlatformChannelFailed(configID);
      toast.success(res.requeued > 0 ? tf("po.channels.retried", { n: res.requeued }) : t("po.channels.nothingToRetry"));
      void mutate();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-foreground">{t("po.channels.title")}</h3>
        <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void mutate()} disabled={isLoading}>
          <RefreshCw className={isLoading ? "size-3 animate-spin" : "size-3"} />
        </Button>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
        <Tile label={t("po.channels.total")} value={fmtInt(data?.data.length)} />
        <Tile label={t("po.channels.errors")} value={fmtInt(data?.errors)} tone={(data?.errors ?? 0) > 0 ? "danger" : "ok"} />
        <Tile label={t("po.channels.pending")} value={fmtInt(data?.outbox_pending)} tone={(data?.outbox_pending ?? 0) > 0 ? "warn" : "muted"} />
        <Tile label={t("po.channels.failed")} value={fmtInt(data?.outbox_failed)} tone={(data?.outbox_failed ?? 0) > 0 ? "danger" : "ok"} />
      </div>

      <Card>
        <CardContent className="pt-4">
          {!data || data.data.length === 0 ? (
            <EmptyState icon={Building2} title={t("po.channels.empty")} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[760px] border-collapse text-xs">
                <thead>
                  <tr className="text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                    <th className="py-1.5 pr-3">{t("pt.tenant")}</th>
                    <th className="py-1.5 pr-3">{t("po.channels.platform")}</th>
                    <th className="py-1.5 pr-3">{t("po.channels.status")}</th>
                    <th className="py-1.5 pr-3">{t("po.channels.checked")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("po.channels.pending")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("po.channels.failed")}</th>
                    <th className="py-1.5">{t("po.channels.lastError")}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.data.map((c: PlatformChannel) => (
                    <tr key={c.config_id} className="border-t border-border/60">
                      <td className="py-1.5 pr-3 font-medium text-foreground">{c.username}</td>
                      <td className="py-1.5 pr-3">{c.platform}{c.account_name ? <span className="text-muted-foreground"> · {c.account_name}</span> : null}</td>
                      <td className="py-1.5 pr-3">
                        <Badge
                          variant={c.status === "connected" ? "success" : c.status === "error" ? "destructive" : "secondary"}
                          className="h-5 px-1.5 text-[10px] font-normal"
                          title={c.detail}
                        >
                          {c.status}
                        </Badge>
                      </td>
                      <td className="py-1.5 pr-3 text-[11px] tabular-nums text-muted-foreground">
                        {c.checked_at ? fmtDateTime(c.checked_at) : t("po.channels.never")}
                      </td>
                      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">{fmtInt(c.outbox_pending)}</td>
                      <td className={`py-1.5 pr-3 text-right tabular-nums ${c.outbox_failed > 0 ? "text-danger" : "text-muted-foreground"}`}>
                        {c.outbox_failed > 0 ? (
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-6 gap-1 px-1.5 text-[10px] text-danger"
                            disabled={busy !== null}
                            onClick={() => void retryFailed(c.config_id)}
                            title={c.last_error}
                          >
                            {busy === c.config_id ? <Loader2 className="size-3 animate-spin" /> : <RotateCcw className="size-3" />}
                            {fmtInt(c.outbox_failed)} · {t("po.channels.retryFailed")}
                          </Button>
                        ) : (
                          fmtInt(c.outbox_failed)
                        )}
                      </td>
                      <td className="py-1.5 max-w-[280px] truncate text-[11px] text-muted-foreground" title={c.last_error}>
                        {c.last_error || "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** Per-tenant unanswered questions — the operator's coaching material. */
export function KnowledgeGapsPanel() {
  const { t, tf } = useI18n();
  const [days, setDays] = React.useState(30);
  const { data, isLoading, mutate } = useSWR(["platform-gaps", days], () => getPlatformKnowledgeGaps(days, 3));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">{t("po.gaps.title")}</h3>
          <p className="text-[11px] text-muted-foreground">{t("po.gaps.hint")}</p>
        </div>
        <div className="flex items-center gap-2">
          {data && (
            <Badge variant={data.total_gaps > 0 ? "warning" : "outline"} className="h-6 px-2 text-[11px] font-normal">
              {tf("po.gaps.summary", { tenants: data.tenants_with_gaps, gaps: data.total_gaps })}
            </Badge>
          )}
          {[7, 30, 90].map((d) => (
            <Button key={d} size="sm" variant={days === d ? "default" : "outline"} className="h-7 px-2 text-[11px]" onClick={() => setDays(d)}>
              {d}d
            </Button>
          ))}
          <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void mutate()} disabled={isLoading}>
            <RefreshCw className={isLoading ? "size-3 animate-spin" : "size-3"} />
          </Button>
        </div>
      </div>

      {!data || data.tenants.length === 0 ? (
        <Card>
          <CardContent className="pt-4">
            <EmptyState icon={MessageSquare} title={t("po.gaps.empty")} />
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {data.tenants.map((row: KnowledgeGapTenant) => (
            <Card key={row.user_id}>
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-2 text-sm">
                  {row.username}
                  <Badge variant="outline" className="h-5 px-1.5 text-[10px] font-normal">{row.plan}</Badge>
                  <span className="ml-auto text-[11px] font-normal text-muted-foreground">
                    {tf("po.gaps.tenantTotal", { n: row.gap_total })}
                  </span>
                </CardTitle>
              </CardHeader>
              <CardContent>
                <ol className="space-y-1.5">
                  {row.questions.map((q, i) => (
                    <li key={i} className="flex items-start gap-2 text-xs">
                      <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-accent text-[10px] font-semibold">{i + 1}</span>
                      <span className="min-w-0 flex-1 break-words text-foreground">{q.query}</span>
                      <span className="shrink-0 tabular-nums text-muted-foreground">×{q.hits}</span>
                    </li>
                  ))}
                </ol>
                <p className="mt-2 text-[10px] text-muted-foreground">{t("po.gaps.howto")}</p>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}

