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

function SectionCard({ title, children, action }: { title: string; children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-3 flex-row items-center justify-between">
        <CardTitle className="text-sm">{title}</CardTitle>
        {action}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

// ---------------- SLA ----------------
function SlaPanel() {
  const { data, mutate } = useSWR<SlaPolicy[]>("sla-policies", listSlaPolicies);
  const { data: breaches } = useSWR("sla-breaches", () => listSlaBreaches(true));
  const [name, setName] = React.useState("");
  const [first, setFirst] = React.useState(300);
  const [resolution, setResolution] = React.useState(3600);

  const submit = async () => {
    if (!name.trim()) { toast.error("名称不能为空"); return; }
    try {
      await upsertSlaPolicy({ name, first_response_secs: first, resolution_secs: resolution });
      await mutate();
      setName("");
      toast.success("SLA 已保存");
    } catch (e) { toast.error((e as Error).message); }
  };

  const policies = data ?? [];
  return (
    <div className="space-y-4">
      <SectionCard title="SLA 政策" action={<AlarmClock className="size-4 text-muted-foreground" />}>
        <div className="mb-3 grid gap-2 sm:grid-cols-4">
          <Input placeholder="名称 (如 urgent)" value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
          <Input type="number" placeholder="首响秒数" value={first} onChange={(e) => setFirst(Number(e.target.value))} className="h-8 text-xs" />
          <Input type="number" placeholder="解决秒数" value={resolution} onChange={(e) => setResolution(Number(e.target.value))} className="h-8 text-xs" />
          <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />添加</Button>
        </div>
        {policies.length === 0 ? <EmptyState icon={AlarmClock} title="暂无 SLA 政策" /> : (
          <Table>
            <TableHeader><TableRow>
              <TableHead className="text-xs">名称</TableHead><TableHead className="text-xs">首响</TableHead>
              <TableHead className="text-xs">解决</TableHead><TableHead className="text-xs">操作</TableHead>
            </TableRow></TableHeader>
            <TableBody>
              {policies.map((p) => (
                <TableRow key={p.sla_id}>
                  <TableCell className="text-xs font-medium">{p.name}</TableCell>
                  <TableCell className="text-xs">{p.first_response_secs}s</TableCell>
                  <TableCell className="text-xs">{p.resolution_secs ? `${p.resolution_secs}s` : "—"}</TableCell>
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={async () => { await deleteSlaPolicy(p.sla_id); await mutate(); }} className="h-7 text-xs text-destructive"><Trash2 className="size-3" /></Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <p className="mt-3 text-xs text-muted-foreground">未解决违约: {(breaches ?? []).length} 条</p>
      </SectionCard>
    </div>
  );
}

// ---------------- Routing ----------------
function RoutingPanel() {
  const { data, mutate } = useSWR<RoutingRule[]>("routing-rules", listRoutingRules);
  const [name, setName] = React.useState("");
  const [intent, setIntent] = React.useState("refund");
  const [skills, setSkills] = React.useState("refund");

  const submit = async () => {
    if (!name.trim()) { toast.error("规则名不能为空"); return; }
    try {
      await upsertRoutingRule({
        name,
        conditions: { intent },
        target_type: "round_robin",
        target_skills: skills.split(",").map((s) => s.trim()).filter(Boolean),
      });
      await mutate();
      setName("");
      toast.success("路由规则已保存");
    } catch (e) { toast.error((e as Error).message); }
  };

  const rules = data ?? [];
  return (
    <SectionCard title="路由规则" action={<GitBranch className="size-4 text-muted-foreground" />}>
      <div className="mb-3 grid gap-2 sm:grid-cols-4">
        <Input placeholder="规则名" value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
        <Input placeholder="意图 (intent)" value={intent} onChange={(e) => setIntent(e.target.value)} className="h-8 text-xs" />
        <Input placeholder="技能 (逗号分隔)" value={skills} onChange={(e) => setSkills(e.target.value)} className="h-8 text-xs" />
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />添加</Button>
      </div>
      {rules.length === 0 ? <EmptyState icon={GitBranch} title="暂无路由规则" /> : (
        <Table>
          <TableHeader><TableRow>
            <TableHead className="text-xs">名称</TableHead><TableHead className="text-xs">条件</TableHead>
            <TableHead className="text-xs">目标技能</TableHead><TableHead className="text-xs">操作</TableHead>
          </TableRow></TableHeader>
          <TableBody>
            {rules.map((r) => (
              <TableRow key={r.rule_id}>
                <TableCell className="text-xs font-medium">{r.name}</TableCell>
                <TableCell className="text-xs">{JSON.stringify(r.conditions)}</TableCell>
                <TableCell className="text-xs">{(r.target_skills ?? []).join(", ") || "—"}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={async () => { await deleteRoutingRule(r.rule_id); await mutate(); }} className="h-7 text-xs text-destructive"><Trash2 className="size-3" /></Button>
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
  const { data, mutate } = useSWR<Macro[]>("macros", listMacros);
  const [title, setTitle] = React.useState("");
  const [steps, setSteps] = React.useState("Hello!\nHow can I help?");

  const submit = async () => {
    if (!title.trim()) { toast.error("宏名称不能为空"); return; }
    try {
      await createMacro({ title, steps: steps.split("\n").filter(Boolean).map((content) => ({ content })) });
      await mutate();
      setTitle("");
      toast.success("宏已创建");
    } catch (e) { toast.error((e as Error).message); }
  };

  const macros = data ?? [];
  return (
    <SectionCard title="宏 (多步回复)" action={<ListChecks className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder="宏名称" value={title} onChange={(e) => setTitle(e.target.value)} className="h-8 text-xs" />
        <Textarea placeholder="每行一个回复步骤" value={steps} onChange={(e) => setSteps(e.target.value)} rows={3} className="text-xs" />
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />创建</Button>
      </div>
      {macros.length === 0 ? <EmptyState icon={ListChecks} title="暂无宏" /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">名称</TableHead><TableHead className="text-xs">步骤数</TableHead><TableHead className="text-xs">操作</TableHead></TableRow></TableHeader>
          <TableBody>
            {macros.map((m) => (
              <TableRow key={m.macro_id}>
                <TableCell className="text-xs font-medium">{m.title}</TableCell>
                <TableCell className="text-xs">{m.steps.length}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={async () => { await deleteMacro(m.macro_id); await mutate(); }} className="h-7 text-xs text-destructive"><Trash2 className="size-3" /></Button>
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
  const { data, mutate } = useSWR<Role[]>("roles", listRoles);
  const [name, setName] = React.useState("");
  const [perms, setPerms] = React.useState<string[]>(["inbox.view", "inbox.reply"]);

  const toggle = (p: string) => setPerms((prev) => prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]);

  const submit = async () => {
    if (!name.trim()) { toast.error("角色名不能为空"); return; }
    try {
      await createRole({ name, permissions: perms });
      await mutate();
      setName("");
      toast.success("角色已保存");
    } catch (e) { toast.error((e as Error).message); }
  };

  const roles = data ?? [];
  return (
    <SectionCard title="自定义角色 (RBAC)" action={<ShieldCheck className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder="角色名 (如 agent)" value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
        <div className="flex flex-wrap gap-1.5">
          {PERMISSION_OPTIONS.map((p) => (
            <Badge key={p} variant={perms.includes(p) ? "info" : "outline"} onClick={() => toggle(p)} className="cursor-pointer text-[10px]">{p}</Badge>
          ))}
        </div>
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />创建角色</Button>
      </div>
      {roles.length === 0 ? <EmptyState icon={ShieldCheck} title="暂无自定义角色" /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">名称</TableHead><TableHead className="text-xs">权限</TableHead><TableHead className="text-xs">操作</TableHead></TableRow></TableHeader>
          <TableBody>
            {roles.map((r) => (
              <TableRow key={r.role_id}>
                <TableCell className="text-xs font-medium">{r.name}</TableCell>
                <TableCell className="text-xs">{r.permissions.join(", ")}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={async () => { await deleteRole(r.role_id); await mutate(); }} className="h-7 text-xs text-destructive"><Trash2 className="size-3" /></Button>
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
  const { data, mutate } = useSWR<WebhookSubscription[]>("webhook-subs", listWebhookSubscriptions);
  const [url, setUrl] = React.useState("");
  const [secret, setSecret] = React.useState("");
  const [events, setEvents] = React.useState<string[]>(["session.resolved"]);

  const toggle = (e: string) => setEvents((prev) => prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]);

  const submit = async () => {
    if (!url.trim()) { toast.error("URL 不能为空"); return; }
    try {
      await createWebhookSubscription({ url, secret, events });
      await mutate();
      setUrl(""); setSecret("");
      toast.success("订阅已创建");
    } catch (err) { toast.error((err as Error).message); }
  };

  const subs = data ?? [];
  return (
    <SectionCard title="出站 Webhook" action={<Webhook className="size-4 text-muted-foreground" />}>
      <div className="mb-3 space-y-2">
        <Input placeholder="https://your-app.com/hook" value={url} onChange={(e) => setUrl(e.target.value)} className="h-8 text-xs" />
        <Input placeholder="签名密钥 (可选, HMAC-SHA256)" value={secret} onChange={(e) => setSecret(e.target.value)} className="h-8 text-xs" />
        <div className="flex flex-wrap gap-1.5">
          {EVENT_OPTIONS.map((e) => (
            <Badge key={e} variant={events.includes(e) ? "info" : "outline"} onClick={() => toggle(e)} className="cursor-pointer text-[10px]">{e}</Badge>
          ))}
        </div>
        <Button size="sm" onClick={submit} className="h-8 gap-1.5 text-xs"><Plus className="size-3" />创建订阅</Button>
      </div>
      {subs.length === 0 ? <EmptyState icon={Webhook} title="暂无订阅" /> : (
        <Table>
          <TableHeader><TableRow><TableHead className="text-xs">URL</TableHead><TableHead className="text-xs">事件</TableHead><TableHead className="text-xs">操作</TableHead></TableRow></TableHeader>
          <TableBody>
            {subs.map((s) => (
              <TableRow key={s.subscription_id}>
                <TableCell className="text-xs font-medium truncate max-w-[200px]">{s.url}</TableCell>
                <TableCell className="text-xs">{s.events.join(", ")}</TableCell>
                <TableCell>
                  <Button size="sm" variant="ghost" onClick={async () => { await deleteWebhookSubscription(s.subscription_id); await mutate(); }} className="h-7 text-xs text-destructive"><Trash2 className="size-3" /></Button>
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
  const { data: perf } = useSWR<AgentPerformance[]>("agent-performance", () => getAgentPerformance(30));
  const { data: intents } = useSWR<IntentCount[]>("intent-analytics", () => getIntentAnalytics(30));
  const { data: status } = useSWR<IntegrationStatus>("integrations-status", getIntegrationStatus);

  const agents = perf ?? [];
  const intentRows = intents ?? [];
  return (
    <div className="space-y-4">
      <SectionCard title="坐席绩效" action={<BarChart3 className="size-4 text-muted-foreground" />}>
        {agents.length === 0 ? <EmptyState icon={BarChart3} title="暂无坐席数据" /> : (
          <Table>
            <TableHeader><TableRow>
              <TableHead className="text-xs">坐席</TableHead><TableHead className="text-xs">已解决</TableHead>
              <TableHead className="text-xs">处理</TableHead><TableHead className="text-xs">平均处理时长</TableHead>
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

      <SectionCard title="意图分析" action={<BarChart3 className="size-4 text-muted-foreground" />}>
        {intentRows.length === 0 ? <EmptyState icon={BarChart3} title="暂无意图数据" /> : (
          <Table>
            <TableHeader><TableRow><TableHead className="text-xs">意图</TableHead><TableHead className="text-xs">会话数</TableHead><TableHead className="text-xs">平均置信度</TableHead></TableRow></TableHeader>
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

      <SectionCard title="渠道集成状态" action={<Plug className="size-4 text-muted-foreground" />}>
        {!status ? <p className="text-xs text-muted-foreground">Loading…</p> : (
          <div className="grid gap-2 sm:grid-cols-3">
            {([["Email", status.email], ["Voice/SMS", status.voice], ["SSO", status.sso]] as const).map(([label, s]) => (
              <div key={label} className="rounded-lg border border-border p-3">
                <p className="text-xs font-medium">{label}</p>
                <Badge variant={s.configured ? "success" : s.enabled ? "secondary" : "outline"} className="mt-1 text-[10px]">
                  {s.configured ? "已配置" : s.enabled ? "已启用(待配置)" : "未启用"}
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
  return (
    <Tabs defaultValue="sla">
      <TabsList className="h-auto w-auto flex-wrap">
        <TabsTrigger value="sla" className="gap-1.5 text-xs"><AlarmClock className="size-3" />SLA</TabsTrigger>
        <TabsTrigger value="routing" className="gap-1.5 text-xs"><GitBranch className="size-3" />Routing</TabsTrigger>
        <TabsTrigger value="macros" className="gap-1.5 text-xs"><ListChecks className="size-3" />Macros</TabsTrigger>
        <TabsTrigger value="roles" className="gap-1.5 text-xs"><ShieldCheck className="size-3" />Roles</TabsTrigger>
        <TabsTrigger value="webhooks" className="gap-1.5 text-xs"><Webhook className="size-3" />Webhooks</TabsTrigger>
        <TabsTrigger value="analytics" className="gap-1.5 text-xs"><BarChart3 className="size-3" />Analytics</TabsTrigger>
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
