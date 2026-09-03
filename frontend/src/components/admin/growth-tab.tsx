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

/**
 * Growth tab: customers 360 (F1/F6), FAQ mining (F3), agent team (F4),
 * marketing campaigns (F5), and billing/quota (F9).
 */
export function GrowthTab() {
  const [section, setSection] = React.useState("customers");
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        {[
          { key: "customers", label: "Customers", icon: Users },
          { key: "faq", label: "FAQ mining", icon: Lightbulb },
          { key: "team", label: "Agent team", icon: Headset },
          { key: "campaigns", label: "Campaigns", icon: Megaphone },
          { key: "billing", label: "Billing", icon: CreditCard },
        ].map((s) => (
          <Button
            key={s.key}
            size="sm"
            variant={section === s.key ? "default" : "outline"}
            className="gap-1.5 text-xs"
            onClick={() => setSection(s.key)}
          >
            <s.icon className="size-3" /> {s.label}
          </Button>
        ))}
      </div>
      {section === "customers" && <CustomersCard />}
      {section === "faq" && <FaqCard />}
      {section === "team" && <TeamCard />}
      {section === "campaigns" && <CampaignsCard />}
      {section === "billing" && <BillingCard />}
    </div>
  );
}

// ============================================
// Customers 360
// ============================================
function CustomersCard() {
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
      toast.success("Notes saved");
      void mutate();
    } catch (e) { toast.error((e as Error).message); }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Users className="size-4 text-primary" /> Customers <span className="text-[10px] text-muted-foreground">(unified 360 view)</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search name or channel id…" className="h-8 pl-8 text-xs" />
        </div>
        {!customers || customers.length === 0 ? (
          <EmptyState icon={Users} title="No customers yet" description="Customers appear after their first inbound message." />
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              {(customers as CustomerProfile[]).map((c) => (
                <button
                  key={c.profile_id}
                  onClick={() => { setSelected(c); setNotes(c.notes || ""); }}
                  className={`w-full text-left rounded-md border p-2.5 transition-colors ${selected?.profile_id === c.profile_id ? "border-primary bg-primary/5" : "border-border hover:bg-muted/40"}`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs font-medium truncate">{c.display_name || c.platform_user_id}</p>
                    <Badge variant="outline" className="h-4 px-1 text-[10px] uppercase">{c.platform}</Badge>
                  </div>
                  <p className="text-[11px] text-muted-foreground mt-0.5">{c.platform_user_id} · {c.total_sessions} sessions</p>
                </button>
              ))}
            </div>
            {selected && detail && (
              <div className="space-y-2 rounded-md border border-border p-3">
                <div className="flex items-center justify-between">
                  <p className="text-xs font-semibold">{detail.profile.display_name || detail.profile.platform_user_id}</p>
                  <Badge variant="outline" className="h-4 px-1 text-[10px] uppercase">{detail.profile.platform}</Badge>
                </div>
                <p className="text-[11px] text-muted-foreground">{detail.profile.platform_user_id} · seen {detail.profile.last_seen_at ? new Date(detail.profile.last_seen_at).toLocaleString() : "—"}</p>
                <div className="grid grid-cols-3 gap-1.5 text-center">
                  {(["total_sessions", "total_messages"] as const).map((k) => (
                    <div key={k} className="rounded bg-muted/40 py-1.5">
                      <p className="text-sm font-semibold">{detail.profile[k]}</p>
                      <p className="text-[10px] text-muted-foreground">{k === "total_sessions" ? "Sessions" : "Messages"}</p>
                    </div>
                  ))}
                </div>
                <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Customer notes…" rows={2} className="text-xs resize-none" />
                <Button size="sm" className="h-7 text-xs" onClick={saveNotes}>Save notes</Button>
                {detail.sessions.length > 0 && (
                  <div className="space-y-1">
                    <p className="text-[11px] font-medium text-muted-foreground">Recent sessions</p>
                    {detail.sessions.slice(0, 5).map((sess) => (
                      <div key={sess.session_id} className="rounded border border-border p-2">
                        <div className="flex items-center gap-1.5">
                          <p className="text-[11px] font-medium truncate flex-1">{sess.title || "Untitled"}</p>
                          {sess.sentiment === "negative" && <Badge variant="destructive" className="h-3.5 px-1 text-[9px]">angry</Badge>}
                          <Badge variant="outline" className="h-3.5 px-1 text-[9px]">{sess.status}</Badge>
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
  const { data, mutate } = useSWR("faq-suggestions", () => listFaqSuggestions("pending"));
  const [accepting, setAccepting] = React.useState<number | null>(null);

  const handleAccept = async (id: number) => {
    setAccepting(id);
    try {
      await acceptFaqSuggestion(id, "");
      toast.success("Added to knowledge base");
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
          <Lightbulb className="size-4 text-warning" /> FAQ mining <span className="text-[10px] text-muted-foreground">(self-learning knowledge base)</span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {!data || data.length === 0 ? (
          <EmptyState icon={Lightbulb} title="No suggestions yet" description="Repeated unanswered questions appear here for one-click conversion into knowledge docs." />
        ) : (
          <div className="space-y-2">
            {(data as FaqSuggestion[]).map((f) => (
              <div key={f.suggestion_id} className="rounded-md border border-border p-3 space-y-1.5">
                <div className="flex items-start justify-between gap-2">
                  <p className="text-xs font-medium flex-1">{f.question}</p>
                  <Badge variant="secondary" className="h-4 px-1 text-[10px]">{f.frequency}×</Badge>
                </div>
                {f.answer && <p className="text-[11px] text-muted-foreground line-clamp-2">{f.answer}</p>}
                <div className="flex gap-1.5">
                  <Button size="sm" variant="default" className="h-6 text-[11px] gap-1" onClick={() => handleAccept(f.suggestion_id)} disabled={accepting === f.suggestion_id}>
                    {accepting === f.suggestion_id ? <Loader2 className="size-3 animate-spin" /> : <BookOpenCheck className="size-3" />} Add to KB
                  </Button>
                  <Button size="sm" variant="ghost" className="h-6 text-[11px] gap-1 text-muted-foreground" onClick={() => handleDismiss(f.suggestion_id)}>
                    <XCircle className="size-3" /> Dismiss
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
      toast.success("Agent added");
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
          <Headset className="size-4 text-primary" /> Agent team
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="rounded-md border border-border p-3 space-y-2 bg-muted/30">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
            <Input placeholder="Agent user_id" value={agentId} onChange={(e) => setAgentId(e.target.value)} className="h-8 text-xs" />
            <Input placeholder="Display name" value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
            <Input placeholder="Skills (comma: sales,support)" value={skills} onChange={(e) => setSkills(e.target.value)} className="h-8 text-xs" />
          </div>
          <Button size="sm" className="h-7 text-xs gap-1" onClick={handleAdd} disabled={adding || !agentId}>
            {adding ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />} Add agent
          </Button>
        </div>
        {!team || team.length === 0 ? (
          <EmptyState icon={Headset} title="No agents yet" description="Add team members to distribute handoffs." />
        ) : (
          <div className="space-y-1.5">
            {(team as AgentMember[]).map((a) => (
              <div key={a.team_id} className="rounded-md border border-border p-2.5 flex items-center gap-2">
                <div className="flex-1 min-w-0">
                  <p className="text-xs font-medium">{a.display_name || a.username}</p>
                  <p className="text-[11px] text-muted-foreground">{a.email}</p>
                  <div className="flex gap-1 mt-1 flex-wrap">
                    {a.skills.map((sk) => <Badge key={sk} variant="outline" className="h-4 px-1 text-[10px]">{sk}</Badge>)}
                  </div>
                </div>
                <button onClick={() => handleRemove(a.team_id)} className="text-muted-foreground hover:text-destructive" title="Remove"><Trash2 className="size-3.5" /></button>
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
      toast.success("Campaign scheduled");
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
          <Megaphone className="size-4 text-primary" /> Marketing campaigns <span className="text-[10px] text-muted-foreground">(WhatsApp templates — 24h window exempt)</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="rounded-md border border-border p-3 space-y-2 bg-muted/30">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <Input placeholder="Campaign name" value={name} onChange={(e) => setName(e.target.value)} className="h-8 text-xs" />
            <Input placeholder="Config id" value={configId} onChange={(e) => setConfigId(e.target.value)} className="h-8 text-xs" />
            <Input placeholder="Template name" value={template} onChange={(e) => setTemplate(e.target.value)} className="h-8 text-xs" />
            <Input placeholder="Body params (comma)" value={params} onChange={(e) => setParams(e.target.value)} className="h-8 text-xs" />
          </div>
          <Input type="datetime-local" value={scheduled} onChange={(e) => setScheduled(e.target.value)} className="h-8 text-xs" />
          <Button size="sm" className="h-7 text-xs gap-1" onClick={handleCreate} disabled={creating || !configId || !template || !scheduled}>
            {creating ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />} Schedule
          </Button>
        </div>
        {!data || data.length === 0 ? (
          <EmptyState icon={Megaphone} title="No campaigns" />
        ) : (
          <div className="space-y-1.5">
            {(data as CampaignItem[]).map((c) => (
              <div key={c.campaign_id} className="rounded-md border border-border p-2.5 flex items-center gap-2">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-1.5">
                    <p className="text-xs font-medium truncate">{c.name}</p>
                    <Badge variant={c.status === "done" ? "success" : c.status === "scheduled" ? "secondary" : "outline"} className="h-4 px-1 text-[10px]">{c.status}</Badge>
                  </div>
                  <p className="text-[11px] text-muted-foreground mt-0.5">
                    {c.template_name} · {new Date(c.scheduled_at).toLocaleString()} · {c.sent_count} sent
                  </p>
                </div>
                {c.status === "scheduled" && (
                  <button onClick={() => handleCancel(c.campaign_id)} className="text-muted-foreground hover:text-destructive" title="Cancel"><XCircle className="size-3.5" /></button>
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
  const { data, mutate } = useSWR("billing", getBilling);
  const [planning, setPlanning] = React.useState<string | null>(null);

  const handlePlan = async (p: string) => {
    setPlanning(p);
    try {
      await setPlan(p);
      toast.success(`Plan updated to ${p}`);
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
          <CreditCard className="size-4 text-primary" /> Billing & quota <span className="text-[10px] text-muted-foreground">(tenant: {user?.username})</span>
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
                <span>Messages</span><span>{info.messages_used} / {info.monthly_message_quota}</span>
              </div>
              <div className="h-1.5 rounded bg-muted overflow-hidden">
                <div className="h-full bg-primary transition-all" style={{ width: `${msgPct}%` }} />
              </div>
            </div>
            <div className="text-[11px] text-muted-foreground">
              Documents: {info.docs_used} / {info.monthly_doc_quota} · Cycle ends {new Date(info.cycle_end).toLocaleDateString()}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
