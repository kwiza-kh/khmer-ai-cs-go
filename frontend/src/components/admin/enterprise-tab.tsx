"use client";

import * as React from "react";
import useSWR from "swr";
import {
  listSlaPolicies, upsertSlaPolicy, deleteSlaPolicy, listSlaBreaches,
  listRoutingRules, upsertRoutingRule, deleteRoutingRule,
  listMacros, createMacro, deleteMacro,
  listWebhookSubscriptions, createWebhookSubscription, deleteWebhookSubscription,
  listWebhookDeliveries, retryWebhookDelivery,
  getAgentPerformance, getIntentAnalytics,
  type SlaPolicy, type RoutingRule, type Macro, type WebhookSubscription,
  type WebhookDelivery, type AgentPerformance, type IntentCount,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useAuth } from "@/lib/auth-client";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState } from "@/components/empty-state";
import { toast } from "sonner";
import {
  AlarmClock, GitBranch, ListChecks, Webhook, BarChart3, Trash2, Plus,
  History, RefreshCw,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { confirmDelete } from "@/lib/confirm-delete";


// Mirrors webhookout.SupportedEvents on the Go side: only events with a real
// emission site are selectable. The old list advertised four names that never
// fired — a saved filter that can never match is worse than no subscription.
const EVENT_OPTIONS = ["session.created", "handoff.requested", "session.resolved"];

/** 小号可见标签 + 控件, 用于紧凑表单行。 */
function LabeledField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="truncate text-label font-medium text-muted-foreground">{label}</span>
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

// ---------------- Webhooks ----------------
function deliveryStatusVariant(status: string): "success" | "destructive" | "info" | "outline" {
  switch (status) {
    case "success": return "success";
    case "failed": return "destructive";
    case "delivering": return "info";
    default: return "outline";
  }
}

// The delivery history is the answer to "my endpoint received nothing": it shows
// every event queued for this subscription, the last HTTP status, the attempt
// count, and lets the integrator re-queue a finished row after fixing the URL.
function WebhookDeliveriesDialog({ sub, onClose }: { sub: WebhookSubscription; onClose: () => void }) {
  const { t } = useI18n();
  const { data, mutate, isLoading } = useSWR<WebhookDelivery[]>(
    `webhook-deliveries-${sub.subscription_id}`,
    () => listWebhookDeliveries(sub.subscription_id),
  );
  const rows = data ?? [];
  const retry = async (id: number) => {
    try {
      await retryWebhookDelivery(id);
      await mutate();
      toast.success(t("ent.retried"));
    } catch (err) { toast.error((err as Error).message); }
  };
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[85vh] overflow-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("ent.deliveriesTitle")}</DialogTitle>
          <DialogDescription className="truncate text-xs">{sub.url}</DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <p className="text-xs text-muted-foreground">{t("ent.deliveriesLoading")}</p>
        ) : rows.length === 0 ? (
          <EmptyState icon={History} title={t("ent.noDeliveries")} />
        ) : (
          <Table>
            <TableHeader><TableRow>
              <TableHead className="text-xs">{t("ent.colEvent")}</TableHead>
              <TableHead className="text-xs">{t("ent.colDeliveryStatus")}</TableHead>
              <TableHead className="text-xs">{t("ent.colStatusCode")}</TableHead>
              <TableHead className="text-xs">{t("ent.colAttempts")}</TableHead>
              <TableHead className="text-xs">{t("ent.colCreatedAt")}</TableHead>
              <TableHead className="text-xs">{t("ent.colActions")}</TableHead>
            </TableRow></TableHeader>
            <TableBody>
              {rows.map((d) => (
                <TableRow key={d.delivery_id}>
                  <TableCell className="text-xs font-medium">{d.event}</TableCell>
                  <TableCell><Badge variant={deliveryStatusVariant(d.status)} className="text-label">{t(`ent.deliveryStatus.${d.status}`)}</Badge></TableCell>
                  <TableCell className="text-xs">{d.status_code || "—"}</TableCell>
                  <TableCell className="text-xs">{d.attempts}</TableCell>
                  <TableCell className="text-xs whitespace-nowrap">{new Date(d.created_at).toLocaleString()}</TableCell>
                  <TableCell>
                    {d.status !== "pending" && d.status !== "delivering" && (
                      <Button size="sm" variant="ghost" className="h-7 text-xs" onClick={() => void retry(d.delivery_id)}>
                        <RefreshCw className="size-3" />{t("ent.retryDelivery")}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </DialogContent>
    </Dialog>
  );
}

function WebhooksPanel() {
  const { t } = useI18n();
  const { data, mutate } = useSWR<WebhookSubscription[]>("webhook-subs", listWebhookSubscriptions);
  const [url, setUrl] = React.useState("");
  const [secret, setSecret] = React.useState("");
  const [events, setEvents] = React.useState<string[]>(["session.resolved"]);
  const [deliveriesFor, setDeliveriesFor] = React.useState<WebhookSubscription | null>(null);

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
        <p className="text-xs text-muted-foreground">{t("ent.webhookSignatureHint")}</p>
        <div className="flex flex-wrap gap-1.5">
          {EVENT_OPTIONS.map((e) => (
            <Badge key={e} variant={events.includes(e) ? "info" : "outline"} onClick={() => toggle(e)} className="cursor-pointer text-label">{e}</Badge>
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
                  <div className="flex items-center gap-1">
                    <Button size="sm" variant="ghost" onClick={() => setDeliveriesFor(s)} className="h-7 text-xs">
                      <History className="size-3" />{t("ent.viewDeliveries")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => confirmDelete(t("ent.deleteWebhookConfirm"), () => deleteWebhookSubscription(s.subscription_id), mutate)} className="h-7 text-xs text-danger"><Trash2 className="size-3" /></Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {deliveriesFor && <WebhookDeliveriesDialog sub={deliveriesFor} onClose={() => setDeliveriesFor(null)} />}
    </SectionCard>
  );
}

// ---------------- Performance / intent / integrations ----------------
function AnalyticsPanel() {
  const { t } = useI18n();
  const { data: perf } = useSWR<AgentPerformance[]>("agent-performance", () => getAgentPerformance(30));
  const { data: intents } = useSWR<IntentCount[]>("intent-analytics", () => getIntentAnalytics(30));

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
    </div>
  );
}

export function EnterpriseTab() {
  const { t } = useI18n();
  const { user, can } = useAuth();
  // SLA/routing/webhooks are settings; macros are scripts; the performance
  // panel aggregates tenant analytics that stay owner-only on the API. A seat
  // sees the tabs its grants can actually load.
  const isOwner = !user?.is_member;
  const first = can("settings_view") ? "sla" : can("scripts_view") ? "macros" : "sla";
  return (
    <Tabs defaultValue={first}>
      {/* 二级导航与 Growth / 一级导航共用同一套 Tabs 视觉. 最后一项原名
          "Analytics" 与一级 Tab 重名, 改为 "Performance". */}
      <TabsList className="h-auto w-auto flex-wrap">
        {can("settings_view") && <TabsTrigger value="sla" className="gap-1.5 text-xs"><AlarmClock className="size-3" />{t("ent.tabSla")}</TabsTrigger>}
        {can("settings_view") && <TabsTrigger value="routing" className="gap-1.5 text-xs"><GitBranch className="size-3" />{t("ent.tabRouting")}</TabsTrigger>}
        {can("scripts_view") && <TabsTrigger value="macros" className="gap-1.5 text-xs"><ListChecks className="size-3" />{t("ent.tabMacros")}</TabsTrigger>}
        {can("settings_view") && <TabsTrigger value="webhooks" className="gap-1.5 text-xs"><Webhook className="size-3" />{t("ent.tabWebhooks")}</TabsTrigger>}
        {isOwner && <TabsTrigger value="analytics" className="gap-1.5 text-xs"><BarChart3 className="size-3" />{t("ent.tabPerformance")}</TabsTrigger>}
      </TabsList>
      {can("settings_view") && <TabsContent value="sla" className="mt-4"><SlaPanel /></TabsContent>}
      {can("settings_view") && <TabsContent value="routing" className="mt-4"><RoutingPanel /></TabsContent>}
      {can("scripts_view") && <TabsContent value="macros" className="mt-4"><MacrosPanel /></TabsContent>}
      {can("settings_view") && <TabsContent value="webhooks" className="mt-4"><WebhooksPanel /></TabsContent>}
      {isOwner && <TabsContent value="analytics" className="mt-4"><AnalyticsPanel /></TabsContent>}
    </Tabs>
  );
}
