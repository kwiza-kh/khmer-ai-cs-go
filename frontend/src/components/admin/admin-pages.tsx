"use client";

import { useCallback, useEffect, useState } from "react";
import useSWR from "swr";
import { listUsers, updateUserRole, getTokenStats, listModelConfigs, listAvailableModels, testModelConfig, updateModelConfig, type AvailableModel, type ModelItem } from "@/lib/api";
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
import { BarChart3, Clock, KeyRound, MessageSquare, RefreshCw, Send, Sparkles, TrendingUp, Users, Zap, Download, ShieldCheck, type LucideIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { toast } from "sonner";

import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { AnalyticsPanel } from "@/components/admin/analytics-panel";
import { FeedbackTab } from "@/components/admin/feedback-tab";
import { OperationsTab } from "@/components/admin/operations-tab";
import { GrowthTab } from "@/components/admin/growth-tab";
import { ReportsTab } from "@/components/admin/reports-tab";
import { EnterpriseTab } from "@/components/admin/enterprise-tab";

interface UserItem {
  user_id: number;
  username: string;
  email: string;
  role: string;
  is_active: boolean;
  auth_method?: string;
}

interface UsersResponse {
  data: UserItem[];
  total: number;
}

interface TokenStats {
  daily_usage: { date: string; tokens: number; cost: number }[];
}

function AdminPageFrame({
  icon,
  title,
  children,
  actions,
}: {
  icon: LucideIcon;
  title: string;
  children: React.ReactNode;
  actions?: React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <div className="flex h-full flex-col">
      <PageHeader icon={icon} kicker={t("nav.administration")} title={title} actions={actions} />
      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="mx-auto max-w-6xl">{children}</div>
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

export function UsersAdminPage() {
  const { t, tf } = useI18n();
  const { data, isLoading, mutate } = useSWR<UsersResponse>("admin-users", () => listUsers(1, 100));
  const users = data?.data ?? [];
  const userTotal = data?.total ?? 0;

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
      await updateUserRole(userId, "", isActive);
      await mutate();
    } catch (error: unknown) {
      toast.error((error as Error).message);
    }
  };

  return (
    <AdminPageFrame
      icon={Users}
      title={t("admin.usersTitle")}
      actions={<RefreshAction onClick={() => void mutate()} refreshing={isLoading} />}
    >
      {isLoading ? <PageLoadingState /> : (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm">{tf("admin.registeredUsers", { n: userTotal })}</CardTitle>
          </CardHeader>
          <CardContent>
            {users.length === 0 ? (
              <EmptyState icon={Users} title={t("admin.noUsers")} />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-12 text-xs">{t("admin.colId")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colUsername")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colEmail")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colRole")}</TableHead>
                    <TableHead className="text-xs">{t("admin.colStatus")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {users.map((user) => (
                    <TableRow key={user.user_id}>
                      <TableCell className="text-xs text-muted-foreground">{user.user_id}</TableCell>
                      <TableCell className="text-xs font-medium">
                        <span className="inline-flex items-center gap-1.5">
                          {user.username}
                          {user.auth_method === "google" && (
                            <Badge variant="secondary" className="h-4 px-1.5 text-[10px] font-normal">
                              Google
                            </Badge>
                          )}
                        </span>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{user.email}</TableCell>
                      <TableCell>
                        <Select value={user.role} onValueChange={(value) => handleUpdateRole(user.user_id, value || "user")}>
                          <SelectTrigger className="h-7 w-20 text-xs"><SelectValue /></SelectTrigger>
                          <SelectContent>
                            <SelectItem value="user">{t("admin.roleUser")}</SelectItem>
                            <SelectItem value="admin">{t("admin.roleAdmin")}</SelectItem>
                          </SelectContent>
                        </Select>
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant={user.is_active ? "success" : "destructive"}
                          onClick={() => handleToggleActive(user.user_id, !user.is_active)}
                          className="h-5 cursor-pointer px-2 text-xs"
                        >
                          {user.is_active ? t("admin.statusActive") : t("admin.statusDisabled")}
                        </Badge>
                      </TableCell>
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
      actions={<RefreshAction onClick={() => void mutate()} refreshing={isLoading} />}
    >
      {isLoading ? <PageLoadingState /> : models.length === 0 ? (
        <Card><CardContent><EmptyState icon={Zap} title={t("admin.noModels")} /></CardContent></Card>
      ) : (
        <div className="space-y-3">
          {models.map((model) => {
            const draft = modelDrafts[model.config_id] ?? model;
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
                        {modelListError && <p role="alert" className="mt-1 text-xs text-destructive">{modelListError}</p>}
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
                    <label className="mb-1 block text-xs text-muted-foreground">{t("admin.systemPrompt")}</label>
                    <Textarea
                      value={draft.system_prompt}
                      onChange={(event) => updateModelDraft(model, { system_prompt: event.target.value })}
                      rows={3}
                      className="resize-none text-xs"
                    />
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
                    {testResult?.error && <p role="alert" className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{testResult.error}</p>}
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
                      <TableCell className="text-xs font-medium">{day.tokens?.toLocaleString()}</TableCell>
                      <TableCell className="text-xs text-success">${day.cost?.toFixed(6)}</TableCell>
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
