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

/**
 * Platform super-admin console: cross-tenant management.
 * Only users with role = platform_admin can access (backend enforces).
 */
export default function PlatformAdminPage() {
  const [section, setSection] = React.useState("overview");
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={ShieldCheck}
        kicker="Platform"
        title="Platform Admin"
        description="Manage all tenants, plans, quotas and platform-wide usage."
      />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-5xl mx-auto space-y-4">
          <div className="flex flex-wrap gap-2">
            {[
              { key: "overview", label: "Overview", icon: BarChart3 },
              { key: "tenants", label: "Tenants", icon: Building2 },
              { key: "create", label: "Create tenant", icon: Plus },
              { key: "audit", label: "Audit logs", icon: ScrollText },
            ].map((s) => (
              <Button key={s.key} size="sm" variant={section === s.key ? "default" : "outline"} className="gap-1.5 text-xs" onClick={() => setSection(s.key)}>
                <s.icon className="size-3" /> {s.label}
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
  const { data } = useSWR<PlatformAnalytics>("platform-analytics", getPlatformAnalytics);
  if (!data) return <EmptyState icon={BarChart3} title="Loading platform stats…" />;
  const stats = [
    { label: "Tenants", value: data.total_tenants, icon: Building2, sub: `${data.active_tenants} active` },
    { label: "Sessions", value: data.total_sessions, icon: MessageSquare },
    { label: "Messages", value: data.total_messages, icon: MessageSquare },
    { label: "Tokens", value: data.total_tokens.toLocaleString(), icon: Coins },
    { label: "KB documents", value: data.total_documents, icon: FileText },
  ];
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2"><BarChart3 className="size-4 text-primary" /> Platform overview</CardTitle>
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
          <p className="text-xs font-medium text-muted-foreground mb-1.5">Plan distribution</p>
          <div className="flex gap-2 flex-wrap">
            {data.plan_distribution.map((p) => (
              <Badge key={p.plan} variant="outline" className="text-xs capitalize">{p.plan}: {p.count}</Badge>
            ))}
          </div>
        </div>
        <div>
          <p className="text-xs font-medium text-muted-foreground mb-1.5">Messages (last 14 days)</p>
          {data.daily_messages.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">No message activity in this window.</p>
          ) : (
            <div className="flex items-end gap-1.5 h-24">
              {data.daily_messages.slice(-14).map((d) => {
                // Normalize bar height to the window maximum so the tallest day
                // fills the chart and low-volume days stay proportionally short.
                const max = Math.max(...data.daily_messages.slice(-14).map((x) => x.messages), 1);
                const pct = Math.round((d.messages / max) * 100);
                const height = Math.max(2, pct); // floor at 2% so 0-days stay visible
                return (
                  <div key={d.date} className="flex-1 flex flex-col items-center gap-1 group">
                    <div
                      className="w-full rounded-t bg-primary/70 transition-all group-hover:bg-primary"
                      style={{ height: `${height}%` }}
                      title={`${d.date}: ${d.messages} messages`}
                    />
                    <span className="text-[9px] text-muted-foreground tabular-nums">{d.date.slice(5)}</span>
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
  const [q, setQ] = React.useState("");
  const { data, mutate } = useSWR(`platform-tenants-${q}`, () => listTenants({ q: q || undefined, pageSize: 100 }));
  const [detail, setDetail] = React.useState<TenantDetail | null>(null);

  const toggle = async (t: TenantItem) => {
    try {
      await setTenantStatus(t.user_id, !t.is_active);
      toast.success(`${t.username} ${t.is_active ? "disabled" : "enabled"}`);
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };
  const changePlan = async (t: TenantItem, plan: string) => {
    try {
      await setTenantPlan(t.user_id, plan);
      toast.success(`${t.username} → ${plan}`);
      void mutate();
      if (detail?.tenant.user_id === t.user_id) { void globalMutate(`platform-tenant-${t.user_id}`); }
    } catch (e) { toast.error((e as Error).message); }
  };
  const openDetail = async (t: TenantItem) => {
    try {
      const d = await getTenantDetail(t.user_id);
      setDetail(d);
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm flex items-center gap-2"><Building2 className="size-4 text-primary" /> Tenants</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search username or email…" className="h-8 pl-8 text-xs" />
          </div>
          {!data || data.data.length === 0 ? (
            <EmptyState icon={Building2} title="No tenants" />
          ) : (
            <div className="space-y-1.5">
              {data.data.map((t) => (
                <div key={t.user_id} className="rounded-md border border-border p-2.5 space-y-1.5">
                  <div className="flex items-center gap-2">
                    <div className="flex-1 min-w-0">
                      <p className="text-xs font-medium truncate">{t.username} <span className="text-muted-foreground font-normal">· {t.email}</span></p>
                    </div>
                    <Badge variant={t.is_active ? "success" : "destructive"} className="h-4 px-1.5 text-[10px]">{t.is_active ? "active" : "disabled"}</Badge>
                  </div>
                  <div className="flex items-center gap-1.5 flex-wrap">
                    <select value={t.plan} onChange={(e) => changePlan(t, e.target.value)} className="h-6 text-[11px] rounded border border-input bg-transparent px-1.5 capitalize">
                      <option value="free">free</option><option value="pro">pro</option><option value="enterprise">enterprise</option>
                    </select>
                    <Badge variant="outline" className="h-4 px-1 text-[10px]">{t.messages_used}/{t.message_quota} msgs</Badge>
                    <Badge variant="outline" className="h-4 px-1 text-[10px]">{t.docs_used}/{t.doc_quota} docs</Badge>
                    <div className="flex-1" />
                    <Button size="icon-sm" variant="ghost" className="h-6 w-6" title="View" onClick={() => openDetail(t)}><Eye className="size-3.5" /></Button>
                    <Button size="icon-sm" variant="ghost" className="h-6 w-6" title={t.is_active ? "Disable" : "Enable"} onClick={() => toggle(t)}>
                      <Power className={`size-3.5 ${t.is_active ? "text-destructive" : "text-success"}`} />
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
          <CardTitle className="text-sm flex items-center gap-2"><Users className="size-4 text-primary" /> Tenant detail</CardTitle>
        </CardHeader>
        <CardContent>
          {!detail ? (
            <EmptyState icon={Users} title="Select a tenant" description="Click the eye icon to inspect a tenant's sessions." />
          ) : (
            <div className="space-y-2">
              <p className="text-xs font-semibold">{detail.tenant.username} <span className="text-muted-foreground font-normal">({detail.tenant.email})</span></p>
              <div className="grid grid-cols-2 gap-1.5 text-center">
                {(["total_sessions", "total_messages", "total_documents", "messages_used"] as const).map((k) => (
                  <div key={k} className="rounded bg-muted/40 py-1.5">
                    <p className="text-sm font-semibold">{detail.tenant[k]}</p>
                    <p className="text-[10px] text-muted-foreground">{k.replace(/_/g, " ")}</p>
                  </div>
                ))}
              </div>
              <div className="space-y-1">
                <p className="text-[11px] font-medium text-muted-foreground">Recent sessions</p>
                {detail.sessions.length === 0 && <p className="text-[11px] text-muted-foreground">No sessions yet</p>}
                {detail.sessions.slice(0, 8).map((sess) => (
                  <div key={sess.session_id} className="rounded border border-border p-2 flex items-center gap-2">
                    <p className="text-[11px] truncate flex-1">{sess.title || sess.platform || "Untitled"}</p>
                    {sess.sentiment === "negative" && <Badge variant="destructive" className="h-3.5 px-1 text-[9px]">angry</Badge>}
                    <Badge variant="outline" className="h-3.5 px-1 text-[9px]">{sess.status}</Badge>
                    <span className="text-[10px] text-muted-foreground">{sess.user_message_count + sess.model_message_count} msgs</span>
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
  const [username, setUsername] = React.useState("");
  const [email, setEmail] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [plan, setPlan] = React.useState("free");
  const [saving, setSaving] = React.useState(false);

  const submit = async () => {
    if (!username || !email || password.length < 6) { toast.error("Username, email and password (≥6 chars) required"); return; }
    setSaving(true);
    try {
      await createTenant({ username, email, password, plan });
      toast.success("Tenant created");
      setUsername(""); setEmail(""); setPassword(""); setPlan("free");
      void globalMutate(`platform-tenants-`);
      void globalMutate("platform-analytics");
    } catch (e) { toast.error((e as Error).message); }
    finally { setSaving(false); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2"><Plus className="size-4 text-primary" /> Provision a tenant</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <Input placeholder="Username" value={username} onChange={(e) => setUsername(e.target.value)} className="h-8 text-xs" />
          <Input placeholder="Email" value={email} onChange={(e) => setEmail(e.target.value)} className="h-8 text-xs" />
          <Input type="password" placeholder="Password (≥6)" value={password} onChange={(e) => setPassword(e.target.value)} className="h-8 text-xs" />
          <select value={plan} onChange={(e) => setPlan(e.target.value)} className="h-8 text-xs rounded-md border border-input bg-transparent px-2">
            <option value="free">free</option><option value="pro">pro</option><option value="enterprise">enterprise</option>
          </select>
        </div>
        <Button onClick={submit} disabled={saving} className="h-8 text-xs gap-1.5">
          {saving ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />} Create tenant
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
          <span>Admin audit trail ({total})</span>
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
              placeholder="Search action or user…"
              className="h-7 w-56 text-xs"
            />
            <Button size="sm" variant="outline" type="submit" className="h-7 gap-1.5 text-xs">
              <Search className="size-3" /> Search
            </Button>
          </form>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {isLoading ? (
          <div className="flex items-center justify-center py-10 text-sm text-muted-foreground">
            <Loader2 className="mr-2 size-4 animate-spin" /> Loading…
          </div>
        ) : rows.length === 0 ? (
          <EmptyState
            icon={ScrollText}
            title="No audit entries"
            description="Every non-GET mutation on admin surfaces is recorded here automatically."
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-border text-[10px] uppercase tracking-[0.1em] text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Time</th>
                  <th className="py-2 pr-3 font-medium">Admin</th>
                  <th className="py-2 pr-3 font-medium">Action</th>
                  <th className="py-2 pr-3 font-medium">Status</th>
                  <th className="py-2 pr-3 font-medium">IP</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const d = parseAuditDetails(row.details);
                  const ok = (d.status ?? 0) < 400;
                  return (
                    <tr key={row.log_id} className="border-b border-border/50 align-top hover:bg-muted/40">
                      <td className="whitespace-nowrap py-2 pr-3 tabular-nums text-muted-foreground">
                        {row.created_at ? new Date(row.created_at).toLocaleString("en-US", { hour12: false }) : "—"}
                      </td>
                      <td className="py-2 pr-3 font-medium">{row.username || <span className="text-muted-foreground">system</span>}</td>
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
            <span>Page {page} / {totalPages}</span>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" className="h-7 gap-1 text-xs" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
                <ChevronLeft className="size-3" /> Prev
              </Button>
              <Button size="sm" variant="outline" className="h-7 gap-1 text-xs" disabled={page >= totalPages} onClick={() => setPage((p) => Math.min(totalPages, p + 1))}>
                Next <ChevronRight className="size-3" />
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
