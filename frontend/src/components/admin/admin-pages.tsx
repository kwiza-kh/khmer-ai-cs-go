"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import useSWR from "swr";
import Link from "next/link";
import { listUsers, updateUserRole, listModelConfigs, listAvailableModels, listVertexRegions, testModelConfig, updateModelConfig, getDefaultSystemPrompt, listPromptVersions, restorePromptVersion, listPersonas, createPersona, updatePersona, deletePersona, putPersonaBinding, deletePersonaBinding, type AvailableModel, type ModelItem, type ModelProvider, type PaginatedResponse, type PersonaBinding, type PersonaItem, type PersonasResponse, listTeam, removeAgent, updateTeamMemberPermissions, listTeamInvites, listTeamInviteHistory, createTeamInvite, revokeTeamInvite, type AgentMember, type InviteHistoryEntry, type UserItem, type UsersStats } from "@/lib/api";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { AlertTriangle, BarChart3, Bot, Clock, Copy, Cpu, Headset, KeyRound, MessageSquare, Pencil, Plus, RefreshCw, Send, SlidersHorizontal, Sparkles, Trash2, TrendingUp, Users, X, Zap, Download, ShieldCheck, Search, UserCheck, UserPlus, Lock, History, RotateCcw, type LucideIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { toast } from "sonner";

import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { getBillingCatalog } from "@/lib/billing-api";
import { useAuth } from "@/lib/auth-client";
import { fmtDate, fmtDateTime, fmtInt } from "@/lib/format";
import { EmptyState } from "@/components/empty-state";
import { confirmDelete } from "@/lib/confirm-delete";
import { PageHeader } from "@/components/page-header";
import { AnalyticsPanel } from "@/components/admin/analytics-panel";
import { FeedbackTab } from "@/components/admin/feedback-tab";
import { OperationsTab } from "@/components/admin/operations-tab";
import { GrowthTab } from "@/components/admin/growth-tab";
import { ReportsTab } from "@/components/admin/reports-tab";
import { EnterpriseTab } from "@/components/admin/enterprise-tab";

type UsersResponse = PaginatedResponse<UserItem> & { stats?: UsersStats };

function AdminPageFrame({
  icon,
  title,
  description,
  children,
  actions,
  maxWidth = "max-w-6xl",
}: {
  icon: LucideIcon;
  title: string;
  description?: string;
  children: React.ReactNode;
  actions?: React.ReactNode;
  /** 内容列宽。表单为主的页面用 max-w-4xl —— 一行 150 字符的输入框没法读。 */
  maxWidth?: string;
}) {
  const { t } = useI18n();
  return (
    <div className="flex h-full flex-col">
      <PageHeader icon={icon} kicker={t("nav.administration")} title={title} description={description} actions={actions} />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className={cn("mx-auto w-full", maxWidth)}>{children}</div>
      </div>
    </div>
  );
}

function RefreshAction({ onClick, refreshing }: { onClick: () => void; refreshing: boolean }) {
  const { t } = useI18n();
  return (
    <Button variant="ghost" size="sm" onClick={onClick} disabled={refreshing} className="h-7 gap-2 text-xs">
      <RefreshCw className={cn("size-3", refreshing && "animate-spin")} />
      {t("admin.refresh")}
    </Button>
  );
}

function PageLoadingState() {
  const { t } = useI18n();
  return <div className="py-10 text-center text-sm text-muted-foreground">{t("settings.loading")}</div>;
}

/**
 * Personas — the console side of internal/persona.
 *
 * A persona replaces the tenant's system prompt for the turns it is bound to,
 * and a binding chooses those turns (session beats conversation beats global).
 * The page edits both together because apart they do nothing: an unbound persona
 * never runs, and a binding with no persona has nothing to say.
 */
export function PersonasAdminPage() {
  const { t } = useI18n();
  const { data, isLoading, mutate } = useSWR<PersonasResponse>("admin-personas", listPersonas);
  const personas = data?.personas ?? [];
  // The server ships its own precedence list so the editor cannot offer a scope
  // the resolver would never match.
  const scopes = data?.scopes ?? ["session", "conversation", "global"];

  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<PersonaItem | null>(null);
  const [form, setForm] = useState({ name: "", systemPrompt: "", beginDialogs: "", errorReply: "" });
  const [saving, setSaving] = useState(false);

  const openCreate = () => {
    setEditing(null);
    setForm({ name: "", systemPrompt: "", beginDialogs: "", errorReply: "" });
    setOpen(true);
  };

  const openEdit = (persona: PersonaItem) => {
    setEditing(persona);
    setForm({
      name: persona.name,
      systemPrompt: persona.system_prompt,
      // One opener per line. A textarea is the only editor that survives a paste
      // of real conversational prose.
      beginDialogs: persona.begin_dialogs.join("\n"),
      errorReply: persona.error_reply,
    });
    setOpen(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      const input = {
        name: form.name,
        system_prompt: form.systemPrompt,
        begin_dialogs: form.beginDialogs.split("\n").map((line) => line.trim()).filter(Boolean),
        error_reply: form.errorReply,
      };
      if (editing) await updatePersona(editing.persona_id, input);
      else await createPersona(input);
      await mutate();
      setOpen(false);
      toast.success(t("personas.saved"));
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <AdminPageFrame
      icon={Bot}
      title={t("personas.title")}
      description={t("personas.subtitle")}
      actions={
        <div className="flex items-center gap-2">
          <RefreshAction onClick={() => void mutate()} refreshing={isLoading} />
          <Button size="sm" className="h-7 gap-1.5 text-xs" onClick={openCreate}>
            <Plus className="size-3" />{t("personas.new")}
          </Button>
        </div>
      }
    >
      {isLoading ? <PageLoadingState /> : personas.length === 0 ? (
        <EmptyState icon={Bot} title={t("personas.empty")} description={t("personas.emptyHint")} />
      ) : (
        <div className="space-y-3">
          {personas.map((persona) => (
            <PersonaCard
              key={persona.persona_id}
              persona={persona}
              scopes={scopes}
              onEdit={() => openEdit(persona)}
              onChanged={() => void mutate()}
            />
          ))}
        </div>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[85vh] overflow-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editing ? t("personas.edit") : t("personas.new")}</DialogTitle>
            <DialogDescription>{t("personas.formHint")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <label className="text-xs font-medium">{t("personas.name")}</label>
              <Input
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                placeholder={t("personas.namePlaceholder")}
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium">{t("personas.systemPrompt")}</label>
              <Textarea
                rows={7}
                value={form.systemPrompt}
                onChange={(event) => setForm({ ...form, systemPrompt: event.target.value })}
                placeholder={t("personas.systemPromptPlaceholder")}
              />
              <p className="text-[11px] text-muted-foreground">{t("personas.systemPromptHint")}</p>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium">{t("personas.beginDialogs")}</label>
              <Textarea
                rows={4}
                value={form.beginDialogs}
                onChange={(event) => setForm({ ...form, beginDialogs: event.target.value })}
                placeholder={t("personas.beginDialogsPlaceholder")}
              />
              <p className="text-[11px] text-muted-foreground">{t("personas.beginDialogsHint")}</p>
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium">{t("personas.errorReply")}</label>
              <Input
                value={form.errorReply}
                onChange={(event) => setForm({ ...form, errorReply: event.target.value })}
                placeholder={t("personas.errorReplyPlaceholder")}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>{t("common.cancel")}</Button>
            <Button
              size="sm"
              disabled={saving || !form.name.trim() || !form.systemPrompt.trim()}
              onClick={() => void save()}
            >
              {saving ? t("common.saving") : t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </AdminPageFrame>
  );
}

function PersonaCard({
  persona,
  scopes,
  onEdit,
  onChanged,
}: {
  persona: PersonaItem;
  scopes: string[];
  onEdit: () => void;
  onChanged: () => void;
}) {
  const { t, tf } = useI18n();
  const [scope, setScope] = useState(scopes[0] ?? "session");
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);

  const bind = async () => {
    // A global binding has no target — the server normalises it away — so the
    // field is hidden for it rather than filled in and silently discarded.
    const value = scope === "global" ? "" : target.trim();
    if (scope !== "global" && !value) return;
    setBusy(true);
    try {
      await putPersonaBinding({ persona_id: persona.persona_id, scope, target: value });
      setTarget("");
      onChanged();
      toast.success(t("personas.bound"));
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const unbind = (binding: PersonaBinding) =>
    void confirmDelete(
      tf("personas.unbindConfirm", { scope: binding.scope, target: binding.target || t("personas.scope.global") }),
      () => deletePersonaBinding(binding.scope, binding.target),
      onChanged,
    );

  const remove = () =>
    void confirmDelete(
      tf("personas.deleteConfirm", { name: persona.name }),
      () => deletePersona(persona.persona_id),
      onChanged,
    );

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 space-y-1.5">
            <CardTitle className="text-sm">{persona.name}</CardTitle>
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="secondary" className="font-mono text-[10px]">{persona.persona_id}</Badge>
              {persona.bindings.length === 0 ? (
                <span className="text-[11px] text-muted-foreground">{t("personas.unbound")}</span>
              ) : (
                persona.bindings.map((binding) => (
                  <Badge
                    key={`${binding.scope}:${binding.target}`}
                    variant="outline"
                    className="gap-1 text-[10px]"
                  >
                    {binding.scope}{binding.target ? `:${binding.target}` : ""}
                    <button
                      type="button"
                      onClick={() => unbind(binding)}
                      aria-label={t("personas.unbind")}
                      className="text-muted-foreground hover:text-destructive"
                    >
                      <X className="size-3" />
                    </button>
                  </Badge>
                ))
              )}
            </div>
          </div>
          <div className="flex items-center gap-1">
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 text-xs" onClick={onEdit}>
              <Pencil className="size-3" />{t("common.edit")}
            </Button>
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 text-xs text-destructive" onClick={remove}>
              <Trash2 className="size-3" />{t("common.delete")}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="whitespace-pre-wrap text-xs text-muted-foreground line-clamp-3">{persona.system_prompt}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={scope} onValueChange={(value) => setScope(value || scopes[0] || "session")}>
            <SelectTrigger className="h-7 w-44 text-xs"><SelectValue /></SelectTrigger>
            <SelectContent>
              {scopes.map((option) => (
                <SelectItem key={option} value={option} className="text-xs">
                  {t(`personas.scope.${option}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {scope !== "global" && (
            <Input
              value={target}
              onChange={(event) => setTarget(event.target.value)}
              placeholder={t("personas.targetPlaceholder")}
              className="h-7 w-64 text-xs"
            />
          )}
          <Button
            size="sm"
            className="h-7 gap-1.5 text-xs"
            disabled={busy || (scope !== "global" && !target.trim())}
            onClick={() => void bind()}
          >
            <Plus className="size-3" />{t("personas.bind")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

export function DashboardPage() {
  const { t } = useI18n();
  const [tab, setTab] = useState("analytics");

  return (
    <AdminPageFrame icon={BarChart3} title={t("admin.dashTitle")}>
      <Tabs value={tab} onValueChange={(value) => setTab(value || "analytics")}>
        <TabsList className="h-auto w-auto flex-wrap">
          <TabsTrigger value="analytics" className="gap-1.5 text-xs"><BarChart3 className="size-3" />{t("admin.tabAnalytics")}</TabsTrigger>
          <TabsTrigger value="feedback" className="gap-1.5 text-xs"><TrendingUp className="size-3" />{t("admin.tabFeedback")}</TabsTrigger>
          <TabsTrigger value="operations" className="gap-1.5 text-xs"><Clock className="size-3" />{t("admin.tabOperations")}</TabsTrigger>
          <TabsTrigger value="growth" className="gap-1.5 text-xs"><Sparkles className="size-3" />{t("admin.tabGrowth")}</TabsTrigger>
          <TabsTrigger value="reports" className="gap-1.5 text-xs"><Download className="size-3" />{t("admin.tabReports")}</TabsTrigger>
          <TabsTrigger value="enterprise" className="gap-1.5 text-xs"><ShieldCheck className="size-3" />{t("admin.tabEnterprise")}</TabsTrigger>
        </TabsList>

        <TabsContent value="analytics" className="mt-4">
          <AnalyticsPanel days={30} />
        </TabsContent>
        <TabsContent value="feedback" className="mt-4">
          <FeedbackTab />
        </TabsContent>
        <TabsContent value="operations" className="mt-4">
          <OperationsTab />
        </TabsContent>
        <TabsContent value="growth" className="mt-4">
          <GrowthTab />
        </TabsContent>
        <TabsContent value="reports" className="mt-4">
          <ReportsTab />
        </TabsContent>
        <TabsContent value="enterprise" className="mt-4">
          <EnterpriseTab />
        </TabsContent>
      </Tabs>
    </AdminPageFrame>
  );
}

function UserStatCard({
  icon: Icon,
  label,
  value,
  prefix = "",
  tone = "default",
}: {
  icon: LucideIcon;
  label: string;
  value?: number;
  prefix?: string;
  tone?: "default" | "success" | "primary" | "warning";
}) {
  const toneClass = {
    default: "bg-muted text-muted-foreground ring-border/60",
    success: "bg-success/10 text-success ring-success/20",
    primary: "bg-primary/10 text-primary ring-primary/15",
    warning: "bg-warning/10 text-warning ring-warning/20",
  }[tone];
  return (
    <Card>
      <CardContent className="flex items-center gap-3 p-4">
        <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-xl ring-1 ring-inset", toneClass)}>
          <Icon className="size-4" />
        </div>
        <div className="min-w-0">
          <p className="truncate text-[11px] font-medium text-muted-foreground">{label}</p>
          <p className="text-xl font-semibold tabular-nums text-foreground">
            {value == null ? "—" : `${prefix}${fmtInt(value)}`}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

// 最高权限用 primary 实心胶囊, 不再借用 warning(琥珀) —— 那会让“最高权限”读成“警告”。
//
// ⚠️ 必须成对写 light / dark: SelectTrigger 基类带 `dark:bg-input/30`, 它在
// 构建产物里的顺序晚于基础 `bg-*`, 深色下会直接盖掉只写了 light 的写法
// (曾导致 platform_admin 变成深底 + 深字, 对比度 1.01:1 完全不可见)。
const ROLE_PILL: Record<string, string> = {
  platform_admin: "bg-primary text-primary-foreground dark:bg-primary dark:text-primary-foreground",
  admin: "bg-primary/12 text-primary dark:bg-primary/15 dark:text-primary",
  user: "bg-muted text-muted-foreground dark:bg-muted/60 dark:text-muted-foreground",
};

// Sign-in methods an account can carry. A user may have several at once — a
// passwordless Google or Telegram account can set an initial password later —
// so the column renders one badge per method rather than picking a winner.
// Keys mirror the auth method names the backend reports per account
// ("password" | "google" | "telegram").
const AUTH_LABEL: Record<string, string> = {
  google: "admin.authGoogle",
  telegram: "admin.authTelegram",
};
AUTH_LABEL["password"] = "admin.authPassword";

// Invite history states. Exhausted/expired are neutral (the link did its job or
// aged out); revoked is destructive-styled because someone killed it on purpose.
const INVITE_STATUS: Record<InviteHistoryEntry["status"], { key: string; className: string }> = {
  pending: { key: "admin.inviteStatusPending", className: "bg-success/10 text-success" },
  exhausted: { key: "admin.inviteStatusExhausted", className: "bg-muted text-muted-foreground" },
  expired: { key: "admin.inviteStatusExpired", className: "bg-muted text-muted-foreground" },
  revoked: { key: "admin.inviteStatusRevoked", className: "bg-destructive/10 text-destructive" },
};

// What a seat may do. The keys are the wire contract (member_permissions.go);
// only wired capabilities appear here, and the hints say plainly what each one
// grants. Channels, widget tokens, billing and the team stay owner-only and are
// deliberately absent from this list.
const MEMBER_PERMISSIONS: { key: string; label: string; hint: string }[] = [
  { key: "inbox_view", label: "admin.permInboxView", hint: "admin.permInboxViewHint" },
  { key: "inbox_reply", label: "admin.permInboxReply", hint: "admin.permInboxReplyHint" },
  { key: "inbox_takeover", label: "admin.permInboxTakeover", hint: "admin.permInboxTakeoverHint" },
  { key: "inbox_assign", label: "admin.permInboxAssign", hint: "admin.permInboxAssignHint" },
  { key: "knowledge_view", label: "admin.permKnowledgeView", hint: "admin.permKnowledgeViewHint" },
  { key: "knowledge_edit", label: "admin.permKnowledgeEdit", hint: "admin.permKnowledgeEditHint" },
];

export function UsersAdminPage() {
  const { t, tf } = useI18n();
  const { user } = useAuth();
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  useEffect(() => {
    const timer = window.setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [searchInput]);

  const { data, isLoading, mutate } = useSWR<UsersResponse>(
    ["admin-users", search],
    () => listUsers(1, 100, search),
  );
  const users = data?.data ?? [];
  const userTotal = data?.total ?? 0;
  const stats = data?.stats;

  // Agent seats. A seat IS the agent_teams row (GET /team), and the allowance
  // rides on the billing catalogue the layout already caches under
  // "billing-catalog" — so the card costs no extra request on a warm page.
  // This lived in the dashboard's Growth tab; the roster below is the same set
  // of people (owner + members), so a second screen only ever disagreed.
  //
  // Seats are filled by invite, not by typing a user_id: the invitee accepts
  // the link while logged in and the server binds their own account, so the
  // owner never needs an identifier and nobody can claim a stranger's signup.
  const { data: team, mutate: mutateTeam } = useSWR("agent-team", listTeam);
  const { data: catalog, mutate: mutateCatalog } = useSWR("billing-catalog", getBillingCatalog);
  // platform_admin's user list spans tenants, so there is no single allowance to
  // show — and seat writes are tenant-scoped server-side.
  const isPlatformAdmin = user?.role === "platform_admin";
  const { data: invites, mutate: mutateInvites } = useSWR(isPlatformAdmin ? null : "team-invites", listTeamInvites);
  // The full log: every link ever minted, including revoked/expired ones — the
  // pending list above is only the actionable slice. This is what answers "who
  // did we invite, through which link, and when does it stop working".
  const { data: history, mutate: mutateHistory } = useSWR(isPlatformAdmin ? null : "team-invite-history", listTeamInviteHistory);
  const [inviteName, setInviteName] = useState("");
  const [inviteSkills, setInviteSkills] = useState("");
  // Expiry and capacity are per link: 1/7/30 days and 1..remaining seats.
  const [inviteHours, setInviteHours] = useState("168");
  const [inviteMaxUses, setInviteMaxUses] = useState("1");
  const [creating, setCreating] = useState(false);
  // The link lives only in this state: the server keeps a hash, so a reload
  // means reissuing (and revoking the old one) rather than re-reading it.
  const [inviteUrl, setInviteUrl] = useState("");
  // Permission dialog: the draft starts from the row's effective set, so saving
  // an untouched dialog is a no-op rather than a silent reset to the defaults.
  const [permMember, setPermMember] = useState<AgentMember | null>(null);
  const [permDraft, setPermDraft] = useState<Record<string, boolean>>({});
  const [savingPerms, setSavingPerms] = useState(false);
  const seatsUsed = catalog?.current?.seats_used ?? null;
  const seatsQuota = catalog?.current?.seats_quota ?? null;
  const seatsFull = seatsUsed !== null && seatsQuota !== null && seatsUsed >= seatsQuota;
  const seatPercent = seatsUsed !== null && seatsQuota
    ? Math.min(100, Math.round((seatsUsed / seatsQuota) * 100))
    : 0;
  const memberByAgent = new Map((team ?? []).map((member) => [member.agent_user_id, member]));
  const teamIdByAgent = new Map((team ?? []).map((member) => [member.agent_user_id, member.team_id]));
  const ownerId = user?.user_id;

  // Every seat write changes all lists: a new invite, an accepted one, or a
  // released seat moves the roster, the counters, the plan card and the log.
  const refreshSeats = async () => {
    await Promise.all([mutate(), mutateTeam(), mutateCatalog(), mutateInvites(), mutateHistory()]);
  };

  const handleCreateInvite = async () => {
    setCreating(true);
    try {
      const created = await createTeamInvite({
        display_name: inviteName.trim(),
        skills: inviteSkills.split(",").map((s) => s.trim()).filter(Boolean),
        expires_in_hours: Number(inviteHours) || 168,
        max_uses: Number(inviteMaxUses) || 1,
      });
      setInviteUrl(created.url);
      setInviteName("");
      setInviteSkills("");
      setInviteMaxUses("1");
      await Promise.all([mutateInvites(), mutateHistory()]);
      toast.success(t("admin.inviteCreated"));
    } catch (error: unknown) {
      toast.error((error as Error).message);
    } finally {
      setCreating(false);
    }
  };

  const handleCopyInvite = async () => {
    try {
      await navigator.clipboard.writeText(inviteUrl);
      toast.success(t("admin.inviteCopied"));
    } catch {
      /* clipboard blocked — the read-only input below still holds the link */
    }
  };

  const handleRevokeInvite = async (inviteId: number) => {
    try {
      await revokeTeamInvite(inviteId);
      toast.success(t("admin.inviteRevoked"));
      // Revoking keeps the row (the server marks it), so the log re-renders
      // with the link now reading as 已撤销 while the pending list drops it.
      await Promise.all([mutateInvites(), mutateHistory()]);
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  const openPermissions = (member: AgentMember) => {
    setPermMember(member);
    setPermDraft({ ...(member.permissions ?? {}) });
  };

  const handleSavePermissions = async () => {
    if (!permMember) return;
    setSavingPerms(true);
    try {
      await updateTeamMemberPermissions(permMember.team_id, permDraft);
      toast.success(t("admin.permSaved"));
      setPermMember(null);
      await mutateTeam();
    } catch (error: unknown) {
      toast.error((error as Error).message);
    } finally {
      setSavingPerms(false);
    }
  };

  const handleRemoveSeat = async (teamId: number) => {
    try {
      await removeAgent(teamId);
      toast.success(t("admin.seatRemoved"));
      await refreshSeats();
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  const copyUserId = async (userId: number) => {
    try {
      await navigator.clipboard.writeText(String(userId));
      toast.success(t("admin.userIdCopied"));
    } catch {
      /* clipboard blocked — the id stays visible in the row, so nothing to announce */
    }
  };

  const handleUpdateRole = async (userId: number, role: string) => {
    try {
      await updateUserRole(userId, role);
      await mutate();
      toast.success(t("admin.roleUpdated"));
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  const handleToggleActive = async (userId: number, isActive: boolean) => {
    try {
      await updateUserRole(userId, undefined, isActive);
      await mutate();
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  return (
    <AdminPageFrame
      icon={Users}
      title={t("admin.usersTitle")}
      description={t("admin.usersSubtitle")}
      actions={
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={searchInput}
              onChange={(event) => setSearchInput(event.target.value)}
              placeholder={t("admin.searchUsers")}
              className="h-8 w-44 pl-8 text-xs sm:w-64"
            />
          </div>
          <RefreshAction onClick={() => void mutate()} refreshing={isLoading} />
        </div>
      }
    >
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <UserStatCard icon={Users} label={t("admin.statTotalUsers")} value={stats?.total} />
        <UserStatCard icon={UserCheck} label={t("admin.statActive")} value={stats?.active} tone="success" />
        <UserStatCard icon={UserPlus} label={t("admin.statNewWeek")} value={stats?.new_week} prefix="+" tone="primary" />
        <UserStatCard icon={ShieldCheck} label={t("admin.statAdmins")} value={stats?.admins} tone="warning" />
      </div>

      {!isPlatformAdmin && (
        <Card className="mt-4">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-sm">
              <Headset className="text-primary size-4" />
              {t("admin.seatCardTitle")}
              <span className="text-[11px] font-normal tabular-nums text-muted-foreground">
                {tf("admin.seatUsage", {
                  used: seatsUsed === null ? "—" : seatsUsed,
                  limit: seatsQuota === null ? t("bl.unlimited") : seatsQuota,
                })}
              </span>
            </CardTitle>
            <CardDescription className="text-[11px]">{t("admin.seatOwnerNote")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {seatsUsed !== null && seatsQuota !== null && (
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div className="bg-primary h-full rounded-full" style={{ width: `${seatPercent}%` }} />
              </div>
            )}
            {seatsFull && (
              <p className="flex items-center gap-1 text-[11px] text-warning">
                <AlertTriangle className="size-3" />
                {t("admin.seatFull")}
                <Link href="/billing" className="text-primary hover:underline">{t("bl.title")}</Link>
              </p>
            )}
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <Input
                value={inviteName}
                onChange={(event) => setInviteName(event.target.value)}
                placeholder={t("admin.inviteNamePh")}
                className="h-8 text-xs"
              />
              <Input
                value={inviteSkills}
                onChange={(event) => setInviteSkills(event.target.value)}
                placeholder={t("admin.inviteSkillsPh")}
                className="h-8 text-xs"
              />
              <Select value={inviteHours} onValueChange={(value) => setInviteHours(value ?? "168")}>
                <SelectTrigger className="h-8 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="24">{t("admin.inviteExpiry24h")}</SelectItem>
                  <SelectItem value="168">{t("admin.inviteExpiry7d")}</SelectItem>
                  <SelectItem value="720">{t("admin.inviteExpiry30d")}</SelectItem>
                </SelectContent>
              </Select>
              <Input
                value={inviteMaxUses}
                onChange={(event) => setInviteMaxUses(event.target.value.replace(/[^0-9]/g, ""))}
                inputMode="numeric"
                placeholder={t("admin.inviteMaxUses")}
                className="h-8 text-xs"
              />
            </div>
            <Button
              size="sm"
              className="h-8 w-full gap-1 text-xs"
              onClick={() => void handleCreateInvite()}
              disabled={creating || seatsFull}
            >
              {creating ? <RefreshCw className="size-3 animate-spin" /> : <UserPlus className="size-3" />}
              {t("admin.inviteCreate")}
            </Button>
            <p className="text-[11px] text-muted-foreground">{t("admin.inviteHint")}</p>
            {inviteUrl && (
              <div className="space-y-1.5 rounded-md border border-border bg-muted/30 p-2">
                <div className="flex items-center gap-2">
                  <Input
                    readOnly
                    value={inviteUrl}
                    onFocus={(event) => event.currentTarget.select()}
                    className="h-8 flex-1 text-xs"
                  />
                  <Button size="sm" className="h-8 gap-1 text-xs" onClick={() => void handleCopyInvite()}>
                    <Copy className="size-3" />{t("admin.inviteCopy")}
                  </Button>
                </div>
                <p className="flex items-center gap-2 text-[11px] text-warning">
                  <AlertTriangle className="size-3" />
                  {t("admin.inviteOnce")}
                  <button
                    type="button"
                    onClick={() => setInviteUrl("")}
                    className="ml-auto shrink-0 text-muted-foreground hover:text-foreground"
                  >
                    {t("admin.inviteAnother")}
                  </button>
                </p>
              </div>
            )}
            {(invites ?? []).length > 0 && (
              <div className="space-y-1 border-t border-border pt-2">
                <p className="text-[11px] font-medium text-muted-foreground">{t("admin.invitePending")}</p>
                {(invites ?? []).map((invite) => (
                  <div key={invite.invite_id} className="flex items-center justify-between gap-2 text-[11px]">
                    <span className="truncate text-muted-foreground">
                      {invite.display_name ? `${invite.display_name} · ` : ""}
                      {tf("admin.inviteJoinedCount", { used: invite.use_count, max: invite.max_uses })}
                      {" · "}
                      {tf("admin.inviteExpires", { date: fmtDate(invite.expires_at) })}
                    </span>
                    <button
                      type="button"
                      onClick={() => void handleRevokeInvite(invite.invite_id)}
                      className="shrink-0 text-muted-foreground hover:text-destructive"
                    >
                      {t("admin.inviteRevoke")}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {!isPlatformAdmin && (
        <Card className="mt-4">
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-sm">
              <History className="text-primary size-4" />
              {t("admin.inviteHistory")}
            </CardTitle>
            <CardDescription className="text-[11px]">{t("admin.inviteHistoryHint")}</CardDescription>
          </CardHeader>
          <CardContent>
            {(history ?? []).length === 0 ? (
              <EmptyState icon={History} title={t("admin.inviteHistoryEmpty")} />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="text-xs">{t("admin.colInviteStatus")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colInviteLink")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colInviteCreated")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colInviteExpires")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colInviteJoined")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colInviteNames")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(history ?? []).map((entry) => {
                    const meta = INVITE_STATUS[entry.status] ?? INVITE_STATUS.pending;
                    return (
                      <TableRow key={entry.invite_id}>
                        <TableCell>
                          <Badge variant="outline" className={cn("h-5 px-1.5 text-[11px] font-normal", meta.className)}>
                            {t(meta.key)}
                          </Badge>
                        </TableCell>
                        <TableCell className="font-mono text-[11px] text-muted-foreground">#{entry.fingerprint}</TableCell>
                        <TableCell className="text-[11px] whitespace-nowrap text-muted-foreground">
                          {fmtDateTime(entry.created_at)}
                        </TableCell>
                        <TableCell className="text-[11px] whitespace-nowrap text-muted-foreground">
                          {fmtDateTime(entry.expires_at)}
                        </TableCell>
                        <TableCell className="text-[11px] tabular-nums">
                          {entry.use_count}/{entry.max_uses}
                        </TableCell>
                        <TableCell>
                          {entry.invited.length === 0 ? (
                            <span className="text-[11px] text-muted-foreground">—</span>
                          ) : (
                            <div className="space-y-0.5">
                              {entry.invited.map((invited) => (
                                <p key={`${invited.username}-${invited.used_at}`} className="text-[11px] whitespace-nowrap text-muted-foreground">
                                  <span className="text-foreground">{invited.display_name || invited.username}</span>
                                  {" · "}{invited.username}{" · "}{fmtDateTime(invited.used_at)}
                                </p>
                              ))}
                            </div>
                          )}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}

      <Card className="mt-4">
        <CardHeader className="pb-3">
          <CardTitle className="text-sm">{tf("admin.registeredUsers", { n: userTotal })}</CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading ? <PageLoadingState /> : users.length === 0 ? (
            <EmptyState icon={Users} title={search ? t("admin.noSearchResults") : t("admin.noUsers")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-xs">{t("admin.colUser")}</TableHead>
                  <TableHead className="text-xs">{t("admin.colRole")}</TableHead>
                  <TableHead className="text-xs">{t("admin.colStatus")}</TableHead>
                  <TableHead className="text-xs">{t("admin.colSeat")}</TableHead>
                  <TableHead className="text-xs">{t("admin.colRegistered")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((user) => {
                  // Skills drive routing rules (routing_rules.target_skills), so a
                  // member's skill set stays visible here — the card that used to
                  // show it was removed with the duplicate team screen.
                  const member = memberByAgent.get(user.user_id);
                  return (
                    <TableRow key={user.user_id}>
                      <TableCell>
                        <div className="flex min-w-0 items-center gap-2.5">
                          <span className={cn(
                            "flex size-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold uppercase",
                            ROLE_PILL[user.role] ?? ROLE_PILL.user,
                          )}>
                            {user.username.slice(0, 2)}
                          </span>
                          <div className="min-w-0">
                            <p className="flex items-center gap-1.5 truncate text-xs font-medium text-foreground">
                              {user.username}
                              {(user.auth_methods ?? []).map((method) => (
                                <Badge
                                  key={method}
                                  variant="outline"
                                  className="h-4 shrink-0 gap-1 px-1.5 text-[11px] font-normal text-muted-foreground"
                                >
                                  {AUTH_LABEL[method] ? t(AUTH_LABEL[method]) : method}
                                </Badge>
                              ))}
                            </p>
                            <p className="truncate text-[11px] text-muted-foreground">{user.email}</p>
                            {member && (member.display_name || member.skills.length > 0) && (
                              <p className="mt-0.5 flex flex-wrap items-center gap-1 text-[11px] text-muted-foreground">
                                {member.display_name && <span className="truncate">{member.display_name}</span>}
                                {member.skills.map((skill) => (
                                  <Badge key={skill} variant="outline" className="h-4 px-1 text-[11px] font-normal">
                                    {skill}
                                  </Badge>
                                ))}
                              </p>
                            )}
                            {/* The raw id is an operator tool now: tenants invite, and
                                nothing in the tenant UI needs a user_id any more. */}
                            {isPlatformAdmin && (
                              <button
                                type="button"
                                onClick={() => void copyUserId(user.user_id)}
                                title={t("admin.copyUserId")}
                                className="mt-0.5 inline-flex items-center gap-1 text-[11px] text-muted-foreground/80 transition-colors hover:text-primary"
                              >
                                <Copy className="size-2.5" />#{user.user_id}
                              </button>
                            )}
                          </div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <Select value={user.role} onValueChange={(value) => handleUpdateRole(user.user_id, value || "user")}>
                          <SelectTrigger className={cn("h-6 w-auto min-w-24 gap-1.5 rounded-full border-transparent px-2.5 text-[11px] font-medium shadow-none", ROLE_PILL[user.role] ?? ROLE_PILL.user)}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="user">{t("admin.roleUser")}</SelectItem>
                            <SelectItem value="admin">{t("admin.roleAdmin")}</SelectItem>
                            <SelectItem value="platform_admin">{t("admin.rolePlatformAdmin")}</SelectItem>
                          </SelectContent>
                        </Select>
                      </TableCell>
                      <TableCell>
                        {user.role === "platform_admin" ? (
                          <span
                            title={t("admin.platformAdminLockHint")}
                            className="inline-flex cursor-not-allowed items-center gap-1.5 rounded-full bg-success/10 px-2 py-0.5 text-[11px] font-medium text-success"
                          >
                            <Lock className="size-2.5" />
                            {t("admin.statusActive")}
                          </span>
                        ) : !isPlatformAdmin ? (
                          // Enabling/disabling an account is a platform action: a
                          // tenant owner offboards by removing the seat, not by
                          // locking the person out of the platform entirely.
                          <span
                            title={t("admin.statusTenantLockHint")}
                            className={cn(
                              "inline-flex cursor-not-allowed items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium",
                              user.is_active ? "bg-success/10 text-success" : "bg-muted text-muted-foreground",
                            )}
                          >
                            <span className={cn("size-1.5 rounded-full", user.is_active ? "bg-success" : "bg-muted-foreground/50")} />
                            {user.is_active ? t("admin.statusActive") : t("admin.statusDisabled")}
                          </span>
                        ) : (
                        <button
                          type="button"
                          onClick={() => handleToggleActive(user.user_id, !user.is_active)}
                          title={t("admin.statusToggleHint")}
                          className={cn(
                            "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium transition-colors",
                            user.is_active ? "bg-success/10 text-success hover:bg-success/15" : "bg-muted text-muted-foreground hover:bg-muted/70",
                          )}
                        >
                          <span className={cn("size-1.5 rounded-full", user.is_active ? "bg-success" : "bg-muted-foreground/50")} />
                          {user.is_active ? t("admin.statusActive") : t("admin.statusDisabled")}
                        </button>
                        )}
                      </TableCell>
                      <TableCell>
                        {isPlatformAdmin ? (
                          <span className="text-[11px] text-muted-foreground">—</span>
                        ) : user.user_id === ownerId ? (
                          <Badge variant="outline" className="h-5 px-1.5 text-[11px] font-normal text-muted-foreground">
                            {t("admin.seatOwner")}
                          </Badge>
                        ) : teamIdByAgent.has(user.user_id) ? (
                          <div className="flex items-center gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground hover:text-foreground"
                              onClick={() => {
                                const member = memberByAgent.get(user.user_id);
                                if (member) openPermissions(member);
                              }}
                            >
                              <SlidersHorizontal className="size-3" />{t("admin.permButton")}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground hover:text-destructive"
                              onClick={() => {
                                const teamId = teamIdByAgent.get(user.user_id);
                                if (teamId) void handleRemoveSeat(teamId);
                              }}
                            >
                              <Trash2 className="size-3" />{t("admin.seatRemove")}
                            </Button>
                          </div>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-[11px] text-muted-foreground">
                        {user.created_at ? fmtDate(user.created_at) : "—"}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={permMember !== null} onOpenChange={(open) => { if (!open) setPermMember(null); }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm">
              {t("admin.permTitle")}
              {permMember ? <span className="text-muted-foreground"> · {permMember.display_name || permMember.username}</span> : null}
            </DialogTitle>
            <DialogDescription className="text-[11px]">{t("admin.permHint")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            {MEMBER_PERMISSIONS.map((perm) => {
              const enabled = permDraft[perm.key] ?? false;
              return (
                <button
                  key={perm.key}
                  type="button"
                  onClick={() => setPermDraft((cur) => ({ ...cur, [perm.key]: !enabled }))}
                  className={cn(
                    "flex w-full items-start justify-between gap-3 rounded-md border border-border px-3 py-2 text-left transition-colors",
                    enabled ? "bg-success/5" : "bg-muted/30",
                  )}
                >
                  <span className="min-w-0">
                    <span className="block text-xs font-medium">{t(perm.label)}</span>
                    <span className="block text-[11px] text-muted-foreground">{t(perm.hint)}</span>
                  </span>
                  <Badge
                    variant="outline"
                    className={cn(
                      "mt-0.5 h-5 shrink-0 px-1.5 text-[11px] font-normal",
                      enabled ? "bg-success/10 text-success" : "text-muted-foreground",
                    )}
                  >
                    {enabled ? t("admin.permOn") : t("admin.permOff")}
                  </Badge>
                </button>
              );
            })}
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" className="h-8 text-xs" onClick={() => setPermMember(null)}>
              {t("admin.permCancel")}
            </Button>
            <Button size="sm" className="h-8 gap-1 text-xs" onClick={() => void handleSavePermissions()} disabled={savingPerms}>
              {savingPerms ? <RefreshCw className="size-3 animate-spin" /> : null}
              {t("admin.permSave")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </AdminPageFrame>
  );
}

// PromptHistory renders the system-prompt version history for one model config
// and restores an earlier version on demand.
//
// Fetching is lazy: the list carries full prompt texts, so it is only loaded
// once the operator opens the section rather than on every visit to the page.
function PromptHistory({ configId, onRestored }: { configId: number; onRestored: () => void }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const { data, isLoading, error, mutate } = useSWR(
    open ? ["prompt-history", configId] : null,
    () => listPromptVersions(configId),
  );
  const [restoring, setRestoring] = useState<number | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);

  const versions = data?.versions ?? [];
  const current = data?.current ?? "";
  // The newest entry whose value equals the live one is the state in force.
  // Comparing by value (not by position) keeps this correct even if the newest
  // row was written by a rollback to an older text.
  const currentVersionId = versions.find((v) => v.system_prompt === current)?.version_id;

  const handleRestore = async (versionId: number) => {
    setRestoring(versionId);
    try {
      await restorePromptVersion(configId, versionId);
      toast.success(t("admin.promptRestored"));
      // Reload the history and the model list: the restored value is now the
      // live one, and the draft on screen is stale.
      await mutate();
      onRestored();
    } catch (err: unknown) {
      toast.error((err as Error).message);
    } finally {
      setRestoring(null);
    }
  };

  const sourceLabel = (source: string) =>
    source === "baseline"
      ? t("admin.promptSourceBaseline")
      : source === "rollback"
        ? t("admin.promptSourceRollback")
        : t("admin.promptSourceEdit");

  return (
    <div className="rounded-lg border border-border p-3">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-2 text-xs font-medium"
        onClick={() => setOpen((previous) => !previous)}
      >
        <span className="flex items-center gap-2">
          <History className="size-3.5 text-muted-foreground" />
          {t("admin.promptHistory")}
        </span>
        <span className="text-muted-foreground">
          {open ? t("admin.promptHistoryHide") : t("admin.promptHistoryShow")}
        </span>
      </button>

      {open && (
        <div className="mt-3 space-y-2">
          {isLoading && <p className="text-xs text-muted-foreground">{t("admin.promptLoading")}</p>}
          {error && <p className="text-xs text-destructive">{(error as Error).message}</p>}
          {!isLoading && !error && versions.length === 0 && (
            <p className="text-xs text-muted-foreground">{t("admin.promptHistoryEmpty")}</p>
          )}
          {versions.map((version) => {
            const isCurrent = version.version_id === currentVersionId;
            const isDefault = version.system_prompt.trim() === "";
            const isOpen = expanded === version.version_id;
            return (
              <div
                key={version.version_id}
                className={`rounded-md border p-2 ${isCurrent ? "border-emerald-500/40 bg-emerald-500/5" : "border-border"}`}
              >
                <div className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="font-medium text-foreground">#{version.version_id}</span>
                  <Badge variant="outline" className="text-[10px]">{sourceLabel(version.source)}</Badge>
                  {isCurrent && (
                    <Badge variant="outline" className="border-emerald-500/40 bg-emerald-500/10 text-[10px] text-emerald-700 dark:text-emerald-400">
                      {t("admin.promptCurrent")}
                    </Badge>
                  )}
                  <span className="text-muted-foreground">{new Date(version.created_at).toLocaleString()}</span>
                  {version.changed_by && <span className="text-muted-foreground">· {version.changed_by}</span>}
                </div>
                <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                  {isDefault
                    ? t("admin.promptBuiltinDefault")
                    : version.system_prompt.slice(0, 120)}
                </p>
                <div className="mt-2 flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-6 px-2 text-[11px]"
                    onClick={() => setExpanded(isOpen ? null : version.version_id)}
                  >
                    {isOpen ? t("admin.promptCollapse") : t("admin.promptExpand")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-6 gap-1 px-2 text-[11px]"
                    disabled={isCurrent || restoring === version.version_id}
                    onClick={() => handleRestore(version.version_id)}
                  >
                    <RotateCcw className="size-3" />
                    {restoring === version.version_id ? t("admin.promptRestoring") : t("admin.promptRestore")}
                  </Button>
                </div>
                {isOpen && (
                  <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap rounded border border-border bg-muted/40 p-2 text-[11px] leading-relaxed text-muted-foreground">
                    {isDefault ? t("admin.promptBuiltinDefaultExplain") : version.system_prompt}
                  </pre>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// Which secret the backend is actually serving with, reported per config by
// GET /admin/models (ModelItem.credential_source).
//
// "service_account" means Vertex: the credential is a file on the server
// (GEMINI_VERTEX_SA_FILE), so model_configs.api_key is NOT a credential there
// and is normally empty — an empty key must not be read as "not configured".
// Anything else (including an older backend that sends no value) is AI Studio,
// today's production behaviour.
//
// The private key deliberately never enters the database: it would then travel
// through this page, the request logs and every DB backup, which a 0600 file
// owned by the service account does not.
function usesServiceAccountCredential(model: ModelItem): boolean {
  return model.credential_source === "service_account";
}

// The providers a model config can name. The server accepts the same values
// (internal/llm); anything else is refused on save.
const MODEL_PROVIDERS: ModelProvider[] = ["gemini", "anthropic", "deepseek"];

// providerLabelKey maps a stored provider to its label. An unknown value reads as Gemini,
// the same reading the server gives it.
function providerLabelKey(provider: string): string {
  if (provider === "anthropic") return "admin.providerClaudeApi";
  if (provider === "deepseek") return "admin.providerDeepSeek";
  return "admin.providerGemini";
}

// The default model id a provider's rows start on when the draft switches to it. Only
// used to fill the form; the server validates the value against the provider catalog.
function defaultModelForProvider(provider: ModelProvider): string {
  if (provider === "anthropic") return "claude-haiku-5-5";
  if (provider === "deepseek") return "deepseek-flash";
  return "";
}

// Launch stages are an API enum, not prose: only the two values an operator has
// to recognise get a translated label, anything else is shown verbatim rather
// than flattened into a vague one.
function modelStageLabelKey(stage: string): string | null {
  const normalized = stage.trim().toUpperCase();
  if (!normalized) return null;
  if (normalized === "GA" || normalized === "GENERALLY_AVAILABLE" || normalized === "STABLE") {
    return "admin.modelStageGa";
  }
  if (normalized.includes("PREVIEW") || normalized.includes("BETA") || normalized.includes("EXPERIMENTAL")) {
    return "admin.modelStagePreview";
  }
  return null;
}

// One labelled group inside a model card. A card carries two credentials, a
// region-dependent model list, three generation knobs and a prompt: grouping
// them (each group with its own divider) is what keeps that readable instead of
// one long column of inputs.
function ModelConfigSection({
  icon: Icon,
  title,
  action,
  children,
}: {
  icon: LucideIcon;
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3 border-t border-border pt-4 first:border-t-0 first:pt-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
          <Icon className="size-4 text-muted-foreground" />
          {title}
        </h3>
        {action}
      </div>
      {children}
    </section>
  );
}

// withDeadline rejects if `promise` has not settled within `ms`, using `message`
// as the rejection reason.
//
// It exists because the region-list request gates the whole page's model loading
// and the shared apiFetch carries no timeout: without a deadline, a request that
// never settles leaves every picker empty and every refresh button disabled,
// with no error surfaced and nothing for the operator to retry. The timer is
// cleared on settle, and the losing promise's own rejection is swallowed so a
// late failure cannot surface as an unhandled rejection.
function withDeadline<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error: unknown) => {
        clearTimeout(timer);
        reject(error instanceof Error ? error : new Error(String(error)));
      },
    );
  });
}

export function ModelsAdminPage() {
  const { t, tf } = useI18n();
  const { data, isLoading, mutate } = useSWR<ModelItem[]>("admin-models", listModelConfigs);
  const models = data ?? [];
  const [modelDrafts, setModelDrafts] = useState<Record<number, ModelItem>>({});
  const [apiKeys, setAPIKeys] = useState<Record<number, string>>({});
  const [testMessages, setTestMessages] = useState<Record<number, string>>({});
  const [testResults, setTestResults] = useState<Record<number, { reply?: string; modelName?: string; promptTokens?: number; outputTokens?: number; error?: string }>>({});
  const [testingConfigID, setTestingConfigID] = useState<number | null>(null);
  const [availableModels, setAvailableModels] = useState<Record<number, AvailableModel[]>>({});
  const [modelListLoading, setModelListLoading] = useState<Record<number, boolean>>({});
  const [modelListErrors, setModelListErrors] = useState<Record<number, string>>({});
  // Non-fatal reason a model list is incomplete (the backend's
  // X-Model-List-Warning header). Deliberately separate from modelListErrors:
  // the list is still usable, so this must not read as a failure — it explains
  // why a region looks smaller than it should.
  const [modelListWarnings, setModelListWarnings] = useState<Record<number, string>>({});
  // Region the operator picked per config. Absent means "follow the region the
  // server is configured with" — the region list itself is shared by every card.
  const [regionByConfig, setRegionByConfig] = useState<Record<number, string>>({});
  // Config whose browsed region is being applied as the SERVING region right
  // now. One id, not a set: the serving region is a deployment-wide setting, so
  // two cards applying at once would race each other's write.
  const [applyingRegionID, setApplyingRegionID] = useState<number | null>(null);
  // The built-in prompt a config falls back to when system_prompt is empty.
  // Without it the field below can only show the STORED value, which is empty
  // on a healthy deployment — an operator then cannot tell whether a prompt is
  // in effect at all, nor what it says.
  const { data: defaultPromptData, error: defaultPromptError } = useSWR("admin-default-prompt", getDefaultSystemPrompt);
  const defaultPrompt = defaultPromptData?.system_prompt ?? "";
  const [showDefaultPrompt, setShowDefaultPrompt] = useState<Record<number, boolean>>({});

  // A region only exists on the service-account (Vertex) transport, so on an AI
  // Studio deployment this request is never made: a region selector there would
  // be a control with nothing behind it.
  const hasVertexModel = models.some(usesServiceAccountCredential);
  const { data: regionsData, error: regionsError, isLoading: regionsLoading } = useSWR(
    hasVertexModel ? "admin-vertex-regions" : null,
    // Bounded on purpose. The whole page's model loading is gated on this
    // request settling (regionsResolved below), and apiFetch carries no timeout,
    // so a request that never settles would leave every picker empty AND the
    // refresh button disabled — with no error and nothing to retry. Racing a
    // deadline turns that silent dead end into the same "region list
    // unavailable" path a 404 already takes, which still loads the models.
    () => withDeadline(listVertexRegions(), 8000, t("admin.regionListUnavailable")),
  );
  const regions = regionsData?.regions ?? [];
  const currentRegion = regionsData?.current ?? "";
  // A Vertex card's model list depends on the region it is loaded for, so the
  // cards wait for the region list rather than loading twice — once for the
  // server default and once for the region the selector ends up showing. A
  // failing endpoint resolves this too: the cards then load exactly as they did
  // before regions existed, with no region parameter at all.
  const regionsResolved = !hasVertexModel || regionsData !== undefined || regionsError !== undefined;

  // The region in force for one card: the operator's pick, else the region the
  // server is configured with, else one a newer backend may report per config.
  // A pick is never empty, hence `||` — `currentRegion` is "" when unknown, and
  // `??` would stop there instead of falling through.
  const regionFor = (model: ModelItem) => {
    if (!usesServiceAccountCredential(model)) return "";
    return regionByConfig[model.config_id] || currentRegion || model.region || "";
  };

  const updateModelDraft = (model: ModelItem, patch: Partial<ModelItem>) => {
    setModelDrafts((previous) => ({
      ...previous,
      [model.config_id]: { ...(previous[model.config_id] ?? model), ...patch },
    }));
  };

  const loadAvailableModels = useCallback(async (model: ModelItem, region?: string): Promise<AvailableModel[] | null> => {
    if (!usesServiceAccountCredential(model) && !model.has_api_key) return null;
    // Sequence stamp per config. Switching regions twice quickly leaves two
    // requests in flight, and without this the slower one wins: the dropdown
    // would show region A's models while the selector, the header and the saved
    // value all say B, and the loading flag would clear when the FIRST request
    // finished rather than the last. Only the newest request may write state.
    const attempt = (listAttempt.current[model.config_id] ?? 0) + 1;
    listAttempt.current[model.config_id] = attempt;
    const isCurrent = () => listAttempt.current[model.config_id] === attempt;
    setModelListLoading((previous) => ({ ...previous, [model.config_id]: true }));
    setModelListErrors((previous) => {
      const next = { ...previous };
      delete next[model.config_id];
      return next;
    });
    setModelListWarnings((previous) => {
      const next = { ...previous };
      delete next[model.config_id];
      return next;
    });
    try {
      // The warning arrives as a header on a 200, not as an error: the request
      // succeeded and the entries below are real, but the region's published
      // list could not be read, so this is the configured/fallback subset rather
      // than that region's catalog. Surfacing it is the difference between
      // "this region has three models" and "we could not list this region" —
      // two states that looked identical while a decode failure was silently
      // degrading every region in production.
      const available = await listAvailableModels(model.config_id, region, (warning) => {
        if (!isCurrent()) return;
        setModelListWarnings((previous) => ({ ...previous, [model.config_id]: warning }));
      });
      if (!isCurrent()) return null;
      setAvailableModels((previous) => ({ ...previous, [model.config_id]: available }));
      return available;
    } catch (error: unknown) {
      // A superseded request's failure is noise: the operator is already looking
      // at a newer region, and reporting the old one's error would attach it to
      // the wrong selector value.
      if (!isCurrent()) return null;
      // Whatever list is on screen stays there: emptying it here would leave an
      // operator who just switched to a region that then fails with a blank
      // dropdown and no way to see which model the config was using.
      setModelListErrors((previous) => ({ ...previous, [model.config_id]: (error as Error).message }));
      return null;
    } finally {
      // Only the newest request may clear the spinner, or the second of two
      // in-flight requests would appear finished while it is still loading.
      if (isCurrent()) {
        setModelListLoading((previous) => ({ ...previous, [model.config_id]: false }));
      }
    }
  }, []);

  const handleRegionChange = (model: ModelItem, region: string) => {
    if (!region || region === regionFor(model)) return;
    setRegionByConfig((previous) => ({ ...previous, [model.config_id]: region }));
    void loadAvailableModels(model, region);
    toast.success(tf("admin.regionChanged", { region }));
  };

  // Apply the browsed region as the SERVING region.
  //
  // Kept as a second, explicit action instead of making the picker above do it:
  // browsing a catalog is free and reversible, while this one moves every
  // customer call (and with it embeddings, context caches and TTS). The backend
  // refuses a region that does not serve the model in use, so the honest failure
  // mod is an error toast, not a silent switch — and `currentRegion` is re-read
  // from the server rather than assumed, so a refused switch never displays as
  // applied.
  const handleApplyRegion = async (model: ModelItem, region: string) => {
    if (!region || region === currentRegion) return;
    setApplyingRegionID(model.config_id);
    try {
      await updateModelConfig(model.config_id, { vertex_region: region });
      await mutate();
      toast.success(tf("admin.regionApplied", { region }));
    } catch (error: unknown) {
      toast.error((error as Error).message);
    } finally {
      setApplyingRegionID(null);
    }
  };

  // Changing the provider changes the form, not the server: the credential fields follow the
  // new provider, and the save enforces the credential rules. The model list is cleared rather
  // than guessed, because it can only come from the server once the row names the new provider.
  const handleProviderChange = (model: ModelItem, provider: ModelProvider) => {
    const current = modelDrafts[model.config_id] ?? model;
    const currentName = current.model_name ?? "";
    // A hosted provider's model ids carry its own prefix; landing on one of them from
    // another provider would leave a name the server refuses at save time.
    const hostPrefix = provider === "gemini" ? "" : provider === "anthropic" ? "claude-" : "deepseek-";
    const patch: Partial<ModelItem> = { provider };
    if (provider !== "gemini" && !currentName.startsWith(hostPrefix)) patch.model_name = defaultModelForProvider(provider);
    if (provider === "gemini" && (currentName.startsWith("claude-") || currentName.startsWith("deepseek-"))) patch.model_name = "";
    updateModelDraft(model, patch);
    setAvailableModels((previous) => {
      const next = { ...previous };
      delete next[model.config_id];
      return next;
    });
  };

  const handleSaveModel = async (model: ModelItem) => {
    const draft = modelDrafts[model.config_id] ?? model;
    const apiKey = apiKeys[model.config_id]?.trim();
    try {
      await updateModelConfig(model.config_id, {
        provider: draft.provider,
        name: draft.name,
        model_name: draft.model_name,
        system_prompt: draft.system_prompt,
        temperature: draft.temperature,
        max_tokens: draft.max_tokens,
        context_cache_ttl: draft.context_cache_ttl,
        is_default: draft.is_default,
        // A key is sent only to a provider that authenticates with one: a Gemini
        // row on Vertex carries the service-account credential instead, and the
        // backend refuses a key there outright (nothing would read it), so sending
        // an empty/leftover value would turn a settings save into a 400. Claude and
        // DeepSeek always authenticate with a key.
        ...(apiKey && (draft.provider !== "gemini" || !usesServiceAccountCredential(model)) ? { api_key: apiKey } : {}),
      });
      setModelDrafts((previous) => {
        const next = { ...previous };
        delete next[model.config_id];
        return next;
      });
      setAPIKeys((previous) => {
        const next = { ...previous };
        delete next[model.config_id];
        return next;
      });
      await mutate();
      // Verify against the region this card is set to, so the toast counts the
      // models the operator can actually choose from.
      const available = await loadAvailableModels(
        { ...model, provider: draft.provider, region: draft.region, has_api_key: Boolean(apiKey) || model.has_api_key },
        regionFor(model) || undefined,
      );
      if (available) {
        toast.success(tf("admin.geminiConnectedToast", { n: available.length }));
      } else if (apiKey || model.has_api_key || usesServiceAccountCredential(model)) {
        toast.error(t("admin.settingsSavedUnconfirmed"));
      } else {
        toast.success(t("admin.modelSaved"));
      }
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  const handleTestModel = async (model: ModelItem) => {
    setTestingConfigID(model.config_id);
    setTestResults((previous) => ({ ...previous, [model.config_id]: {} }));
    try {
      const result = await testModelConfig(model.config_id, testMessages[model.config_id] ?? "");
      setTestResults((previous) => ({
        ...previous,
        [model.config_id]: {
          reply: result.reply,
          modelName: result.model_name,
          promptTokens: result.prompt_tokens,
          outputTokens: result.output_tokens,
        },
      }));
    } catch (error: unknown) {
      const message = (error as Error).message;
      setTestResults((previous) => ({ ...previous, [model.config_id]: { error: message } }));
      toast.error(message);
    } finally {
      setTestingConfigID(null);
    }
  };

  // Model lists load once per fetched snapshot of the config list. The guard is
  // that snapshot: the revalidation after a save hands back a new array, which
  // is exactly when every list should be reloaded — while a region switch (new
  // state, same snapshot) must not reload any other card's list.
  const loadedSnapshot = useRef<ModelItem[] | null>(null);
  // Newest model-list request per config, used to discard a superseded
  // response. A ref (not state) because it must be read and bumped
  // synchronously inside loadAvailableModels, before any await.
  const listAttempt = useRef<Record<number, number>>({});
  useEffect(() => {
    if (!data || !regionsResolved) return;
    if (loadedSnapshot.current === data) return;
    loadedSnapshot.current = data;
    // Under Vertex there is no key to wait for — the credential lives on the
    // server — so the model list has to load for every config, not only the
    // ones with a stored key.
    data.forEach((model) => {
      if (!model.has_api_key && !usesServiceAccountCredential(model)) return;
      const picked = regionByConfig[model.config_id];
      const region = usesServiceAccountCredential(model)
        ? picked || currentRegion || model.region || ""
        : "";
      void loadAvailableModels(model, region || undefined);
    });
  }, [data, loadAvailableModels, regionsResolved, regionByConfig, currentRegion]);

  return (
    <div className="space-y-4 max-w-4xl">
      {/* No page frame: this is a panel inside the platform console now
          (/platform-admin). The refresh action stays where it was, and the
          form-width ceiling the frame used to supply goes on the wrapper. */}
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold text-foreground">{t("admin.modelsTitle")}</h3>
        <RefreshAction onClick={() => void mutate()} refreshing={isLoading} />
      </div>
      {isLoading ? <PageLoadingState /> : models.length === 0 ? (
        <Card><CardContent><EmptyState icon={Zap} title={t("admin.noModels")} /></CardContent></Card>
      ) : (
        <div className="space-y-4">
          {models.map((model) => {
            const draft = modelDrafts[model.config_id] ?? model;
            // Where the prompt in force actually comes from. An empty field is
            // a healthy, meaningful state — it means the built-in default is
            // running — but it looks identical to "nothing configured".
            const promptText = (draft.system_prompt ?? "").trim();
            const promptOverridden = promptText !== "";
            const promptSameAsDefault =
              promptOverridden && defaultPrompt !== "" && promptText === defaultPrompt.trim();
            const testResult = testResults[model.config_id];
            const isTesting = testingConfigID === model.config_id;
            const modelOptions = availableModels[model.config_id] ?? [];
            const selectedOption = modelOptions.find((option) => option.name === draft.model_name);
            const hasCurrentModel = selectedOption !== undefined;
            // The configured model stays in the dropdown even when the backend
            // does not list it for this region — otherwise the control would
            // show some other model than the one actually running.
            const selectOptions: AvailableModel[] = hasCurrentModel || !draft.model_name
              ? modelOptions
              : [{ name: draft.model_name, display_name: `${draft.model_name}${t("admin.currentSuffix")}` }, ...modelOptions];
            const isLoadingModels = modelListLoading[model.config_id] ?? false;
            const modelListError = modelListErrors[model.config_id];
            const modelListWarning = modelListWarnings[model.config_id];
            // Two credentials, two places: the service-account file on the
            // server (Vertex) or the key stored in this config (AI Studio). The
            // difference decides whether "no stored key" means "needs setup" or
            // "nothing to do here".
            // Which form and which credential the card shows follows the provider the draft
            // names, so changing the provider changes the form before the save.
            const draftProvider = draft.provider;
            const isGemini = draftProvider === "gemini";
            const claudeDirect = draftProvider === "anthropic";
            const deepseekDirect = draftProvider === "deepseek";
            // The service-account credential is a Vertex fact, so it can only apply while
            // the draft still names Gemini: on the saved row's field a switch to a hosted
            // provider would otherwise keep hiding the key input.
            const serviceAccount = isGemini && usesServiceAccountCredential(model);
            // Provider-named status strings follow the draft too, so the card does not
            // promise a Claude connection while the form says DeepSeek.
            const checkingKey = claudeDirect ? "admin.checkingClaude" : deepseekDirect ? "admin.checkingDeepSeek" : "admin.checkingGemini";
            const connectedKey = claudeDirect ? "admin.connectedClaude" : deepseekDirect ? "admin.connectedDeepSeek" : "admin.connectedGemini";
            const needsVerifyKey = claudeDirect ? "admin.claudeNeedsVerify" : deepseekDirect ? "admin.deepseekNeedsVerify" : "admin.geminiNeedsVerify";
            const credentialReady = serviceAccount || model.has_api_key;
            const isConnected = credentialReady && modelOptions.length > 0 && !modelListError;
            // A Vertex card's list is not even requested until the region list
            // has answered, and that wait has to read as loading too: otherwise
            // the dropdown would sit empty with no explanation for one
            // round-trip, looking exactly like a failing connection.
            const modelsPending = isLoadingModels || (serviceAccount && !regionsResolved);
            const region = regionFor(model);
            const regionLabel = region
              ? regions.find((entry) => entry.id === region)?.label || region
              : t("admin.regionUnknown");
            const selectedStage = selectedOption?.launch_stage?.trim() ?? "";
            const selectedStageKey = selectedStage ? modelStageLabelKey(selectedStage) : null;
            // Exactly false: an older backend omits the field, and "unknown"
            // must never be rendered as "unusable".
            const selectedUnavailable = selectedOption?.available === false;
            // An older backend reports neither capability nor availability, and
            // then every listed model counts — the count this hint showed before.
            const chatModels = modelOptions.filter(
              (option) => option.available !== false && (!option.capability || option.capability === "chat"),
            );
            return (
              <Card key={model.config_id}>
                <CardHeader className="border-b border-border">
                  <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
                    {model.name}
                    {model.is_default && <Badge variant="success" className="h-4 px-1.5 text-xs">{t("admin.defaultBadge")}</Badge>}
                  </CardTitle>
                  <CardDescription className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                    <span className="font-medium text-foreground">{t(providerLabelKey(draft.provider))}</span>
                    <span aria-hidden className="text-muted-foreground">·</span>
                    <span className="font-mono text-muted-foreground">{draft.model_name || t("admin.modelNotSet")}</span>
                    {serviceAccount && (
                      <>
                        <span aria-hidden className="text-muted-foreground">·</span>
                        <span className="text-muted-foreground">{regionLabel}</span>
                      </>
                    )}
                  </CardDescription>
                  <CardAction>
                    <Badge variant={isConnected ? "success" : "secondary"} className="h-5 text-xs">
                      {isConnected
                        ? t("admin.connected")
                        : serviceAccount
                          ? t("admin.saCredential")
                          : model.has_api_key
                            ? t("admin.verificationNeeded")
                            : t("admin.apiKeyRequired")}
                    </Badge>
                  </CardAction>
                </CardHeader>
                <CardContent className="space-y-4">
                  <ModelConfigSection icon={KeyRound} title={t("admin.connSettings")}>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.provider")}</label>
                        <Select value={draftProvider} onValueChange={(value) => { if (value) handleProviderChange(model, String(value) as ModelProvider); }}>
                          <SelectTrigger className="h-8 w-full text-xs">
                            <SelectValue>{() => t(providerLabelKey(draftProvider))}</SelectValue>
                          </SelectTrigger>
                          <SelectContent>
                            {MODEL_PROVIDERS.map((option) => (
                              <SelectItem key={option} value={option} label={t(providerLabelKey(option))}>
                                {t(providerLabelKey(option))}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <p className="mt-1 text-xs text-muted-foreground">{t("admin.providerScopeNote")}</p>
                        {draft.provider !== model.provider && (
                          <p className="mt-1 flex items-start gap-1.5 text-xs text-warning"><AlertTriangle className="mt-0.5 size-3.5 shrink-0" />{t("admin.providerSwitchNote")}</p>
                        )}
                      </div>
                      <div className={cn(serviceAccount && "sm:col-span-2")}>
                        <label className="mb-1 block text-xs text-muted-foreground">
                          {claudeDirect ? t("admin.claudeApiKey") : deepseekDirect ? t("admin.deepseekApiKey") : serviceAccount ? t("admin.geminiCredential") : t("admin.geminiApiKey")}
                        </label>
                        {serviceAccount ? (
                          // No input at all under Vertex. An API-key box here is
                          // not a harmless extra: it invites an operator to paste
                          // a key the backend refuses to store (nothing reads that
                          // column on this transport), which reads as "the
                          // credential was rotated" when nothing changed.
                          <p className="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs leading-relaxed text-muted-foreground">
                            {t("admin.saCredentialHint")}
                          </p>
                        ) : (
                          <>
                            <Input
                              type="password"
                              autoComplete="off"
                              value={apiKeys[model.config_id] ?? ""}
                              onChange={(event) => setAPIKeys((previous) => ({ ...previous, [model.config_id]: event.target.value }))}
                              placeholder={model.has_api_key ? t("admin.keySavedPh") : t("admin.pasteKeyPh")}
                              className="h-8 text-xs"
                            />
                            <p className="mt-1 text-xs text-muted-foreground">{t("admin.keyNeverShown")}</p>
                          </>
                        )}
                      </div>
                    </div>
                  </ModelConfigSection>

                  <ModelConfigSection icon={Cpu} title={t("admin.sectionModelRegion")}>
                    <div className={cn("grid gap-3", serviceAccount && "lg:grid-cols-2")}>
                      {serviceAccount && (
                        <div>
                          <label className="mb-1 block text-xs text-muted-foreground">{t("admin.region")}</label>
                          {regions.length > 0 ? (
                            <Select
                              value={region}
                              onValueChange={(value) => { if (value) handleRegionChange(model, String(value)); }}
                            >
                              <SelectTrigger className="h-8 w-full text-xs">
                                {/* Explicit label: the options carry badges, which
                                    must not leak into the trigger. */}
                                <SelectValue>{() => regionLabel}</SelectValue>
                              </SelectTrigger>
                              <SelectContent>
                                {regions.map((entry) => (
                                  <SelectItem key={entry.id} value={entry.id} label={entry.label || entry.id}>
                                    {entry.label || (entry.id === "global" ? t("admin.regionGlobal") : entry.id)}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Input value={regionLabel} disabled className="h-8 text-xs" />
                          )}
                          <p className="mt-1 text-xs text-muted-foreground">
                            {regionsLoading
                              ? t("admin.regionLoading")
                              : regionsError
                                ? t("admin.regionListUnavailable")
                                // Names the region this config actually serves from.
                                // Without it the picker above reads as "where this
                                // runs", which is how picking global + a global-only
                                // model ended up saving a model the serving region
                                // 404s — the failure the save guard now refuses.
                                : tf("admin.regionHint", { servingRegion: currentRegion || t("admin.regionUnknown") })}
                          </p>
                          {/* Offered only when the pick above differs from what is
                              serving: a button that says "switch" while already
                              there teaches the operator to ignore it. */}
                          {region && currentRegion && region !== currentRegion && (
                            <Button
                              type="button"
                              variant="secondary"
                              size="sm"
                              className="mt-2 h-7 text-xs"
                              disabled={applyingRegionID !== null}
                              onClick={() => void handleApplyRegion(model, region)}
                            >
                              {applyingRegionID === model.config_id
                                ? t("admin.regionApplying")
                                : tf("admin.regionApply", { region })}
                            </Button>
                          )}
                        </div>
                      )}
                      <div>
                        <div className="mb-1 flex flex-wrap items-center gap-2">
                          <label className="block text-xs text-muted-foreground">{t("admin.modelIdentifier")}</label>
                          {selectedStage && (
                            <Badge
                              variant={selectedStageKey === "admin.modelStageGa" ? "outline" : "info"}
                              className="h-4 px-1.5 text-[10px]"
                            >
                              {selectedStageKey ? t(selectedStageKey) : selectedStage}
                            </Badge>
                          )}
                        </div>
                        <div className="flex gap-2">
                          {selectOptions.length > 0 ? (
                            <Select value={draft.model_name} onValueChange={(value) => { if (value) updateModelDraft(model, { model_name: value }); }}>
                              <SelectTrigger className="h-8 min-w-0 flex-1 text-xs">
                                {/* Label resolved from the option list rather
                                    than from the item registry, so it reads
                                    cleanly without the stage/availability
                                    badges the items carry. */}
                                <SelectValue>{() => selectedOption?.display_name || draft.model_name || t("admin.modelNotSet")}</SelectValue>
                              </SelectTrigger>
                              <SelectContent className="min-w-72">
                                {selectOptions.map((option) => {
                                  const optionStage = option.launch_stage?.trim() ?? "";
                                  const optionStageKey = optionStage ? modelStageLabelKey(optionStage) : null;
                                  return (
                                    <SelectItem key={option.name} value={option.name} label={option.display_name || option.name}>
                                      <span className="flex w-full min-w-0 items-center gap-2">
                                        <span className="min-w-0 flex-1 truncate">{option.display_name || option.name}</span>
                                        {optionStage && (
                                          <Badge
                                            variant={optionStageKey === "admin.modelStageGa" ? "outline" : "info"}
                                            className="h-4 shrink-0 px-1.5 text-[10px]"
                                          >
                                            {optionStageKey ? t(optionStageKey) : optionStage}
                                          </Badge>
                                        )}
                                        {/* Flagged, not disabled: the operator
                                            may still pick it and test it. */}
                                        {option.available === false && (
                                          <Badge variant="warning" className="h-4 shrink-0 px-1.5 text-[10px]">
                                            {t("admin.modelUnavailableHere")}
                                          </Badge>
                                        )}
                                      </span>
                                    </SelectItem>
                                  );
                                })}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Input value={draft.model_name} onChange={(event) => updateModelDraft(model, { model_name: event.target.value })} className="h-8 text-xs" />
                          )}
                          <Button size="icon-sm" variant="outline" disabled={modelsPending} onClick={() => {
                            if (!credentialReady) {
                              setModelListErrors((previous) => ({ ...previous, [model.config_id]: t("admin.saveKeyFirst") }));
                              return;
                            }
                            void loadAvailableModels(model, region || undefined);
                          }} aria-label={t("admin.refreshModelsAria")}>
                            <RefreshCw className={cn(modelsPending && "animate-spin")} />
                          </Button>
                        </div>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {modelsPending
                            ? region
                              ? tf("admin.loadingModelsForRegion", { region })
                              : t(checkingKey)
                            : isConnected
                              ? region
                                ? tf(isGemini ? "admin.connectedGeminiRegion" : connectedKey, { n: chatModels.length, region })
                                : tf(connectedKey, { n: chatModels.length })
                              : credentialReady
                                ? t(needsVerifyKey)
                                : t("admin.saveKeyToLoad")}
                        </p>
                        {selectedUnavailable && (
                          <p className="mt-1 flex items-center gap-1.5 text-xs text-warning">
                            <AlertTriangle className="size-3.5 shrink-0" />
                            {t("admin.modelUnavailableHere")}
                          </p>
                        )}
                        {modelListError && <p role="alert" className="mt-1 text-xs text-danger">{modelListError}</p>}
                        {/* Non-fatal: the list below is real and selectable, but
                            the region's published catalog could not be read, so
                            it is the configured/fallback subset. Warning tone,
                            not error tone — and it names the region so the
                            operator can switch away from a broken one. */}
                        {modelListWarning && (
                          <p role="status" className="mt-1 flex items-start gap-1.5 text-xs text-warning">
                            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                            <span>{tf("admin.modelListIncomplete", { region: region || t("admin.regionUnknown"), reason: modelListWarning })}</span>
                          </p>
                        )}
                      </div>
                    </div>
                  </ModelConfigSection>

                  <ModelConfigSection icon={SlidersHorizontal} title={t("admin.sectionGeneration")}>
                    <div className="grid gap-3 sm:grid-cols-3">
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.temperature")}</label>
                        <Input type="number" step="0.1" min="0" max="2" value={draft.temperature} onChange={(event) => updateModelDraft(model, { temperature: Number(event.target.value) })} className="h-8 text-xs" />
                        {claudeDirect && <p className="mt-1 text-xs text-muted-foreground">{t("admin.claudeSamplingNote")}</p>}
                      </div>
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.maxTokens")}</label>
                        <Input type="number" min="1" value={draft.max_tokens} onChange={(event) => updateModelDraft(model, { max_tokens: Number(event.target.value) })} className="h-8 text-xs" />
                      </div>
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{isGemini ? t("admin.cacheTtl") : t("admin.cacheTtlGeminiOnly")}</label>
                        <Input type="number" min="0" value={draft.context_cache_ttl} onChange={(event) => updateModelDraft(model, { context_cache_ttl: Number(event.target.value) })} className="h-8 text-xs" />
                      </div>
                    </div>
                  </ModelConfigSection>

                  <ModelConfigSection
                    icon={Sparkles}
                    title={t("admin.systemPrompt")}
                    action={
                      <Badge
                        variant="outline"
                        className={
                          promptSameAsDefault
                            ? "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
                            : promptOverridden
                              ? "border-border bg-muted text-foreground"
                              : "border-border bg-muted text-muted-foreground"
                        }
                      >
                        {promptSameAsDefault
                          ? t("admin.promptSameAsDefault")
                          : promptOverridden
                            ? t("admin.promptOverridden")
                            : t("admin.promptUsingDefault")}
                      </Badge>
                    }
                  >
                    <Textarea
                      value={draft.system_prompt}
                      onChange={(event) => updateModelDraft(model, { system_prompt: event.target.value })}
                      rows={showDefaultPrompt[model.config_id] ? 8 : 3}
                      className="resize-none text-xs"
                      placeholder={t("admin.promptEmptyPh")}
                    />
                    <p className={`text-xs ${promptSameAsDefault ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground"}`}>
                      {defaultPromptError
                        ? t("admin.promptDefaultUnavailable")
                        : promptSameAsDefault
                          ? t("admin.promptHintSame")
                          : promptOverridden
                            ? t("admin.promptHintOverridden")
                            : t("admin.promptHintDefault")}
                    </p>
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-7 text-xs"
                        onClick={() => setShowDefaultPrompt((previous) => ({ ...previous, [model.config_id]: !previous[model.config_id] }))}
                      >
                        {showDefaultPrompt[model.config_id] ? t("admin.promptHideDefault") : t("admin.promptShowDefault")}
                      </Button>
                      {promptOverridden && (
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs"
                          onClick={() => updateModelDraft(model, { system_prompt: "" })}
                        >
                          {t("admin.promptResetDefault")}
                        </Button>
                      )}
                    </div>
                    {showDefaultPrompt[model.config_id] && (
                      <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-muted/40 p-3 text-xs leading-relaxed text-muted-foreground">
                        {defaultPrompt || t("admin.promptLoading")}
                      </pre>
                    )}
                    <PromptHistory
                      configId={model.config_id}
                      onRestored={() => {
                        // The server changed system_prompt; drop the local draft
                        // so the textarea shows the restored value, and refresh
                        // the list so has_api_key / other fields stay truthful.
                        setModelDrafts((previous) => {
                          const next = { ...previous };
                          delete next[model.config_id];
                          return next;
                        });
                        void mutate();
                      }}
                    />
                  </ModelConfigSection>

                  <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border pt-4">
                    <Button size="sm" onClick={() => handleSaveModel(model)} className="h-8 gap-1.5 text-xs">
                      <ShieldCheck className="size-3.5" />
                      {t("admin.saveVerify")}
                    </Button>
                  </div>

                  <ModelConfigSection
                    icon={MessageSquare}
                    title={t("admin.testChat")}
                    action={<span className="text-xs text-muted-foreground">{t("admin.testNotSaved")}</span>}
                  >
                    <Textarea
                      value={testMessages[model.config_id] ?? ""}
                      onChange={(event) => setTestMessages((previous) => ({ ...previous, [model.config_id]: event.target.value }))}
                      placeholder={t("admin.testPh")}
                      rows={2}
                      className="min-h-20 resize-none text-sm"
                    />
                    <div className="flex flex-wrap items-center gap-2">
                      <Button size="sm" variant="outline" disabled={!credentialReady || isTesting} onClick={() => void handleTestModel(model)}>
                        <Send data-icon="inline-start" />
                        {isTesting ? t("admin.testing") : t("admin.sendTest")}
                      </Button>
                      {!credentialReady && <span className="text-xs text-muted-foreground">{t("admin.saveKeyToTest")}</span>}
                    </div>
                    {testResult?.error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-danger">{testResult.error}</p>}
                    {testResult?.reply && (
                      <div aria-live="polite" className="rounded-md border border-border bg-muted/40 p-3">
                        <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                          <span>{testResult.modelName}</span>
                          <span>{tf("admin.tokenInOut", { in: testResult.promptTokens ?? 0, out: testResult.outputTokens ?? 0 })}</span>
                        </div>
                        <p className="whitespace-pre-wrap text-sm leading-6 text-foreground">{testResult.reply}</p>
                      </div>
                    )}
                  </ModelConfigSection>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}
