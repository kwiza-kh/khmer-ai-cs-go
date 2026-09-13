"use client";

import * as React from "react";
import useSWR, { mutate as globalMutate } from "swr";
import {
  listCustomers, getCustomer360, updateCustomerNotes, listFaqSuggestions, acceptFaqSuggestion,
  dismissFaqSuggestion, listTeam, addAgent, removeAgent, listCampaigns, createCampaign,
  cancelCampaign, getBilling, setPlan,
  CustomerProfile, FaqSuggestion, AgentMember, CampaignItem, BillingInfo,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/empty-state";
import { useAuth } from "@/lib/auth-client";
import {
  Users, Lightbulb, Headset, Megaphone, CreditCard, Loader2, Plus, Trash2, Search,
  BookOpenCheck, XCircle, CheckCircle2, Send,
} from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { fmtDate, fmtDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * Growth tab: customers 360 (F1/F6), FAQ mining (F3), agent team (F4),
 * marketing campaigns (F5), and billing/quota (F9).
 */
export function GrowthTab() {
  const { t } = useI18n();
  const [section, setSection] = React.useState("customers");
  return (
    // 二级导航统一用与 Enterprise 相同的 Tabs 组件 —— 此前这里是手写
    // Button 组(实心紫活动态), 导致同一个仪表盘里出现 3 种 tab 视觉。
    <Tabs value={section} onValueChange={(v) => setSection(String(v))}>
      <TabsList className="h-auto w-auto flex-wrap">
        {[
          { key: "customers", labelKey: "gr.customers", icon: Users },
          { key: "faq", labelKey: "gr.faq", icon: Lightbulb },
          { key: "team", labelKey: "gr.team", icon: Headset },
          { key: "campaigns", labelKey: "gr.campaigns", icon: Megaphone },
          { key: "billing", labelKey: "gr.billing", icon: CreditCard },
        ].map((s) => (
          <TabsTrigger key={s.key} value={s.key} className="gap-1.5 text-xs">
            <s.icon className="size-3" /> {t(s.labelKey)}
          </TabsTrigger>
        ))}
      </TabsList>
      <TabsContent value="customers" className="mt-4"><CustomersCard /></TabsContent>
      <TabsContent value="faq" className="mt-4"><FaqCard /></TabsContent>
      <TabsContent value="team" className="mt-4"><TeamCard /></TabsContent>
      <TabsContent value="campaigns" className="mt-4"><CampaignsCard /></TabsContent>
      <TabsContent value="billing" className="mt-4"><BillingCard /></TabsContent>
    </Tabs>
  );
}

// ============================================
// Customers 360
// ============================================
function CustomersCard() {
  const { t, tf } = useI18n();
  const [q, setQ] = React.useState("");
  const { data: customers } = useSWR(`customers-${q}`, () => listCustomers(q || undefined));
  const [selected, setSelected] = React.useState<CustomerProfile | null>(null);
  const [notes, setNotes] = React.useState("");
  const { data: detail, mutate } = useSWR(
    selected ? `customer-360-${selected.profile_id}` : null,
    () => (selected ? getCustomer360(selected.profile_id) : null),
  );

  const saveNotes = async () => {
    if (!selected) return;
    try {
      await updateCustomerNotes(selected.profile_id, notes);
      toast.success(t("gr.notesSaved"));
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Users className="size-4 text-primary" /> {t("gr.customers")} <span className="text-[11px] text-muted-foreground">{t("gr.customers360")}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("gr.searchPh")} className="h-8 pl-8 text-xs" />
        </div>
        {!customers || customers.length === 0 ? (
          <EmptyState icon={Users} title={t("gr.noCustomers")} description={t("gr.noCustomersDesc")} />
        ) : (
          // 未选中客户时不摆两列 —— 否则列表只占左半, 右侧留一片空白
          <div className={cn("grid grid-cols-1 gap-3", selected && "lg:grid-cols-2")}>
            <div className="space-y-1.5">
              {(customers as CustomerProfile[]).map((c) => (
                <button
                  key={c.profile_id}
                  onClick={() => { setSelected(c); setNotes(c.notes || ""); }}
                  className={`w-full text-left rounded-md border p-2.5 transition-colors ${selected?.profile_id === c.profile_id ? "border-primary bg-primary/5" : "border-border hover:bg-muted/40"}`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs font-medium truncate">{c.display_name || c.platform_user_id}</p>
                    <Badge variant="outline" className="h-4 px-1 text-[11px] uppercase">{c.platform}</Badge>
                  </div>
                  <p className="text-[11px] text-muted-foreground mt-0.5">{c.platform_user_id} · {tf("gr.sessionsCount", { n: c.total_sessions })}</p>
                </button>
              ))}
            </div>
            {selected && detail && (
              <div className="space-y-2 rounded-md border border-border p-3">
                <div className="flex items-center justify-between">
                  <p className="text-xs font-semibold">{detail.profile.display_name || detail.profile.platform_user_id}</p>
                  <Badge variant="outline" className="h-4 px-1 text-[11px] uppercase">{detail.profile.platform}</Badge>
                </div>
                <p className="text-[11px] text-muted-foreground">{detail.profile.platform_user_id} · {tf("gr.seen", { time: detail.profile.last_seen_at ? fmtDateTime(detail.profile.last_seen_at) : "—" })}</p>
                <div className="grid grid-cols-3 gap-1.5 text-center">
                  {(["total_sessions", "total_messages"] as const).map((k) => (
                    <div key={k} className="rounded bg-muted/40 py-1.5">
                      <p className="text-sm font-semibold">{detail.profile[k]}</p>
                      <p className="text-[10px] text-muted-foreground">{k === "total_sessions" ? t("gr.statSessions") : t("gr.statMessages")}</p>
                    </div>
                  ))}
                </div>
                <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} placeholder={t("gr.notesPh")} rows={2} className="text-xs resize-none" />
                <Button size="sm" className="h-7 text-xs" onClick={saveNotes}>{t("gr.saveNotes")}</Button>
                {detail.sessions.length > 0 && (
                  <div className="space-y-1">
                    <p className="text-[11px] font-medium text-muted-foreground">{t("gr.recentSessions")}</p>
                    {detail.sessions.slice(0, 5).map((sess) => (
                      <div key={sess.session_id} className="rounded border border-border p-2">
                        <div className="flex items-center gap-1.5">
                          <p className="text-[11px] font-medium truncate flex-1">{sess.title || t("gr.untitled")}</p>
                          {sess.sentiment === "negative" && <Badge variant="destructive" className="h-3.5 px-1 text-[10px]">{t("inbox.angry")}</Badge>}
                          <Badge variant="outline" className="h-3.5 px-1 text-[10px]">{sess.status}</Badge>
                        </div>
                        {sess.summary && <p className="text-[10px] text-muted-foreground mt-1 line-clamp-2">{sess.summary}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ============================================
// FAQ mining
// ============================================
function FaqCard() {
  const { t } = useI18n();
  const { data, mutate } = useSWR("faq-suggestions", () => listFaqSuggestions("pending"));
  const [accepting, setAccepting] = React.useState<number | null>(null);

  const handleAccept = async (id: number) => {
    setAccepting(id);
    try {
      await acceptFaqSuggestion(id, "");
      toast.success(t("gr.addedToKb"));
      void mutate();
      void globalMutate("knowledge");
    } catch (e) { toast.error((e as Error).message); }
    finally { setAccepting(null); }
  };
  const handleDismiss = async (id: number) => {
    try {
      await dismissFaqSuggestion(id);
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Lightbulb className="size-4 text-warning" /> {t("gr.faq")} <span className="text-[10px] text-muted-foreground">{t("gr.faqNote")}</span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {!data || data.length === 0 ? (
          <EmptyState icon={Lightbulb} title={t("gr.noSuggestions")} description={t("gr.noSuggestionsDesc")} />
        ) : (
          <div className="space-y-2">
            {(data as FaqSuggestion[]).map((f) => (
              <div key={f.suggestion_id} className="rounded-md border border-border p-3 space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <p className="text-xs font-medium flex-1">{f.question}</p>
                  <Badge variant="secondary" className="h-4 px-1 text-[11px]">{f.frequency}×</Badge>
                </div>
                {f.answer && <p className="text-[11px] text-muted-foreground line-clamp-2">{f.answer}</p>}
                <div className="flex gap-1.5">
                  <Button size="sm" variant="default" className="h-6 text-[11px] gap-1" onClick={() => handleAccept(f.suggestion_id)} disabled={accepting === f.suggestion_id}>
                    {accepting === f.suggestion_id ? <Loader2 className="size-3 animate-spin" /> : <BookOpenCheck className="size-3" />} {t("gr.addToKb")}
                  </Button>
                  <Button size="sm" variant="ghost" className="h-6 text-[11px] gap-1 text-muted-foreground" onClick={() => handleDismiss(f.suggestion_id)}>
                    <XCircle className="size-3" /> {t("gr.dismiss")}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ============================================
// Agent team
// ============================================
function TeamCard() {
  const { t } = useI18n();
  const { data: team, mutate } = useSWR("agent-team", listTeam);
  const [agentId, setAgentId] = React.useState("");
  const [name, setName] = React.useState("");
  const [skills, setSkills] = React.useState("");
  const [adding, setAdding] = React.useState(false);

  const handleAdd = async () => {
    if (!agentId) return;
    setAdding(true);
    try {
      await addAgent(Number(agentId), name, skills.split(",").map((s) => s.trim()).filter(Boolean));
      setAgentId(""); setName(""); setSkills("");
      toast.success(t("gr.agentAdded"));
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
    finally { setAdding(false); }
  };
  const handleRemove = async (id: number) => {
    try {
      await removeAgent(id);
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Headset className="size-4 text-primary" /> {t("gr.team")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="rounded-md border border-border p-3 space-y-2 bg-muted/30">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
            <Input placeholder={t("gr.agentIdPh")} value={agentId} onChange={(e) => setAgentId(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("gr.displayNamePh")} value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("gr.skillsPh")} value={skills} onChange={(e) => setSkills(e.target.value)} className="h-8 text-xs" />
          </div>
          <Button size="sm" className="h-7 text-xs gap-1" onClick={handleAdd} disabled={adding || !agentId}>
            {adding ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />} {t("gr.addAgent")}
          </Button>
        </div>
        {!team || team.length === 0 ? (
          <EmptyState icon={Headset} title={t("gr.noAgents")} description={t("gr.noAgentsDesc")} />
        ) : (
          <div className="space-y-1.5">
            {(team as AgentMember[]).map((a) => (
              <div key={a.team_id} className="rounded-md border border-border p-2.5 flex items-center gap-2">
                <div className="flex-1 min-w-0">
                  <p className="text-xs font-medium">{a.display_name || a.username}</p>
                  <p className="text-[11px] text-muted-foreground">{a.email}</p>
                  <div className="flex gap-1 mt-1 flex-wrap">
                    {a.skills.map((sk) => <Badge key={sk} variant="outline" className="h-4 px-1 text-[11px]">{sk}</Badge>)}
                  </div>
                </div>
                <button onClick={() => handleRemove(a.team_id)} className="text-muted-foreground hover:text-danger" title={t("gr.remove")}><Trash2 className="size-3.5" /></button>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ============================================
// Campaigns
// ============================================
function CampaignsCard() {
  const { t, tf } = useI18n();
  const { data, mutate } = useSWR("campaigns", listCampaigns);
  const [name, setName] = React.useState("");
  const [configId, setConfigId] = React.useState("");
  const [template, setTemplate] = React.useState("");
  const [params, setParams] = React.useState("");
  const [scheduled, setScheduled] = React.useState("");
  const [creating, setCreating] = React.useState(false);

  const handleCreate = async () => {
    if (!configId || !template || !scheduled) return;
    setCreating(true);
    try {
      await createCampaign({
        name: name || template, platform: "whatsapp", config_id: Number(configId),
        template_name: template, template_language: "km",
        body_params: params.split(",").map((s) => s.trim()).filter(Boolean),
        recipient_filter: "all", scheduled_at: new Date(scheduled).toISOString(),
      });
      setName(""); setConfigId(""); setTemplate(""); setParams(""); setScheduled("");
      toast.success(t("gr.scheduled"));
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
    finally { setCreating(false); }
  };
  const handleCancel = async (id: number) => {
    try { await cancelCampaign(id); void mutate(); } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Megaphone className="size-4 text-primary" /> {t("gr.campaigns")} <span className="text-[10px] text-muted-foreground">{t("gr.campaignsNote")}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="rounded-md border border-border p-3 space-y-2 bg-muted/30">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <Input placeholder={t("gr.campaignNamePh")} value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("gr.configIdPh")} value={configId} onChange={(e) => setConfigId(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("gr.templateNamePh")} value={template} onChange={(e) => setTemplate(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("gr.bodyParamsPh")} value={params} onChange={(e) => setParams(e.target.value)} className="h-8 text-xs" />
          </div>
          <Input type="datetime-local" value={scheduled} onChange={(e) => setScheduled(e.target.value)} className="h-8 text-xs" />
          <Button size="sm" className="h-7 text-xs gap-1" onClick={handleCreate} disabled={creating || !configId || !template || !scheduled}>
            {creating ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />} {t("gr.schedule")}
          </Button>
        </div>
        {!data || data.length === 0 ? (
          <EmptyState icon={Megaphone} title={t("gr.noCampaigns")} />
        ) : (
          <div className="space-y-1.5">
            {(data as CampaignItem[]).map((c) => (
              <div key={c.campaign_id} className="rounded-md border border-border p-2.5 flex items-center gap-2">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-1.5">
                    <p className="text-xs font-medium truncate">{c.name}</p>
                    <Badge variant={c.status === "done" ? "success" : c.status === "scheduled" ? "secondary" : "outline"} className="h-4 px-1 text-[11px]">{c.status}</Badge>
                  </div>
                  <p className="text-[11px] text-muted-foreground mt-0.5">
                    {c.template_name} · {fmtDateTime(c.scheduled_at)} · {tf("gr.sentCount", { n: c.sent_count })}
                  </p>
                </div>
                {c.status === "scheduled" && (
                  <button onClick={() => handleCancel(c.campaign_id)} className="text-muted-foreground hover:text-danger" title={t("common.cancel")}><XCircle className="size-3.5" /></button>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ============================================
// Billing
// ============================================
function BillingCard() {
  const { user } = useAuth();
  const { t, tf } = useI18n();
  const { data, mutate } = useSWR("billing", getBilling);
  const [planning, setPlanning] = React.useState<string | null>(null);

  const handlePlan = async (p: string) => {
    setPlanning(p);
    try {
      await setPlan(p);
      toast.success(tf("gr.planUpdated", { plan: p }));
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
    finally { setPlanning(null); }
  };

  const info = data as BillingInfo | undefined;
  const msgPct = info && info.monthly_message_quota > 0 ? Math.min(100, (info.messages_used / info.monthly_message_quota) * 100) : 0;

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <CreditCard className="size-4 text-primary" /> {t("gr.billing")} <span className="text-[10px] text-muted-foreground">{tf("gr.billingNote", { user: user?.username ?? "" })}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          {(["free", "pro", "enterprise"] as const).map((p) => (
            <Button
              key={p}
              variant={info?.plan === p ? "default" : "outline"}
              size="sm"
              className="h-8 text-xs capitalize"
              disabled={planning !== null}
              onClick={() => handlePlan(p)}
            >
              {planning === p ? <Loader2 className="size-3 animate-spin" /> : <CheckCircle2 className="size-3" />} {p}
            </Button>
          ))}
        </div>
        {info && (
          <div className="space-y-2">
            <div>
              <div className="flex justify-between text-[11px] text-muted-foreground mb-1">
                <span>{t("gr.messagesLabel")}</span><span>{info.messages_used} / {info.monthly_message_quota}</span>
              </div>
              <div className="h-1.5 rounded bg-muted overflow-hidden">
                <div className="h-full bg-primary transition-all" style={{ width: `${msgPct}%` }} />
              </div>
            </div>
            <div className="text-[11px] text-muted-foreground">
              {tf("gr.docsLine", { used: info.docs_used, quota: info.monthly_doc_quota, date: fmtDate(info.cycle_end) })}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
