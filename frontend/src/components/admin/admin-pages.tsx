"use client";

import { useCallback, useEffect, useState } from "react";
import useSWR from "swr";
import { listUsers, updateUserRole, getTokenStats, listModelConfigs, listAvailableModels, testModelConfig, updateModelConfig, getDefaultSystemPrompt, type AvailableModel, type ModelItem, type PaginatedResponse, type UserItem, type UsersStats } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import { BarChart3, Clock, KeyRound, MessageSquare, RefreshCw, Send, Sparkles, TrendingUp, Users, Zap, Download, ShieldCheck, Search, UserCheck, UserPlus, Coins, Lock, type LucideIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { toast } from "sonner";

import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { fmtDate, fmtInt, fmtMoney } from "@/lib/format";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { AnalyticsPanel } from "@/components/admin/analytics-panel";
import { FeedbackTab } from "@/components/admin/feedback-tab";
import { OperationsTab } from "@/components/admin/operations-tab";
import { GrowthTab } from "@/components/admin/growth-tab";
import { ReportsTab } from "@/components/admin/reports-tab";
import { EnterpriseTab } from "@/components/admin/enterprise-tab";

type UsersResponse = PaginatedResponse<UserItem> & { stats?: UsersStats };

interface TokenStats {
  daily_usage: { date: string; tokens: number; cost: number }[];
}

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

function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}k`;
  return String(tokens);
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

export function UsersAdminPage() {
  const { t, tf } = useI18n();
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
  const maxTokens = users.reduce((max, u) => Math.max(max, u.total_tokens ?? 0), 0);

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
                  <TableHead className="text-xs">{t("admin.colUsage")}</TableHead>
                  <TableHead className="text-xs">{t("admin.colRegistered")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((user) => {
                  const tokens = user.total_tokens ?? 0;
                  const usagePct = maxTokens > 0 ? Math.round((tokens / maxTokens) * 100) : 0;
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
                        <div className="flex items-center gap-2">
                          <div className="h-1.5 w-20 overflow-hidden rounded-full bg-muted">
                            <div className="h-full rounded-full bg-primary" style={{ width: `${usagePct}%` }} />
                          </div>
                          <span className="text-[11px] font-medium tabular-nums text-foreground" title={tf("admin.tokenCost", { cost: (user.cost_estimate ?? 0).toFixed(4) })}>
                            {formatTokenCount(tokens)}
                          </span>
                        </div>
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
    </AdminPageFrame>
  );
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
  // The built-in prompt a config falls back to when system_prompt is empty.
  // Without it the field below can only show the STORED value, which is empty
  // on a healthy deployment — an operator then cannot tell whether a prompt is
  // in effect at all, nor what it says.
  const { data: defaultPromptData, error: defaultPromptError } = useSWR("admin-default-prompt", getDefaultSystemPrompt);
  const defaultPrompt = defaultPromptData?.system_prompt ?? "";
  const [showDefaultPrompt, setShowDefaultPrompt] = useState<Record<number, boolean>>({});

  const updateModelDraft = (model: ModelItem, patch: Partial<ModelItem>) => {
    setModelDrafts((previous) => ({
      ...previous,
      [model.config_id]: { ...(previous[model.config_id] ?? model), ...patch },
    }));
  };

  const loadAvailableModels = useCallback(async (model: ModelItem): Promise<AvailableModel[] | null> => {
    if (!model.has_api_key) return null;
    setModelListLoading((previous) => ({ ...previous, [model.config_id]: true }));
    setAvailableModels((previous) => ({ ...previous, [model.config_id]: [] }));
    setModelListErrors((previous) => {
      const next = { ...previous };
      delete next[model.config_id];
      return next;
    });
    try {
      const available = await listAvailableModels(model.config_id);
      setAvailableModels((previous) => ({ ...previous, [model.config_id]: available }));
      return available;
    } catch (error: unknown) {
      setModelListErrors((previous) => ({ ...previous, [model.config_id]: (error as Error).message }));
      return null;
    } finally {
      setModelListLoading((previous) => ({ ...previous, [model.config_id]: false }));
    }
  }, []);

  const handleSaveModel = async (model: ModelItem) => {
    const draft = modelDrafts[model.config_id] ?? model;
    const apiKey = apiKeys[model.config_id]?.trim();
    try {
      await updateModelConfig(model.config_id, {
        name: draft.name,
        model_name: draft.model_name,
        system_prompt: draft.system_prompt,
        temperature: draft.temperature,
        max_tokens: draft.max_tokens,
        context_cache_ttl: draft.context_cache_ttl,
        is_default: draft.is_default,
        ...(apiKey ? { api_key: apiKey } : {}),
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
      const available = await loadAvailableModels({ ...model, has_api_key: Boolean(apiKey) || model.has_api_key });
      if (available) {
        toast.success(tf("admin.geminiConnectedToast", { n: available.length }));
      } else if (apiKey || model.has_api_key) {
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

  useEffect(() => {
    if (!data) return;
    data.filter((model) => model.has_api_key).forEach((model) => {
      void loadAvailableModels(model);
    });
  }, [data, loadAvailableModels]);

  return (
    <AdminPageFrame
      icon={Zap}
      title={t("admin.modelsTitle")}
      maxWidth="max-w-4xl"
      actions={<RefreshAction onClick={() => void mutate()} refreshing={isLoading} />}
    >
      {isLoading ? <PageLoadingState /> : models.length === 0 ? (
        <Card><CardContent><EmptyState icon={Zap} title={t("admin.noModels")} /></CardContent></Card>
      ) : (
        <div className="space-y-3">
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
            const hasCurrentModel = modelOptions.some((option) => option.name === draft.model_name);
            const selectOptions = hasCurrentModel || !draft.model_name
              ? modelOptions
              : [{ name: draft.model_name, display_name: `${draft.model_name}${t("admin.currentSuffix")}` }, ...modelOptions];
            const isLoadingModels = modelListLoading[model.config_id] ?? false;
            const modelListError = modelListErrors[model.config_id];
            const isConnected = model.has_api_key && modelOptions.length > 0 && !modelListError;
            return (
              <Card key={model.config_id}>
                <CardHeader className="pb-2">
                  <div className="flex items-center justify-between">
                    <CardTitle className="flex items-center gap-2 text-sm">
                      {model.name}
                      {model.is_default && <Badge variant="success" className="h-4 px-1.5 text-xs">{t("admin.defaultBadge")}</Badge>}
                    </CardTitle>
                    <Badge variant="secondary" className="h-5 text-xs">{model.provider} / {model.model_name}</Badge>
                  </div>
                </CardHeader>
                <CardContent className="space-y-3">
                  <div className="rounded-lg border border-border bg-muted/30 p-3">
                    <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2 text-sm font-medium">
                        <KeyRound className="size-4 text-muted-foreground" />
                        {t("admin.connSettings")}
                      </div>
                      <Badge variant={isConnected ? "success" : "secondary"} className="h-5 text-xs">
                        {isConnected ? t("admin.connected") : model.has_api_key ? t("admin.verificationNeeded") : t("admin.apiKeyRequired")}
                      </Badge>
                    </div>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.provider")}</label>
                        <Input value="Gemini API" disabled className="h-8 text-xs" />
                      </div>
                      <div>
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.modelIdentifier")}</label>
                        <div className="flex gap-2">
                          {selectOptions.length > 0 ? (
                            <Select value={draft.model_name} onValueChange={(value) => { if (value) updateModelDraft(model, { model_name: value }); }}>
                              <SelectTrigger className="h-8 flex-1 text-xs"><SelectValue /></SelectTrigger>
                              <SelectContent>
                                {selectOptions.map((option) => <SelectItem key={option.name} value={option.name}>{option.display_name || option.name}</SelectItem>)}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Input value={draft.model_name} onChange={(event) => updateModelDraft(model, { model_name: event.target.value })} className="h-8 text-xs" />
                          )}
                          <Button size="icon-sm" variant="outline" disabled={isLoadingModels} onClick={() => {
                            if (!model.has_api_key) {
                              setModelListErrors((previous) => ({ ...previous, [model.config_id]: t("admin.saveKeyFirst") }));
                              return;
                            }
                            void loadAvailableModels(model);
                          }} aria-label={t("admin.refreshModelsAria")}>
                            <RefreshCw className={cn(isLoadingModels && "animate-spin")} />
                          </Button>
                        </div>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {isLoadingModels ? t("admin.checkingGemini") : isConnected ? tf("admin.connectedGemini", { n: modelOptions.length }) : model.has_api_key ? t("admin.geminiNeedsVerify") : t("admin.saveKeyToLoad")}
                        </p>
                        {modelListError && <p role="alert" className="mt-1 text-xs text-danger">{modelListError}</p>}
                      </div>
                      <div className="sm:col-span-2">
                        <label className="mb-1 block text-xs text-muted-foreground">{t("admin.geminiApiKey")}</label>
                        <Input
                          type="password"
                          autoComplete="off"
                          value={apiKeys[model.config_id] ?? ""}
                          onChange={(event) => setAPIKeys((previous) => ({ ...previous, [model.config_id]: event.target.value }))}
                          placeholder={model.has_api_key ? t("admin.keySavedPh") : t("admin.pasteKeyPh")}
                          className="h-8 text-xs"
                        />
                        <p className="mt-1 text-xs text-muted-foreground">{t("admin.keyNeverShown")}</p>
                      </div>
                    </div>
                  </div>
                  <div>
                    <div className="mb-1 flex flex-wrap items-center gap-2">
                      <label className="block text-xs text-muted-foreground">{t("admin.systemPrompt")}</label>
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
                    </div>
                    <Textarea
                      value={draft.system_prompt}
                      onChange={(event) => updateModelDraft(model, { system_prompt: event.target.value })}
                      rows={showDefaultPrompt[model.config_id] ? 8 : 3}
                      className="resize-none text-xs"
                      placeholder={t("admin.promptEmptyPh")}
                    />
                    <p className={`mt-1 text-xs ${promptSameAsDefault ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground"}`}>
                      {defaultPromptError
                        ? t("admin.promptDefaultUnavailable")
                        : promptSameAsDefault
                          ? t("admin.promptHintSame")
                          : promptOverridden
                            ? t("admin.promptHintOverridden")
                            : t("admin.promptHintDefault")}
                    </p>
                    <div className="mt-2 flex flex-wrap items-center gap-2">
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
                      <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-muted/40 p-3 text-xs leading-relaxed text-muted-foreground">
                        {defaultPrompt || t("admin.promptLoading")}
                      </pre>
                    )}
                  </div>
                  <div className="flex gap-3">
                    <div className="flex-1">
                      <label className="mb-1 block text-xs text-muted-foreground">{t("admin.temperature")}</label>
                      <Input type="number" step="0.1" min="0" max="2" value={draft.temperature} onChange={(event) => updateModelDraft(model, { temperature: Number(event.target.value) })} className="h-8 text-xs" />
                    </div>
                    <div className="flex-1">
                      <label className="mb-1 block text-xs text-muted-foreground">{t("admin.maxTokens")}</label>
                      <Input type="number" min="1" value={draft.max_tokens} onChange={(event) => updateModelDraft(model, { max_tokens: Number(event.target.value) })} className="h-8 text-xs" />
                    </div>
                    <div className="flex-1">
                      <label className="mb-1 block text-xs text-muted-foreground">{t("admin.cacheTtl")}</label>
                      <Input type="number" min="0" value={draft.context_cache_ttl} onChange={(event) => updateModelDraft(model, { context_cache_ttl: Number(event.target.value) })} className="h-8 text-xs" />
                    </div>
                  </div>
                  <Button size="sm" onClick={() => handleSaveModel(model)} className="h-8 text-xs">{t("admin.saveVerify")}</Button>
                  <div className="rounded-lg border border-border p-3">
                    <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2 text-sm font-medium">
                        <MessageSquare className="size-4 text-muted-foreground" />
                        {t("admin.testChat")}
                      </div>
                      <span className="text-xs text-muted-foreground">{t("admin.testNotSaved")}</span>
                    </div>
                    <Textarea
                      value={testMessages[model.config_id] ?? ""}
                      onChange={(event) => setTestMessages((previous) => ({ ...previous, [model.config_id]: event.target.value }))}
                      placeholder={t("admin.testPh")}
                      rows={2}
                      className="min-h-20 resize-none text-sm"
                    />
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                      <Button size="sm" variant="outline" disabled={!model.has_api_key || isTesting} onClick={() => void handleTestModel(model)}>
                        <Send data-icon="inline-start" />
                        {isTesting ? t("admin.testing") : t("admin.sendTest")}
                      </Button>
                      {!model.has_api_key && <span className="text-xs text-muted-foreground">{t("admin.saveKeyToTest")}</span>}
                    </div>
                    {testResult?.error && <p role="alert" className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-danger">{testResult.error}</p>}
                    {testResult?.reply && (
                      <div aria-live="polite" className="mt-3 rounded-md border border-border bg-muted/40 p-3">
                        <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                          <span>{testResult.modelName}</span>
                          <span>{tf("admin.tokenInOut", { in: testResult.promptTokens ?? 0, out: testResult.outputTokens ?? 0 })}</span>
                        </div>
                        <p className="whitespace-pre-wrap text-sm leading-6 text-foreground">{testResult.reply}</p>
                      </div>
                    )}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </AdminPageFrame>
  );
}

export function TokensAdminPage() {
  const { t } = useI18n();
  const { data, isLoading, mutate } = useSWR<TokenStats>("admin-token-stats", () => getTokenStats(30));
  const usage = data?.daily_usage ?? [];

  return (
    <AdminPageFrame
      icon={TrendingUp}
      title={t("admin.tokensTitle")}
      actions={<RefreshAction onClick={() => void mutate()} refreshing={isLoading} />}
    >
      {isLoading ? <PageLoadingState /> : (
        <Card>
          <CardHeader className="pb-3"><CardTitle className="text-sm">{t("admin.dailyTokens")}</CardTitle></CardHeader>
          <CardContent>
            {usage.length === 0 ? (
              <EmptyState icon={TrendingUp} title={t("an.noUsage")} />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="text-xs">{t("admin.colDate")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colTokens")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colCost")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {usage.map((day) => (
                    <TableRow key={day.date}>
                      <TableCell className="text-xs">{day.date}</TableCell>
                      <TableCell className="text-xs font-medium tabular-nums">{fmtInt(day.tokens)}</TableCell>
                      <TableCell className="text-xs text-success tabular-nums">{fmtMoney(day.cost, 4)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}
    </AdminPageFrame>
  );
}
