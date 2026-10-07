"use client";

import * as React from "react";
import useSWR from "swr";
import {
  Area, Bar, BarChart, CartesianGrid, Cell, ComposedChart, Legend, Line, Pie, PieChart,
  ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { getPlatformTokens, type PlatformTokens } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/empty-state";
import { Activity, Coins, Database, RefreshCw, Users, Zap } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { fmtDateTime, fmtInt, fmtMoney } from "@/lib/format";

// Token-driven colors, same set the tenant analytics panel uses.
const PIE_COLORS = [
  "var(--color-primary)", "var(--color-info)", "var(--color-warning)",
  "var(--color-success)", "var(--color-danger)", "var(--color-muted-foreground)",
];

const RANGES = [7, 30, 90] as const;

const tooltipStyle = {
  background: "var(--color-popover)",
  border: "1px solid var(--color-border)",
  borderRadius: 10,
  fontSize: 12,
  color: "var(--color-popover-foreground)",
};

/**
 * Platform token board — the cross-tenant god view: totals, per-day tokens and
 * cost, the model mix, and every tenant (including the ones that never spent a
 * token, which is the row an operator actually acts on).
 *
 * One request feeds all of it (`GET /api/v1/platform/tokens`), so the views can
 * never disagree with each other. Backend is platformAdminOnly and read-only.
 */
export function PlatformTokensPanel() {
  const { t, tf } = useI18n();
  const [days, setDays] = React.useState<number>(30);
  const [q, setQ] = React.useState("");
  const { data, isLoading, mutate } = useSWR<PlatformTokens>(
    ["platform-tokens", days],
    () => getPlatformTokens(days),
  );

  const totals = data?.totals;
  const tenants = React.useMemo(() => {
    const needle = q.trim().toLowerCase();
    const rows = data?.by_tenant ?? [];
    return needle ? rows.filter((r) => r.username.toLowerCase().includes(needle)) : rows;
  }, [data, q]);
  const withUsage = (data?.by_tenant ?? []).filter((r) => r.calls > 0).length;
  const topTenants = React.useMemo(
    () => (data?.by_tenant ?? []).filter((r) => r.tokens > 0).slice(0, 10),
    [data],
  );
  const avgPerDay = data && data.days > 0 ? Math.round((totals?.tokens ?? 0) / data.days) : 0;
  const modelMix = (data?.by_model ?? []).filter((m) => m.tokens > 0);

  const kpis = [
    { label: t("pt.totalTokens"), value: fmtInt(totals?.tokens), icon: Zap, tone: "text-success" },
    { label: t("pt.totalCost"), value: fmtMoney(totals?.cost, 4), icon: Coins, tone: "text-warning" },
    { label: t("pt.calls"), value: fmtInt(totals?.calls), icon: Activity, tone: "text-info" },
    { label: t("pt.cacheHit"), value: `${(totals?.cache_hit_rate ?? 0).toFixed(1)}%`, icon: Database, tone: "text-info" },
    { label: t("pt.activeTenants"), value: `${withUsage} / ${data?.by_tenant.length ?? 0}`, icon: Users, tone: "text-foreground" },
    { label: t("pt.avgPerDay"), value: fmtInt(avgPerDay), icon: Zap, tone: "text-muted-foreground" },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold text-foreground">{t("pt.title")}</h3>
          <p className="text-[11px] text-muted-foreground">{t("pt.hint")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="outline" className="h-6 px-2 text-[11px] font-normal text-muted-foreground">
            {tf("pt.days", { days })}
          </Badge>
          {RANGES.map((d) => (
            <Button
              key={d}
              size="sm"
              variant={days === d ? "default" : "outline"}
              className="h-7 px-2 text-[11px]"
              onClick={() => setDays(d)}
            >
              {d}d
            </Button>
          ))}
          <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-[11px]" onClick={() => void mutate()} disabled={isLoading}>
            <RefreshCw className={isLoading ? "size-3 animate-spin" : "size-3"} />
          </Button>
        </div>
      </div>

      {/* KPI row — 6 tiles, 3 per row on md so nothing is an orphan. */}
      <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
        {kpis.map((k) => (
          <Card key={k.label}>
            <CardContent className="flex items-center gap-3 p-3">
              <div className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted">
                <k.icon className={`size-[15px] ${k.tone}`} />
              </div>
              <div className="min-w-0">
                <p className="truncate text-[11px] font-medium uppercase tracking-[0.1em] text-muted-foreground">{k.label}</p>
                <p className="text-lg font-semibold leading-tight tabular-nums">{k.value}</p>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* Daily tokens (area, left axis) + cost (line, right axis). */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">{t("pt.daily")}</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="h-64">
            {!data?.daily.length ? (
              <EmptyState icon={Activity} title={t("pt.noUsage")} />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <ComposedChart data={data.daily} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <defs>
                    <linearGradient id="ptTokensGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-primary)" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="var(--color-primary)" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--color-border)" vertical={false} />
                  <XAxis dataKey="date" tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                  <YAxis yAxisId="tokens" tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                  <YAxis yAxisId="cost" orientation="right" tick={{ fontSize: 10 }} stroke="var(--color-warning)" />
                  <Tooltip contentStyle={tooltipStyle} />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                  <Area
                    yAxisId="tokens"
                    type="monotone"
                    dataKey="tokens"
                    name={t("pt.tokens")}
                    stroke="var(--color-primary)"
                    fill="url(#ptTokensGrad)"
                    strokeWidth={2}
                  />
                  <Line
                    yAxisId="cost"
                    type="monotone"
                    dataKey="cost"
                    name={t("pt.cost")}
                    stroke="var(--color-warning)"
                    strokeWidth={2}
                    dot={false}
                  />
                </ComposedChart>
              </ResponsiveContainer>
            )}
          </div>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Model mix: which model is actually carrying the load. */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("pt.byModel")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="h-64">
              {!modelMix.length ? (
                <EmptyState icon={Database} title={t("pt.noUsage")} />
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <PieChart>
                    <Pie
                      data={modelMix}
                      dataKey="tokens"
                      nameKey="model"
                      cx="50%"
                      cy="50%"
                      innerRadius={50}
                      outerRadius={80}
                      paddingAngle={2}
                    >
                      {modelMix.map((_, i) => (
                        <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                      ))}
                    </Pie>
                    <Legend wrapperStyle={{ fontSize: 12 }} />
                    <Tooltip contentStyle={tooltipStyle} formatter={(v) => fmtInt(Number(v))} />
                  </PieChart>
                </ResponsiveContainer>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Who is spending: the actionable half of the board. */}
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("pt.topTenants")}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="h-64">
              {!topTenants.length ? (
                <EmptyState icon={Users} title={t("pt.noUsage")} />
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={topTenants} layout="vertical" margin={{ top: 4, right: 12, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="var(--color-border)" horizontal={false} />
                    <XAxis type="number" tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                    <YAxis type="category" dataKey="username" width={92} tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                    <Tooltip
                      contentStyle={tooltipStyle}
                      formatter={(v) => fmtInt(Number(v))}
                      labelFormatter={(label) => {
                        const row = topTenants.find((r) => r.username === label);
                        return row ? `${label} · ${fmtMoney(row.cost, 4)}` : String(label);
                      }}
                    />
                    <Bar dataKey="tokens" name={t("pt.tokens")} radius={[0, 4, 4, 0]}>
                      {topTenants.map((_, i) => (
                        <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />
                      ))}
                    </Bar>
                  </BarChart>
                </ResponsiveContainer>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Every tenant, including the silent ones. */}
      <Card>
        <CardHeader className="pb-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-sm">{t("pt.allTenants")}</CardTitle>
            <Input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={t("pt.search")}
              className="h-7 w-48 text-xs"
            />
          </div>
        </CardHeader>
        <CardContent>
          {!tenants.length ? (
            <EmptyState icon={Users} title={t("pt.noUsage")} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] border-collapse text-xs">
                <thead>
                  <tr className="text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                    <th className="py-1.5 pr-3">{t("pt.tenant")}</th>
                    <th className="py-1.5 pr-3">{t("pt.plan")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("pt.tokens")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("pt.cost")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("pt.calls")}</th>
                    <th className="py-1.5 pr-3 text-right">{t("pt.cacheHit")}</th>
                    <th className="py-1.5 pr-3">{t("pt.lastCall")}</th>
                  </tr>
                </thead>
                <tbody>
                  {tenants.map((row) => (
                    <tr key={row.user_id} className="border-t border-border/60">
                      <td className="py-1.5 pr-3 font-medium text-foreground">{row.username}</td>
                      <td className="py-1.5 pr-3">
                        <Badge variant="outline" className="h-5 px-1.5 text-[10px] font-normal">
                          {row.role === "platform_admin" ? t("role.platformAdmin") : row.plan}
                        </Badge>
                      </td>
                      <td className="py-1.5 pr-3 text-right tabular-nums">{fmtInt(row.tokens)}</td>
                      <td className="py-1.5 pr-3 text-right tabular-nums text-warning">{fmtMoney(row.cost, 4)}</td>
                      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">{fmtInt(row.calls)}</td>
                      <td className="py-1.5 pr-3 text-right tabular-nums text-muted-foreground">
                        {row.calls > 0 ? `${row.cache_hit_rate.toFixed(1)}%` : "—"}
                      </td>
                      <td className="py-1.5 pr-3 text-muted-foreground">
                        {row.last_call_at ? fmtDateTime(row.last_call_at) : t("pt.never")}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="mt-2 text-[11px] text-muted-foreground">{t("pt.zeroHint")}</p>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
