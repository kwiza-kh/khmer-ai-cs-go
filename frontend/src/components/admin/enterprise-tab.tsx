"use client";

import * as React from "react";
import useSWR from "swr";
import {
  listSlaPolicies, upsertSlaPolicy, deleteSlaPolicy, listSlaBreaches,
  listRoutingRules, upsertRoutingRule, deleteRoutingRule,
  listMacros, createMacro, deleteMacro,
  listRoles, createRole, deleteRole,
  listWebhookSubscriptions, createWebhookSubscription, deleteWebhookSubscription,
  getAgentPerformance, getIntentAnalytics, getIntegrationStatus,
  type SlaPolicy, type RoutingRule, type Macro, type Role, type WebhookSubscription,
  type AgentPerformance, type IntentCount, type IntegrationStatus,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState } from "@/components/empty-state";
import { toast } from "sonner";
import {
  AlarmClock, GitBranch, ListChecks, ShieldCheck, Webhook, BarChart3, Plug, Trash2, Plus,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { confirmDelete } from "@/lib/confirm-delete";

const PERMISSION_OPTIONS = [
  "inbox.view", "inbox.reply", "inbox.assign", "inbox.status",
  "knowledge.manage", "sla.view", "sla.manage", "routing.manage", "macros.manage",
  "roles.manage", "webhooks.manage", "customers.view", "reports.export",
  "billing.manage", "team.manage", "campaigns.manage", "admin.analytics", "admin.models",
];

const EVENT_OPTIONS = [
  "session.created", "message.received", "message.sent", "handoff.requested",
  "handoff.assigned", "session.resolved", "sentiment.negative",
];

/** 小号可见标签 + 控件, 用于紧凑表单行。 */
function LabeledField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="truncate text-[11px] font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function SectionCard({ title, children, action }: { title: string; children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-2 pb-3">
        <CardTitle className="text-sm">{title}</CardTitle>
        {action}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

// ---------------- SLA ----------------
function SlaPanel() {
  const { t, tf } = useI18n();
  const { data, mutate } = useSWR<SlaPolicy[]>("sla-policies", listSlaPolicies);
  const { data: breaches } = useSWR("sla-breaches", () => listSlaBreaches(true));
  const [name, setName] = React.useState("");
  const [first, setFirst] = React.useState(300);
  const [resolution, setResolution] = React.useState(3600);

  const submit = async () => {
    if (!name.trim()) { toast.error(t("ent.nameRequired")); return; }
    try {
      await upsertSlaPolicy({ name, first_response_secs: first, resolution_secs: resolution });
      await mutate();
      setName("");
      toast.success(t("ent.slaSaved"));
    } catch (e) { toast.error((e as Error).message); }
  };

  const policies = data ?? [];
  return (
    <div className="space-y-4">
      <SectionCard title={t("ent.slaTitle")} action={<AlarmClock className="size-4 text-muted-foreground" />}>
        {/* 这三个输入框都有预填值, 所以 placeholder 不会显示 —— 必须有可见标签,
            否则用户只看到裸的 300 / 3600, 不知道单位。 */}
        <div className="mb-3 grid items-end gap-2 sm:grid-cols-2 lg:grid-cols-4">
          <LabeledField label={t("ent.colName")}>
            <Input placeholder={t("ent.namePh")} value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
          </LabeledField>
          <LabeledField label={t("ent.firstRespPh")}>
            <Input type="number" value={first} onChange={(e) => setFirst(Number(e.target.value))} className="h-8 text-xs" />
          </LabeledField>
          <LabeledField label={t("ent.resolutionPh")}>
            <Input type="number" value={resolution} onChange={(e) => setResolution(Number(e.target.value))} className="h-8 text-xs" />
          </LabeledField>
          <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />{t("ent.add")}</Button>
        </div>
        {policies.length === 0 ? <EmptyState icon={AlarmClock} title={t("ent.noSla")} /> : (
          <Table>
            <TableHeader><TableRow>
              <TableHead className="text-xs">{t("ent.colName")}</TableHead><TableHead className="text-xs">{t("ent.colFirst")}</TableHead>
              <TableHead className="text-xs">{t("ent.colResolution")}</TableHead><TableHead className="text-xs">{t("ent.colActions")}</TableHead>
            </TableRow></TableHeader>
            <TableBody>
              {policies.map((p) => (
                <TableRow key={p.sla_id}>
                  <TableCell className="text-xs font-medium">{p.name}</TableCell>
                  <TableCell className="text-xs">{p.first_response_secs}s</TableCell>
                  <TableCell className="text-xs">{p.resolution_secs ? `${p.resolution_secs}s` : "—"}</TableCell>
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteSlaConfirm"), () => deleteSlaPolicy(p.sla_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <p className="mt-3 text-xs text-muted-foreground">{tf("ent.unresolvedBreaches", { n: (breaches ?? []).length })}</p>
      </SectionCard>
    </div>
  );
}

// ---------------- Routing ----------------
function RoutingPanel() {
  const { t } = useI18n();
  const { data, mutate } = useSWR<RoutingRule[]>("routing-rules", listRoutingRules);
  const [name, setName] = React.useState("");
  const [intent, setIntent] = React.useState("refund");
  const [skills, setSkills] = React.useState("refund");

  const submit = async () => {
    if (!name.trim()) { toast.error(t("ent.ruleNameRequired")); return; }
    try {
      await upsertRoutingRule({
        name,
        conditions: { intent },
        target_type: "round_robin",
        target_skills: skills.split(",").map((s) => s.trim()).filter(Boolean),
      });
      await mutate();
      setName("");
      toast.success(t("ent.routingSaved"));
    } catch (e) { toast.error((e as Error).message); }
  };

  const rules = data ?? [];
  return (
    <SectionCard title={t("ent.routingTitle")} action={<GitBranch className="size-4 text-muted-foreground" />}>
      <div className="mb-3 grid gap-2 sm:grid-cols-4">
        <Input placeholder={t("ent.ruleNamePh")} value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
        <Input placeholder={t("ent.intentPh")} value={intent} onChange={(e) => setIntent(e.target.value)} className="h-8 text-xs" />
        <Input placeholder={t("ent.skillsPh")} value={skills} onChange={(e) => setSkills(e.target.value)} className="h-8 text-xs" />
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />{t("ent.add")}</Button>
      </div>
      {rules.length === 0 ? <EmptyState icon={GitBranch} title={t("ent.noRouting")} /> : (
        <Table>
          <TableHeader><TableRow>
            <TableHead className="text-xs">{t("ent.colName")}</TableHead><TableHead className="text-xs">{t("ent.colConditions")}</TableHead>
            <TableHead className="text-xs">{t("ent.colSkills")}</TableHead><TableHead className="text-xs">{t("ent.colActions")}</TableHead>
          </TableRow></TableHeader>
          <TableBody>
            {rules.map((r) => (
              <TableRow key={r.rule_id}>
                <TableCell className="text-xs font-medium">{r.name}</TableCell>
                <TableCell className="text-xs">{JSON.stringify(r.conditions)}</TableCell>
                <TableCell className="text-xs">{(r.target_skills ?? []).join(", ") || "—"}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteRuleConfirm"), () => deleteRoutingRule(r.rule_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </SectionCard>
  );
}

// ---------------- Macros ----------------
function MacrosPanel() {
  const { t } = useI18n();
  const { data, mutate } = useSWR<Macro[]>("macros", listMacros);
  const [title, setTitle] = React.useState("");
  const [steps, setSteps] = React.useState("Hello!\nHow can I help?");

  const submit = async () => {
    if (!title.trim()) { toast.error(t("ent.macroNameRequired")); return; }
    try {
      await createMacro({ title, steps: steps.split("\n").filter(Boolean).map((content) => ({ content })) });
      await mutate();
      setTitle("");
      toast.success(t("ent.macroCreated"));
    } catch (e) { toast.error((e as Error).message); }
  };

  const macros = data ?? [];
  return (
    <SectionCard title={t("ent.macrosTitle")} action={<ListChecks className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder={t("ent.macroNamePh")} value={title} onChange={(e) => setTitle(e.target.value)} className="h-8 text-xs" />
        <Textarea placeholder={t("ent.stepsPh")} value={steps} onChange={(e) => setSteps(e.target.value)} rows={3} className="text-xs" />
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />{t("ent.create")}</Button>
      </div>
      {macros.length === 0 ? <EmptyState icon={ListChecks} title={t("ent.noMacros")} /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">{t("ent.colName")}</TableHead><TableHead className="text-xs">{t("ent.colSteps")}</TableHead><TableHead className="text-xs">{t("ent.colActions")}</TableHead></TableRow></TableHeader>
          <TableBody>
            {macros.map((m) => (
              <TableRow key={m.macro_id}>
                <TableCell className="text-xs font-medium">{m.title}</TableCell>
                <TableCell className="text-xs">{m.steps.length}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteMacroConfirm"), () => deleteMacro(m.macro_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </SectionCard>
  );
}

// ---------------- Roles ----------------
function RolesPanel() {
  const { t } = useI18n();
  const { data, mutate } = useSWR<Role[]>("roles", listRoles);
  const [name, setName] = React.useState("");
  const [perms, setPerms] = React.useState<string[]>(["inbox.view", "inbox.reply"]);

  const toggle = (p: string) => setPerms((prev) => prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]);

  const submit = async () => {
    if (!name.trim()) { toast.error(t("ent.roleNameRequired")); return; }
    try {
      await createRole({ name, permissions: perms });
      await mutate();
      setName("");
      toast.success(t("ent.roleSaved"));
    } catch (e) { toast.error((e as Error).message); }
  };

  const roles = data ?? [];
  return (
    <SectionCard title={t("ent.rolesTitle")} action={<ShieldCheck className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder={t("ent.roleNamePh")} value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
        <div className="flex flex-wrap gap-1.5">
          {PERMISSION_OPTIONS.map((p) => (
            <Badge key={p} variant={perms.includes(p) ? "info" : "outline"} onClick={() => toggle(p)} className="cursor-pointer text-[10px]">{p}</Badge>
          ))}
        </div>
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />{t("ent.createRole")}</Button>
      </div>
      {roles.length === 0 ? <EmptyState icon={ShieldCheck} title={t("ent.noRoles")} /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">{t("ent.colName")}</TableHead><TableHead className="text-xs">{t("ent.colPermissions")}</TableHead><TableHead className="text-xs">{t("ent.colActions")}</TableHead></TableRow></TableHeader>
          <TableBody>
            {roles.map((r) => (
              <TableRow key={r.role_id}>
                <TableCell className="text-xs font-medium">{r.name}</TableCell>
                <TableCell className="text-xs">{r.permissions.join(", ")}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteRoleConfirm"), () => deleteRole(r.role_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </SectionCard>
  );
}

// ---------------- Webhooks ----------------
function WebhooksPanel() {
  const { t } = useI18n();
  const { data, mutate } = useSWR<WebhookSubscription[]>("webhook-subs", listWebhookSubscriptions);
  const [url, setUrl] = React.useState("");
  const [secret, setSecret] = React.useState("");
  const [events, setEvents] = React.useState<string[]>(["session.resolved"]);

  const toggle = (e: string) => setEvents((prev) => prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]);

  const submit = async () => {
    if (!url.trim()) { toast.error(t("ent.urlRequired")); return; }
    try {
      await createWebhookSubscription({ url, secret, events });
      await mutate();
      setUrl(""); setSecret("");
      toast.success(t("ent.subCreated"));
    } catch (err) { toast.error((err as Error).message); }
  };

  const subs = data ?? [];
  return (
    <SectionCard title={t("ent.webhooksTitle")} action={<Webhook className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder="https://your-app.com/hook" value={url} onChange={(e) => setUrl(e.target.value)} className="h-8 text-xs" />
        <Input placeholder={t("ent.secretPh")} value={secret} onChange={(e) => setSecret(e.target.value)} className="h-8 text-xs" />
        <div className="flex flex-wrap gap-1.5">
          {EVENT_OPTIONS.map((e) => (
            <Badge key={e} variant={events.includes(e) ? "info" : "outline"} onClick={() => toggle(e)} className="cursor-pointer text-[10px]">{e}</Badge>
          ))}
        </div>
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />{t("ent.createSub")}</Button>
      </div>
      {subs.length === 0 ? <EmptyState icon={Webhook} title={t("ent.noSubs")} /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">URL</TableHead><TableHead className="text-xs">{t("ent.colEvents")}</TableHead><TableHead className="text-xs">{t("ent.colActions")}</TableHead></TableRow></TableHeader>
          <TableBody>
            {subs.map((s) => (
              <TableRow key={s.subscription_id}>
                <TableCell className="text-xs font-medium truncate max-w-[200px]">{s.url}</TableCell>
                <TableCell className="text-xs">{s.events.join(", ")}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteWebhookConfirm"), () => deleteWebhookSubscription(s.subscription_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </SectionCard>
  );
}

// ---------------- Performance / intent / integrations ----------------
function AnalyticsPanel() {
  const { t } = useI18n();
  const { data: perf } = useSWR<AgentPerformance[]>("agent-performance", () => getAgentPerformance(30));
  const { data: intents } = useSWR<IntentCount[]>("intent-analytics", () => getIntentAnalytics(30));
  const { data: status } = useSWR<IntegrationStatus>("integrations-status", getIntegrationStatus);

  const agents = perf ?? [];
  const intentRows = intents ?? [];
  return (
    <div className="space-y-4">
      <SectionCard title={t("ent.perfTitle")} action={<BarChart3 className="size-4 text-muted-foreground" />}>
        {agents.length === 0 ? <EmptyState icon={BarChart3} title={t("ent.noPerf")} /> : (
          <Table>
            <TableHeader><TableRow>
              <TableHead className="text-xs">{t("ent.colAgent")}</TableHead><TableHead className="text-xs">{t("ent.colResolved")}</TableHead>
              <TableHead className="text-xs">{t("ent.colHandled")}</TableHead><TableHead className="text-xs">{t("ent.colAvgHandle")}</TableHead>
              <TableHead className="text-xs">CSAT</TableHead>
            </TableRow></TableHeader>
            <TableBody>
              {agents.map((a) => (
                <TableRow key={a.user_id}>
                  <TableCell className="text-xs font-medium">{a.display_name}</TableCell>
                  <TableCell className="text-xs">{a.resolved_count}</TableCell>
                  <TableCell className="text-xs">{a.handled_count}</TableCell>
                  <TableCell className="text-xs">{Math.round(a.avg_handle_secs)}s</TableCell>
                  <TableCell className="text-xs">{a.avg_csat.toFixed(2)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </SectionCard>

      <SectionCard title={t("ent.intentTitle")} action={<BarChart3 className="size-4 text-muted-foreground" />}>
        {intentRows.length === 0 ? <EmptyState icon={BarChart3} title={t("ent.noIntents")} /> : (
          <Table>
            <TableHeader><TableRow><TableHead className="text-xs">{t("ent.colIntent")}</TableHead><TableHead className="text-xs">{t("ent.colSessions")}</TableHead><TableHead className="text-xs">{t("ent.colConfidence")}</TableHead></TableRow></TableHeader>
            <TableBody>
              {intentRows.map((i) => (
                <TableRow key={i.intent}>
                  <TableCell className="text-xs font-medium">{i.intent}</TableCell>
                  <TableCell className="text-xs">{i.count}</TableCell>
                  <TableCell className="text-xs">{i.avg_confidence ? i.avg_confidence.toFixed(2) : "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </SectionCard>

      <SectionCard title={t("ent.integrations")} action={<Plug className="size-4 text-muted-foreground" />}>
        {!status ? <p className="text-xs text-muted-foreground">{t("settings.loading")}</p> : (
          <div className="grid gap-2 sm:grid-cols-3">
            {([["Email", status.email], ["Voice/SMS", status.voice], ["SSO", status.sso]] as const).map(([label, s]) => (
              <div key={label} className="rounded-lg border border-border p-3">
                <p className="text-xs font-medium">{label}</p>
                <Badge variant={s.configured ? "success" : s.enabled ? "secondary" : "outline"} className="mt-1 text-[10px]">
                  {s.configured ? t("ent.configured") : s.enabled ? t("ent.enabledPending") : t("ent.notEnabled")}
                </Badge>
              </div>
            ))}
          </div>
        )}
      </SectionCard>
    </div>
  );
}

export function EnterpriseTab() {
  const { t } = useI18n();
  return (
    <Tabs defaultValue="sla">
      {/* 二级导航与 Growth / 一级导航共用同一套 Tabs 视觉. 最后一项原名
          "Analytics" 与一级 Tab 重名, 改为 "Performance". */}
      <TabsList className="h-auto w-auto flex-wrap">
        <TabsTrigger value="sla" className="gap-1.5 text-xs"><AlarmClock className="size-3" />{t("ent.tabSla")}</TabsTrigger>
        <TabsTrigger value="routing" className="gap-1.5 text-xs"><GitBranch className="size-3" />{t("ent.tabRouting")}</TabsTrigger>
        <TabsTrigger value="macros" className="gap-1.5 text-xs"><ListChecks className="size-3" />{t("ent.tabMacros")}</TabsTrigger>
        <TabsTrigger value="roles" className="gap-1.5 text-xs"><ShieldCheck className="size-3" />{t("ent.tabRoles")}</TabsTrigger>
        <TabsTrigger value="webhooks" className="gap-1.5 text-xs"><Webhook className="size-3" />{t("ent.tabWebhooks")}</TabsTrigger>
        <TabsTrigger value="analytics" className="gap-1.5 text-xs"><BarChart3 className="size-3" />{t("ent.tabPerformance")}</TabsTrigger>
      </TabsList>
      <TabsContent value="sla" className="mt-4"><SlaPanel /></TabsContent>
      <TabsContent value="routing" className="mt-4"><RoutingPanel /></TabsContent>
      <TabsContent value="macros" className="mt-4"><MacrosPanel /></TabsContent>
      <TabsContent value="roles" className="mt-4"><RolesPanel /></TabsContent>
      <TabsContent value="webhooks" className="mt-4"><WebhooksPanel /></TabsContent>
      <TabsContent value="analytics" className="mt-4"><AnalyticsPanel /></TabsContent>
    </Tabs>
  );
}
