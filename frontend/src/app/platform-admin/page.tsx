"use client";

import * as React from "react";
import useSWR, { mutate as globalMutate } from "swr";
import {
  listTenants, getTenantDetail, setTenantStatus, setTenantPlan, getPlatformAnalytics,
  createTenant, listAuditLogs, TenantItem, TenantDetail, PlatformAnalytics, AuditLogItem,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import {
  Building2, Search, Power, ShieldCheck, Loader2, Plus, Users, MessageSquare, FileText,
  Coins, BarChart3, Eye, ScrollText, ChevronLeft, ChevronRight,
} from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { fmtDateTime, fmtInt } from "@/lib/format";

/**
 * Platform super-admin console: cross-tenant management.
 * Only users with role = platform_admin can access (backend enforces).
 */
export default function PlatformAdminPage() {
  const { t } = useI18n();
  const [section, setSection] = React.useState("overview");
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={ShieldCheck}
        kicker={t("pa.kicker")}
        title={t("pa.title")}
        description={t("pa.desc")}
      />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-5xl mx-auto space-y-4">
          <div className="flex flex-wrap gap-2">
            {[
              { key: "overview", labelKey: "pa.overview", icon: BarChart3 },
              { key: "tenants", labelKey: "pa.tenants", icon: Building2 },
              { key: "create", labelKey: "pa.createTenant", icon: Plus },
              { key: "audit", labelKey: "pa.audit", icon: ScrollText },
            ].map((s) => (
              <Button key={s.key} size="sm" variant={section === s.key ? "default" : "outline"} className="gap-1.5 text-xs" onClick={() => setSection(s.key)}>
                <s.icon className="size-3" /> {t(s.labelKey)}
              </Button>
            ))}
          </div>
          {section === "overview" && <OverviewPanel />}
          {section === "tenants" && <TenantsPanel />}
          {section === "create" && <CreateTenantPanel />}
          {section === "audit" && <AuditLogsPanel />}
        </div>
      </div>
    </div>
  );
}

function OverviewPanel() {
  const { t, tf } = useI18n();
  const { data } = useSWR<PlatformAnalytics>("platform-analytics", getPlatformAnalytics);
  if (!data) return <EmptyState icon={BarChart3} title={t("pa.loadingStats")} />;
  const stats = [
    { label: t("pa.statTenants"), value: fmtInt(data.total_tenants), icon: Building2, sub: tf("pa.activeCount", { n: data.active_tenants }) },
    { label: t("pa.statSessions"), value: fmtInt(data.total_sessions), icon: MessageSquare },
    { label: t("pa.statMessages"), value: fmtInt(data.total_messages), icon: MessageSquare },
    { label: t("pa.statTokens"), value: fmtInt(data.total_tokens), icon: Coins },
    { label: t("pa.statKbDocs"), value: fmtInt(data.total_documents), icon: FileText },
  ];
  // 柱子百分比高度需要一个确定高度的父级 —— 提前算好窗口最大值。
  const window14 = data.daily_messages.slice(-14);
  const maxMessages = Math.max(...window14.map((x) => x.messages), 1);
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2"><BarChart3 className="size-4 text-primary" /> {t("pa.overviewTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2">
          {stats.map((s) => (
            <div key={s.label} className="rounded-md border border-border p-3">
              <s.icon className="size-4 text-muted-foreground" />
              <p className="text-lg font-semibold mt-1">{s.value}</p>
              <p className="text-[11px] text-muted-foreground">{s.label}{s.sub ? ` · ${s.sub}` : ""}</p>
            </div>
          ))}
        </div>
        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1.5">{t("pa.planDistribution")}</p>
          <div className="flex gap-2 flex-wrap">
            {data.plan_distribution.map((p) => (
              <Badge key={p.plan} variant="outline" className="text-xs capitalize">{p.plan}: {p.count}</Badge>
            ))}
          </div>
        </div>
        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1.5">{t("pa.messages14")}</p>
          {data.daily_messages.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">{t("pa.noActivity")}</p>
          ) : (
            <div className="flex items-end gap-1.5 h-24">
              {window14.map((d) => {
                // Normalize bar height to the window maximum so the tallest day
                // fills the chart and low-volume days stay proportionally short.
                const pct = Math.round((d.messages / maxMessages) * 100);
                const height = Math.max(2, pct); // floor at 2% so 0-days stay visible
                return (
                  // h-full + justify-end: 给柱子一个确定高度, 百分比才能解析
                  // (items-end 只会让列按内容收缩 → height:X% 解析为 0)。
                  <div key={d.date} className="flex-1 h-full flex flex-col items-center justify-end gap-1 group">
                    <div
                      className="w-full rounded-t bg-primary/70 transition-all group-hover:bg-primary"
                      style={{ height: `${height}%` }}
                      title={tf("pa.chartTitle", { date: d.date, n: d.messages })}
                    />
                    <span className="text-[10px] text-muted-foreground tabular-nums">{d.date.slice(5)}</span>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function TenantsPanel() {
  const { t, tf } = useI18n();
  const [q, setQ] = React.useState("");
  const { data, mutate } = useSWR(`platform-tenants-${q}`, () => listTenants({ q: q || undefined, pageSize: 100 }));
  const [detail, setDetail] = React.useState<TenantDetail | null>(null);

  const toggle = async (tenant: TenantItem) => {
    try {
      await setTenantStatus(tenant.user_id, !tenant.is_active);
      toast.success(tenant.is_active ? tf("pa.tenantDisabled", { name: tenant.username }) : tf("pa.tenantEnabled", { name: tenant.username }));
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };
  const changePlan = async (tenant: TenantItem, plan: string) => {
    try {
      await setTenantPlan(tenant.user_id, plan);
      toast.success(`${tenant.username} → ${plan}`);
      void mutate();
      if (detail?.tenant.user_id === tenant.user_id) { void globalMutate(`platform-tenant-${tenant.user_id}`); }
    } catch (e) { toast.error((e as Error).message); }
  };
  const openDetail = async (tenant: TenantItem) => {
    try {
      const d = await getTenantDetail(tenant.user_id);
      setDetail(d);
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-2"><Building2 className="size-4 text-primary" /> {t("pa.tenants")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("pa.searchTenantPh")} className="h-8 pl-8 text-xs" />
          </div>
          {!data || data.data.length === 0 ? (
            <EmptyState icon={Building2} title={t("pa.noTenants")} />
          ) : (
            <div className="space-y-1.5">
              {data.data.map((tenant) => (
                <div key={tenant.user_id} className="rounded-md border border-border p-2.5 space-y-1.5">
                  <div className="flex items-center gap-2">
                    <div className="flex-1 min-w-0">
                      <p className="text-xs font-medium truncate">{tenant.username} <span className="text-muted-foreground font-normal">· {tenant.email}</span></p>
                    </div>
                    <Badge variant={tenant.is_active ? "success" : "destructive"} className="h-4 px-1.5 text-[11px]">{tenant.is_active ? t("pa.badgeActive") : t("pa.badgeDisabled")}</Badge>
                  </div>
                  <div className="flex items-center gap-1.5 flex-wrap">
                    <select value={tenant.plan} onChange={(e) => changePlan(tenant, e.target.value)} className="h-6 text-[11px] rounded border border-input bg-transparent px-1.5 capitalize">
                      <option value="free">free</option><option value="pro">pro</option><option value="enterprise">enterprise</option>
                    </select>
                    <Badge variant="outline" className="h-4 px-1 text-[11px]">{tf("pa.msgsBadge", { used: tenant.messages_used, quota: tenant.message_quota })}</Badge>
                    <Badge variant="outline" className="h-4 px-1 text-[11px]">{tf("pa.docsBadge", { used: tenant.docs_used, quota: tenant.doc_quota })}</Badge>
                    <div className="flex-1" />
                    <Button size="icon-sm" variant="ghost" className="h-6 w-6" title={t("pa.view")} onClick={() => openDetail(tenant)}><Eye className="size-3.5" /></Button>
                    <Button size="icon-sm" variant="ghost" className="h-6 w-6" title={tenant.is_active ? t("pa.disable") : t("pa.enable")} onClick={() => toggle(tenant)}>
                      <Power className={`size-3.5 ${tenant.is_active ? "text-danger" : "text-success"}`} />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-2"><Users className="size-4 text-primary" /> {t("pa.tenantDetail")}</CardTitle>
        </CardHeader>
        <CardContent>
          {!detail ? (
            <EmptyState icon={Users} title={t("pa.selectTenant")} description={t("pa.selectTenantDesc")} />
          ) : (
            <div className="space-y-2">
              <p className="text-xs font-semibold">{detail.tenant.username} <span className="text-muted-foreground font-normal">({detail.tenant.email})</span></p>
              <div className="grid grid-cols-2 gap-1.5 text-center">
                {(["total_sessions", "total_messages", "total_documents", "messages_used"] as const).map((k) => (
                  <div key={k} className="rounded bg-muted/40 py-1.5">
                    <p className="text-sm font-semibold">{detail.tenant[k]}</p>
                    <p className="text-[10px] text-muted-foreground">{t(`pa.field${k.replace(/(^|_)([a-z])/g, (_, __, c: string) => c.toUpperCase())}`)}</p>
                  </div>
                ))}
              </div>
              <div className="space-y-1">
                <p className="text-[11px] font-medium text-muted-foreground">{t("pa.recentSessions")}</p>
                {detail.sessions.length === 0 && <p className="text-[11px] text-muted-foreground">{t("pa.noSessions")}</p>}
                {detail.sessions.slice(0, 8).map((sess) => (
                  <div key={sess.session_id} className="rounded border border-border p-2 flex items-center gap-2">
                    <p className="text-[11px] truncate flex-1">{sess.title || sess.platform || t("gr.untitled")}</p>
                    {sess.sentiment === "negative" && <Badge variant="destructive" className="h-3.5 px-1 text-[10px]">{t("inbox.angry")}</Badge>}
                    <Badge variant="outline" className="h-3.5 px-1 text-[10px]">{sess.status}</Badge>
                    <span className="text-[10px] text-muted-foreground">{tf("pa.msgsCount", { n: sess.user_message_count + sess.model_message_count })}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function CreateTenantPanel() {
  const { t } = useI18n();
  const [username, setUsername] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [plan, setPlan] = React.useState("free");
  const [saving, setSaving] = React.useState(false);

  const submit = async () => {
    if (!username || !email || password.length < 6) { toast.error(t("pa.validation")); return; }
    setSaving(true);
    try {
      await createTenant({ username, email, password, plan });
      toast.success(t("pa.created"));
      setUsername(""); setEmail(""); setPassword(""); setPlan("free");
      void globalMutate(`platform-tenants-`);
      void globalMutate("platform-analytics");
    } catch (e) { toast.error((e as Error).message); }
    finally { setSaving(false); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2"><Plus className="size-4 text-primary" /> {t("pa.provision")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <Input placeholder={t("pa.usernamePh")} value={username} onChange={(e) => setUsername(e.target.value)} className="h-8 text-xs" />
          <Input placeholder={t("pa.emailPh")} value={email} onChange={(e) => setEmail(e.target.value)} className="h-8 text-xs" />
          <Input type="password" placeholder={t("pa.passwordPh")} value={password} onChange={(e) => setPassword(e.target.value)} className="h-8 text-xs" />
          <select value={plan} onChange={(e) => setPlan(e.target.value)} className="h-8 text-xs rounded-md border border-input bg-transparent px-2">
            <option value="free">free</option><option value="pro">pro</option><option value="enterprise">enterprise</option>
          </select>
        </div>
        <Button onClick={submit} disabled={saving} className="h-8 text-xs gap-1.5">
          {saving ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />} {t("pa.createTenant")}
        </Button>
      </CardContent>
    </Card>
  );
}

// ============================================
// Audit logs — platform-wide admin mutation trail.
// ============================================

function parseAuditDetails(raw?: string): { method?: string; path?: string; status?: number } {
  if (!raw) return {};
  try {
    const v = JSON.parse(raw);
    return { method: v.method, path: v.path, status: v.status };
  } catch {
    return {};
  }
}

function AuditLogsPanel() {
  const { t, tf } = useI18n();
  const [q, setQ] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const pageSize = 50;
  const { data, isLoading } = useSWR(
    `platform-audit-${query}-${page}`,
    () => listAuditLogs({ q: query || undefined, page, pageSize }),
  );
  const rows: AuditLogItem[] = data?.data ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex flex-wrap items-center justify-between gap-3 text-sm">
          <span>{tf("pa.auditTitle", { n: total })}</span>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              setPage(1);
              setQuery(q.trim());
            }}
          >
            <Input
              value={q}
              onChange={(event) => setQ(event.target.value)}
              placeholder={t("pa.auditSearchPh")}
              className="h-7 w-56 text-xs"
            />
            <Button size="sm" variant="outline" type="submit" className="h-7 gap-1.5 text-xs">
              <Search className="size-3" /> {t("nav.search")}
            </Button>
          </form>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {isLoading ? (
          <div className="flex items-center justify-center py-10 text-sm text-muted-foreground">
            <Loader2 className="mr-2 size-4 animate-spin" /> {t("settings.loading")}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState
            icon={ScrollText}
            title={t("pa.noAudit")}
            description={t("pa.noAuditDesc")}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-border text-[10px] uppercase tracking-[0.1em] text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">{t("pa.colTime")}</th>
                  <th className="py-2 pr-3 font-medium">{t("pa.colAdmin")}</th>
                  <th className="py-2 pr-3 font-medium">{t("pa.colAction")}</th>
                  <th className="py-2 pr-3 font-medium">{t("pa.colStatus")}</th>
                  <th className="py-2 pr-3 font-medium">{t("pa.colIp")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const d = parseAuditDetails(row.details);
                  const ok = (d.status ?? 0) < 400;
                  return (
                    <tr key={row.log_id} className="border-b border-border/50 align-top hover:bg-muted/40">
                      <td className="whitespace-nowrap py-2 pr-3 tabular-nums text-muted-foreground">
                        {row.created_at ? fmtDateTime(row.created_at) : "—"}
                      </td>
                      <td className="py-2 pr-3 font-medium">{row.username || <span className="text-muted-foreground">{t("pa.system")}</span>}</td>
                      <td className="py-2 pr-3">
                        <span className="font-mono text-[11px]">{row.action}</span>
                        {d.path && d.path !== row.action && (
                          <span className="ml-1 text-muted-foreground">{d.path}</span>
                        )}
                      </td>
                      <td className="py-2 pr-3">
                        <Badge variant={ok ? "success" : "destructive"} className="h-5 px-1.5 text-[10px]">
                          {d.status ?? "?"}
                        </Badge>
                      </td>
                      <td className="py-2 pr-3 font-mono text-[11px] text-muted-foreground">{row.ip_address || "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {totalPages > 1 && (
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>{tf("pa.pageN", { page, total: totalPages })}</span>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" className="h-7 gap-1 text-xs" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
                <ChevronLeft className="size-3" /> {t("pa.prev")}
              </Button>
              <Button size="sm" variant="outline" className="h-7 gap-1 text-xs" disabled={page >= totalPages} onClick={() => setPage((p) => Math.min(totalPages, p + 1))}>
                {t("pa.next")} <ChevronRight className="size-3" />
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
