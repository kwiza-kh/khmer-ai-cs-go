"use client";

import * as React from "react";
import useSWR from "swr";
import {
  AreaChart, Area, BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid,
  PieChart, Pie, Cell, Legend,
} from "recharts";
import {
  getAnalyticsOverview, getAnalyticsTimeline, getLanguageBreakdown, getTopQueries,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { StatCard } from "@/components/stat-card";
import { StatCards, type StatCardData } from "@/components/spectrumui/charts/stat-cards";
import { EmptyState } from "@/components/empty-state";
import { Zap, TrendingUp, BarChart3, Users, Clock, Smile, ThumbsUp, ThumbsDown, Activity, ShieldCheck } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { fmtInt, fmtMoney } from "@/lib/format";

// 6 distinct token-driven colors (cycle through semantic tokens).
const PIE_COLORS = ["var(--color-primary)", "var(--color-info)", "var(--color-warning)", "var(--color-success)", "var(--color-danger)", "var(--color-muted-foreground)"];

export function AnalyticsPanel({ days = 30 }: { days?: number }) {
  const { t, tf } = useI18n();
  const { data: overview } = useSWR(`analytics-overview-${days}`, () => getAnalyticsOverview(days));
  const { data: timeline } = useSWR(`analytics-timeline-${days}`, () => getAnalyticsTimeline(days));
  const { data: languages } = useSWR(`analytics-languages-${days}`, () => getLanguageBreakdown(days));
  const { data: topQueries } = useSWR(`analytics-top-${days}`, () => getTopQueries(days, 8));

  // Spectrum UI stat cards (registry: @spectrumui/stat-cards, vendored with
  // token colors) drive the trend row: real per-day series + a delta sentence
  // computed against the period's first day.
  const trendCards: StatCardData[] = React.useMemo(() => {
    if (!timeline?.length) return [];
    const vsLabel = tf("an.vsPrev", { days });
    return [
      {
        label: t("an.totalTokens"),
        series: timeline.map((p) => p.tokens),
        format: (v) => fmtInt(v),
        caption: t("an.perDay"),
        deltaLabel: vsLabel,
      },
      {
        label: t("an.sessions"),
        series: timeline.map((p) => p.sessions),
        format: (v) => fmtInt(v),
        caption: t("an.perDay"),
        deltaLabel: vsLabel,
      },
      {
        label: t("an.estCost"),
        series: timeline.map((p) => p.cost),
        format: (v) => fmtMoney(v, 4),
        goodWhen: "down",
        caption: t("an.perDay"),
        deltaLabel: vsLabel,
      },
      {
        label: t("an.deflection"),
        series: timeline.map((p) => +((p.deflection_rate ?? 0) * 100).toFixed(1)),
        format: (v) => `${v.toFixed(1)}%`,
        caption: t("an.perDay"),
        deltaLabel: vsLabel,
      },
    ];
  }, [timeline, t, tf, days]);

  // Highest-volume day gets the dark bar (the reference chart's single
  // emphasis) — everything else stays on chart-1's near-neutral gray.
  const peakSessionDay = React.useMemo(() => {
    if (!timeline?.length) return -1;
    let idx = 0;
    timeline.forEach((p, i) => { if (p.sessions > timeline[idx].sessions) idx = i; });
    return idx;
  }, [timeline]);
  const totalSessions = React.useMemo(
    () => (timeline ?? []).reduce((sum, p) => sum + (p.sessions ?? 0), 0),
    [timeline],
  );

  return (
    <div className="space-y-4">
      {/* KPI cards — exactly 8 so the 4-col grid never leaves an orphan row. */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <StatCard icon={Zap} label={t("an.totalTokens")} value={fmtInt(overview?.total_tokens)} tone="success" />
        <StatCard icon={TrendingUp} label={t("an.cacheHit")} value={`${(overview?.cache_hit_rate ?? 0).toFixed(1)}%`} tone="info" />
        <StatCard icon={BarChart3} label={t("an.estCost")} value={fmtMoney(overview?.total_cost, 4)} tone="warning" />
        <StatCard icon={Users} label={t("an.sessions")} value={fmtInt(overview?.total_sessions)} tone="default" />
        <StatCard icon={Clock} label={t("an.avgFirstResp")} value={fmtMs(overview?.avg_first_response_ms)} tone="info" />
        <StatCard icon={Activity} label={t("an.avgResolution")} value={fmtMs(overview?.avg_resolution_ms)} tone="warning" />
        <StatCard icon={Smile} label="CSAT" value={overview?.csat != null ? `${(overview.csat * 100).toFixed(0)}%` : "—"} tone="success" />
        <StatCard icon={ShieldCheck} label={t("an.deflection")} value={`${((overview?.deflection_rate ?? 0) * 100).toFixed(1)}%`} tone="success" />
      </div>

      {/* Trend row — Spectrum UI's stat cards: per-day sparkline, tweened
          headline value and a delta sentence against the period's start. */}
      {trendCards.length > 0 && (
        <div className="space-y-2">
          <p className="px-0.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground/70">
            {tf("an.trend", { days })}
          </p>
          <StatCards cards={trendCards} columns={4} />
        </div>
      )}

      {/* Sessions per day — the reference layout's emphasised bar chart:
          near-neutral bars, one dark peak, rotated date axis. */}
      <Card>
        <CardHeader className="pb-2">
          <div className="flex items-center justify-between gap-3">
            <CardTitle className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              {t("an.dailySessions")}
            </CardTitle>
            <span className="text-[12px] tabular-nums text-muted-foreground">
              {fmtInt(totalSessions)} · {t("an.inPeriod")}
            </span>
          </div>
        </CardHeader>
        <CardContent>
          <div className="h-64">
            {!timeline?.length ? (
              <EmptyState icon={BarChart3} title={t("an.noUsage")} />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={timeline} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <CartesianGrid vertical={false} stroke="var(--color-border)" strokeDasharray="3 3" />
                  <XAxis
                    dataKey="date"
                    tick={{ fontSize: 10 }}
                    angle={-45}
                    textAnchor="end"
                    height={54}
                    stroke="var(--color-muted-foreground)"
                  />
                  <YAxis tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" allowDecimals={false} />
                  <Tooltip
                    cursor={{ fill: "var(--color-muted)", opacity: 0.5 }}
                    contentStyle={{
                      background: "var(--color-popover)",
                      border: "1px solid var(--color-border)",
                      borderRadius: 10,
                      fontSize: 12,
                      color: "var(--color-popover-foreground)",
                    }}
                  />
                  <Bar dataKey="sessions" radius={[5, 5, 0, 0]}>
                    {timeline.map((point, i) => (
                      <Cell
                        key={point.date}
                        fill={i === peakSessionDay ? "var(--color-foreground)" : "var(--color-chart-1)"}
                      />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Token usage over time */}
      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-sm">{tf("an.tokenUsage", { days })}</CardTitle></CardHeader>
        <CardContent>
          <div className="h-64">
            {!timeline?.length ? (
              <EmptyState icon={BarChart3} title={t("an.noUsage")} />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={timeline} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <defs>
                    <linearGradient id="tokensGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-primary)" stopOpacity={0.4} />
                      <stop offset="95%" stopColor="var(--color-primary)" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--color-border)" />
                  <XAxis dataKey="date" tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                  <YAxis tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                  <Tooltip
                    contentStyle={{
                      background: "var(--color-popover)",
                      border: "1px solid var(--color-border)",
                      borderRadius: 6,
                      fontSize: 12,
                      color: "var(--color-popover-foreground)",
                    }}
                  />
                  <Area type="monotone" dataKey="tokens" stroke="var(--color-primary)" fill="url(#tokensGrad)" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Deflection rate over time — share of each day's sessions resolved
          without staying open. The core ROI metric for AI-first CS. */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-1.5">
            <ShieldCheck className="size-3.5 text-success" />
            {tf("an.deflectionTitle", { days })}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="h-56">
            {!timeline?.length || timeline.every((t) => !t.deflection_rate) ? (
              <EmptyState icon={ShieldCheck} title={t("an.noResolution")} />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart
                  data={timeline.map((t) => ({ date: t.date, deflection: +((t.deflection_rate ?? 0) * 100).toFixed(1) }))}
                  margin={{ top: 8, right: 8, left: 0, bottom: 0 }}
                >
                  <defs>
                    <linearGradient id="deflectGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-success)" stopOpacity={0.4} />
                      <stop offset="95%" stopColor="var(--color-success)" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--color-border)" />
                  <XAxis dataKey="date" tick={{ fontSize: 10 }} stroke="var(--color-muted-foreground)" />
                  <YAxis
                    tick={{ fontSize: 10 }}
                    stroke="var(--color-muted-foreground)"
                    domain={[0, 100]}
                    tickFormatter={(v) => `${v}%`}
                  />
                  <Tooltip
                    contentStyle={{
                      background: "var(--color-popover)",
                      border: "1px solid var(--color-border)",
                      borderRadius: 6,
                      fontSize: 12,
                      color: "var(--color-popover-foreground)",
                    }}
                    formatter={(v) => `${v}%`}
                  />
                  <Area type="monotone" dataKey="deflection" stroke="var(--color-success)" fill="url(#deflectGrad)" strokeWidth={2} />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
          <p className="text-xs text-muted-foreground mt-1">
            {t("an.deflectionDesc")}
          </p>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {/* Languages pie */}
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm">{t("an.languages")}</CardTitle></CardHeader>
          <CardContent>
            <div className="h-56">
              {!languages?.length ? (
                <EmptyState icon={Users} title={t("an.noData")} />
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <PieChart>
                    <Pie data={languages} dataKey="count" nameKey="language" cx="50%" cy="50%" innerRadius={45} outerRadius={70} paddingAngle={2}>
                      {languages.map((_, i) => <Cell key={i} fill={PIE_COLORS[i % PIE_COLORS.length]} />)}
                    </Pie>
                    <Legend wrapperStyle={{ fontSize: 13 }} />
                    <Tooltip contentStyle={{ background: "var(--color-popover)", border: "1px solid var(--color-border)", borderRadius: 6, fontSize: 13 }} />
                  </PieChart>
                </ResponsiveContainer>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Top queries */}
        <Card>
          <CardHeader className="pb-2"><CardTitle className="text-sm">{t("an.topQuestions")}</CardTitle></CardHeader>
          <CardContent>
            {!topQueries?.length ? (
              <EmptyState icon={Activity} title={t("an.noQuestions")} />
            ) : (
              <ol className="space-y-2">
                {topQueries.map((q, i) => (
                  <li key={i} className="flex items-start gap-2 text-xs">
                    <span className="flex-shrink-0 size-5 rounded-full bg-accent text-foreground inline-flex items-center justify-center text-xs font-semibold">{i + 1}</span>
                    <span className="flex-1 truncate text-foreground">{q.query || t("an.empty")}</span>
                    <span className="text-muted-foreground">×{q.count}</span>
                  </li>
                ))}
              </ol>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function fmtMs(ms?: number): string {
  if (!ms || ms <= 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

// Unused-icon imports kept for potential future use — silence TS noUnusedLocals.
void ThumbsUp; void ThumbsDown;
