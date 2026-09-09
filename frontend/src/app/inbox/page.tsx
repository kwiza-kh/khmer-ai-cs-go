"use client";

import * as React from "react";
import useSWR from "swr";
import { useSearchParams } from "next/navigation";
import {
	InboxItem, SessionStatus, listInbox, listSessionMessages,
	takeoverSession, updateSessionStatus, assignSession, ChatMessageItem,
	agentReply, getSessionSummary, getPreferences, setSessionTags, listCannedResponses, CannedResponse,
	sendTestMessage, RAGSource, PlatformMessageKind, PlatformMessagePayload, PlatformReplyButton,
	listSessionWhatsAppTemplates, WhatsAppTemplate, getInboundPlatformMediaURL, InboundPlatformMedia,
	copilotSuggest, getCustomer360, listCustomers, CustomerProfile, updateCustomerNotes,
	translateText,
	TranslateTarget,
} from "@/lib/api";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Input } from "@/components/ui/input";
import { Button, buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
	Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import {
	Inbox as InboxIcon, Search, UserCircle2, HandMetal, CheckCircle2, XCircle,
	Send, Loader2, Sparkles, Zap, FileText, Clock3, CircleAlert, CheckCheck,
	Image as ImageIcon, ListChecks, FileCode2, Plus, Trash2, Paperclip, MessageSquare, Volume2,
	AlertTriangle, RefreshCw, ChevronDown, ChevronUp, PanelRight, ChevronRight, Phone, Mail, ChevronLeft, Pencil,
	Languages, Check,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth-client";
import { useInboxRealtime } from "@/lib/realtime";
import { Markdown } from "@/components/markdown";
import { useI18n } from "@/lib/i18n";
import {
	Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
	DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

// Filter/status/reply-mode labels are i18n keys resolved at render time.
const STATUS_FILTERS: { value: SessionStatus | "all"; labelKey: string }[] = [
  { value: "all", labelKey: "inbox.filterAll" },
  { value: "active", labelKey: "inbox.filterActive" },
  { value: "handoff", labelKey: "inbox.filterHandoff" },
  { value: "resolved", labelKey: "inbox.filterResolved" },
];

const STATUS_BADGE: Record<SessionStatus, { variant: "default" | "secondary" | "success" | "warning" | "info" | "destructive"; labelKey: string }> = {
  active: { variant: "info", labelKey: "inbox.statusActive" },
  pending: { variant: "warning", labelKey: "inbox.statusPending" },
  handoff: { variant: "warning", labelKey: "inbox.statusAgent" },
  resolved: { variant: "success", labelKey: "inbox.statusResolved" },
  closed: { variant: "secondary", labelKey: "inbox.statusClosed" },
};

type PlatformFilter = "all" | "telegram" | "meta" | "instagram" | "whatsapp" | "line" | "zalo" | "web";
type PlatformCategory = Exclude<PlatformFilter, "all">;
type ComposerKind = PlatformMessageKind;

const PLATFORM_GROUPS: { value: PlatformCategory; label: string; filterLabel: string; dotClass: string }[] = [
  { value: "telegram", label: "Telegram", filterLabel: "Telegram", dotClass: "bg-sky-400" },
  { value: "meta", label: "Messenger", filterLabel: "Messenger", dotClass: "bg-indigo-400" },
  { value: "instagram", label: "Instagram", filterLabel: "Instagram", dotClass: "bg-pink-400" },
  { value: "whatsapp", label: "WhatsApp", filterLabel: "WhatsApp", dotClass: "bg-emerald-400" },
  { value: "line", label: "LINE", filterLabel: "LINE", dotClass: "bg-lime-500" },
  { value: "zalo", label: "Zalo", filterLabel: "Zalo", dotClass: "bg-blue-500" },
  { value: "web", label: "Website", filterLabel: "Website", dotClass: "bg-violet-400" },
];

const PLATFORM_FILTERS: { value: PlatformFilter; filterLabel: string | null }[] = [
  { value: "all", filterLabel: null },
  ...PLATFORM_GROUPS,
];

const PLATFORM_LABELS: Record<string, string> = {
  meta: "Messenger",
  instagram: "Instagram",
  telegram: "Telegram",
  whatsapp: "WhatsApp",
  line: "LINE",
  zalo: "Zalo",
  web: "Website",
};

const CARE_WINDOW_PLATFORMS = new Set(["meta", "instagram", "whatsapp"]);

const REPLY_MODES = [
  { kind: "text" as const, labelKey: "inbox.modeText", icon: MessageSquare },
  { kind: "media" as const, labelKey: "inbox.modeMedia", icon: ImageIcon },
  { kind: "buttons" as const, labelKey: "inbox.modeButtons", icon: ListChecks },
  { kind: "template" as const, labelKey: "inbox.modeTemplate", icon: FileCode2 },
];

// Copilot translation targets — keep in sync with the backend translateTargets
// whitelist (translate_handlers.go). Labels are written in the language itself
// so agents can spot the right one regardless of their UI locale.
const TRANSLATE_LANGS: { code: TranslateTarget; label: string; flag: string }[] = [
  { code: "zh", label: "简体中文", flag: "🇨🇳" },
  { code: "km", label: "ភាសាខ្មែរ", flag: "🇰🇭" },
  { code: "en", label: "English", flag: "🇬🇧" },
  { code: "th", label: "ไทย", flag: "🇹🇭" },
  { code: "vi", label: "Tiếng Việt", flag: "🇻🇳" },
  { code: "lo", label: "ລາວ", flag: "🇱🇦" },
  { code: "my", label: "မြန်မာ", flag: "🇲🇲" },
  { code: "ms", label: "Bahasa Melayu", flag: "🇲🇾" },
  { code: "id", label: "Bahasa Indonesia", flag: "🇮🇩" },
  { code: "ja", label: "日本語", flag: "🇯🇵" },
  { code: "ko", label: "한국어", flag: "🇰🇷" },
  { code: "ar", label: "العربية", flag: "🇸🇦" },
  { code: "ru", label: "Русский", flag: "🇷🇺" },
  { code: "fr", label: "Français", flag: "🇫🇷" },
  { code: "es", label: "Español", flag: "🇪🇸" },
  { code: "de", label: "Deutsch", flag: "🇩🇪" },
];

function translateLang(code: string) {
  return TRANSLATE_LANGS.find((l) => l.code === code) ?? TRANSLATE_LANGS[0];
}

// Dropdown of agent-pickable translation target languages.
function TranslateLangMenu({ current, onPick, label, triggerTitle, triggerClassName, disabled, children }: {
  current?: TranslateTarget;
  onPick: (code: TranslateTarget) => void;
  label: string;
  triggerTitle?: string;
  triggerClassName?: string;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger disabled={disabled} title={triggerTitle} className={triggerClassName}>
        {children}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-auto min-w-40 max-h-72 overflow-y-auto">
        <DropdownMenuLabel>{label}</DropdownMenuLabel>
        {TRANSLATE_LANGS.map((l) => (
          <DropdownMenuItem key={l.code} onClick={() => onPick(l.code)}>
            <span className="w-4 shrink-0 text-center">{l.flag}</span>
            <span className="flex-1">{l.label}</span>
            {current === l.code && <Check className="size-3" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// Module-level translation cache: message_id → rendering. Survives component
// remounts (switching conversations) so the same message is never translated
// twice in one browser session.
const translationCache = new Map<number, { text: string; target: TranslateTarget }>();

function clampRunes(value: string, max: number) {
  return Array.from(value).slice(0, max).join("");
}

function utf8Bytes(value: string) {
  return new TextEncoder().encode(value).length;
}

function clampUTF8(value: string, maxBytes: number) {
  let result = "";
  for (const rune of value) {
    if (utf8Bytes(result + rune) > maxBytes) break;
    result += rune;
  }
  return result;
}

function replyButtonLimits(platform?: string) {
  if (platform === "meta" || platform === "whatsapp") {
    return { maxButtons: platform === "meta" ? 13 : 3, maxTitleRunes: 20, maxPayloadBytes: platform === "meta" ? 1000 : 256 };
  }
  if (platform === "telegram") {
    return { maxButtons: 3, maxTitleRunes: 64, maxPayloadBytes: 64 };
  }
  return { maxButtons: 3, maxTitleRunes: 64, maxPayloadBytes: 1000 };
}

function isPublicHTTPSURL(value: string) {
  try {
    const url = new URL(value.trim());
    return url.protocol === "https:" && Boolean(url.hostname);
  } catch {
    return false;
  }
}

function whatsAppTemplateKey(template: Pick<WhatsAppTemplate, "name" | "language">) {
  return `${template.name}::${template.language}`;
}

function platformCategory(platform?: string): PlatformCategory | null {
  switch (platform) {
    case "telegram":
    case "meta":
    case "instagram":
    case "whatsapp":
    case "line":
    case "web":
      return platform;
    default:
      // NULL/未知平台的会话 (旧数据) 不归入任何平台分组, 仅在 "all" 下展示.
      return null;
  }
}

export default function InboxPage() {
  const { user, token } = useAuth();
  const { t, tf } = useI18n();
  const searchParams = useSearchParams();
  const [statusFilter, setStatusFilter] = React.useState<SessionStatus | "all">("all");
  const [platformFilter, setPlatformFilter] = React.useState<PlatformFilter>("all");
  // 全局顶栏搜索跳转到 /inbox?q=... — 预填本地过滤词.
  const [query, setQuery] = React.useState(() => searchParams.get("q") ?? "");
  // Stay in sync when the top-bar search pushes a new ?q= while already here.
  React.useEffect(() => {
    const next = searchParams.get("q") ?? "";
    setQuery((current) => (current === next ? current : next));
  }, [searchParams]);
  const [activeId, setActiveId] = React.useState<string | null>(null);
	const [notes, setNotes] = React.useState("");
	const [realtimeVersion, setRealtimeVersion] = React.useState(0);
	const [testChatOpen, setTestChatOpen] = React.useState(false);
  const [brokenAvatars, setBrokenAvatars] = React.useState<Set<string>>(new Set());
  const refreshTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const mutateInboxRef = React.useRef<() => void>(() => {});
  const [mobileView, setMobileView] = React.useState<"list" | "chat">(() => (searchParams.get("session") ? "chat" : "list"));
  const [selectMode, setSelectMode] = React.useState(false);
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = React.useState(false);
  // Per-browser UX state hydrated lazily (client-only; SSR-safe fallbacks).
  const [lastSeenMap, setLastSeenMap] = React.useState<Record<string, string>>(() => lsGetJSON<Record<string, string>>(LS_LASTSEEN, {}));
  const [drafts, setDrafts] = React.useState<Record<string, string>>(() => lsGetJSON<Record<string, string>>(LS_DRAFTS, {}));
  React.useEffect(() => { lsSetJSON(LS_DRAFTS, drafts); }, [drafts]);

  const markSeen = React.useCallback((sessionId: string) => {
    setLastSeenMap((prev) => {
      const next = { ...prev, [sessionId]: new Date().toISOString() };
      lsSetJSON(LS_LASTSEEN, next);
      return next;
    });
  }, []);

  const updateDraft = React.useCallback((sessionId: string, value: string) => {
    setDrafts((prev) => ({ ...prev, [sessionId]: value }));
  }, []);

  // Deep-link aware: URL ?session= selects that conversation even before the
  // list resolves (handoff page "open in inbox" relies on this).
  const selectedSessionID = activeId ?? searchParams.get("session") ?? null;

  const handleRealtimeEvent = React.useCallback((event: { session_id: string }) => {
    if (event.session_id === selectedSessionID) {
      setRealtimeVersion((version) => version + 1);
    }
    if (refreshTimerRef.current) clearTimeout(refreshTimerRef.current);
    refreshTimerRef.current = setTimeout(() => { mutateInboxRef.current(); }, 150);
  }, [selectedSessionID]);

  const wsConnected = useInboxRealtime(token, handleRealtimeEvent);

  const inboxKey = `inbox-${statusFilter}`;
  const { data: inboxData, mutate: mutateInbox } = useSWR(
    inboxKey,
    () => listInbox({ status: statusFilter === "all" ? undefined : statusFilter, pageSize: 100 }),
    // WS 事件驱动是主通道 (收到即刷新); 轮询为自愈兜底 — 连接正常时降频, 断线时加密.
    { refreshInterval: wsConnected ? 60_000 : 15_000 },
  );
  React.useEffect(() => { mutateInboxRef.current = () => { void mutateInbox(); }; }, [mutateInbox]);
  // useMemo keeps the array identity stable between renders — the keydown
  // effect depends on it, so this prevents listener churn on every poll.
  const items = React.useMemo(() => (inboxData?.data ?? []).filter((item) => {
    const matchesQuery = !query || (item.title || item.user_display_name || item.last_message || "").toLowerCase().includes(query.toLowerCase());
    return matchesQuery && (platformFilter === "all" || platformCategory(item.platform) === platformFilter);
  }), [inboxData, query, platformFilter]);
  const groupedItems = React.useMemo(() => {
    const groups = platformFilter === "all"
      ? PLATFORM_GROUPS
      : PLATFORM_GROUPS.filter((group) => group.value === platformFilter);
    return groups
      .map((group) => ({ ...group, items: items.filter((item) => platformCategory(item.platform) === group.value) }))
      .filter((group) => group.items.length > 0);
  }, [items, platformFilter]);
  const active = items.find((item) => item.session_id === selectedSessionID) ?? null;

  // Adopt a deep-linked ?session= exactly once per param change: the URL wins
  // over a stale activeId so the conversation opens like a manual click.
  const adoptedSessionRef = React.useRef<string | null>(null);
  React.useEffect(() => {
    const deepLink = searchParams.get("session");
    if (adoptedSessionRef.current === deepLink) return;
    adoptedSessionRef.current = deepLink;
    if (!deepLink) return;
    setActiveId(deepLink);
    setNotes("");
    setMobileView("chat");
    markSeen(deepLink);
  }, [searchParams, markSeen]);

  // ↑/↓ moves across the filtered conversation list (ignored while typing).
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT" || target.isContentEditable)) return;
      if (selectMode || items.length === 0) return;
      e.preventDefault();
      const idx = items.findIndex((entry) => entry.session_id === selectedSessionID);
      const nextIdx = idx === -1
        ? 0
        : e.key === "ArrowDown" ? Math.min(idx + 1, items.length - 1) : Math.max(idx - 1, 0);
      const candidate = items[nextIdx];
      if (!candidate || candidate.session_id === selectedSessionID) return;
      setActiveId(candidate.session_id);
      setNotes("");
      setMobileView("chat");
      markSeen(candidate.session_id);
      document.querySelector(`[data-sid="${candidate.session_id}"]`)?.scrollIntoView({ block: "nearest" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [items, selectedSessionID, selectMode, markSeen]);

  const toggleSelected = React.useCallback((sessionId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(sessionId)) next.delete(sessionId); else next.add(sessionId);
      return next;
    });
  }, []);

  const runBulk = async (action: "assign" | "close") => {
    const ids = [...selectedIds];
    if (ids.length === 0) return;
    setBulkBusy(true);
    let ok = 0;
    for (const id of ids) {
      try {
        if (action === "assign") await assignSession(id, user?.user_id ?? 0, "");
        else await updateSessionStatus(id, "closed", "");
        ok += 1;
      } catch { /* keep going through the rest */ }
    }
    const failed = ids.length - ok;
    const doneKey = action === "assign" ? "inbox.bulkAssigned" : "inbox.bulkClosed";
    if (failed === 0) {
      toast.success(tf(doneKey, { ok, total: ids.length }));
    } else {
      // Partial failure must be visible: the operator needs to know which
      // conversations still need attention.
      toast.warning(tf("inbox.bulkPartial", { ok, total: ids.length, failed }));
    }
    setBulkBusy(false);
    setSelectedIds(new Set());
    void mutateInbox();
  };

  React.useEffect(() => () => {
    if (refreshTimerRef.current) clearTimeout(refreshTimerRef.current);
  }, []);

  return (
    <div className="flex h-full flex-col">
		<PageHeader
			icon={InboxIcon}
			kicker={t("inbox.kicker")}
			title={t("inbox.title")}
			description={t("inbox.description")}
			actions={
				<Button size="sm" variant="outline" onClick={() => setTestChatOpen(true)} className="gap-1.5">
					<Sparkles className="size-3.5" /> {t("inbox.testAi")}
				</Button>
			}
		/>

      <div className="flex-1 flex overflow-hidden">
        {/* Left: conversation list */}
        <aside className={cn("w-80 shrink-0 flex-col border-r border-border bg-card text-card-foreground", mobileView === "chat" ? "hidden lg:flex" : "flex")}>
          <div className="space-y-3 border-b border-border p-3">
            <div className="flex items-center justify-between">
              <button
                type="button"
                onClick={() => setMobileView("list")}
                className="inline-flex items-center gap-1 text-[11px] text-muted-foreground lg:hidden"
              >
                <InboxIcon className="size-3" /> {t("inbox.title")}
              </button>
              <span className="hidden text-[11px] font-semibold uppercase tracking-wide text-muted-foreground lg:inline">{t("inbox.title")}</span>
              <button
                type="button"
                onClick={() => { setSelectMode((v) => !v); setSelectedIds(new Set()); }}
                aria-pressed={selectMode}
                className={cn(
                  "inline-flex h-6 items-center gap-1 rounded px-1.5 text-[11px] transition-colors",
                  selectMode ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )}
              >
                <ListChecks className="size-3" /> {t("inbox.bulkMode")}
              </button>
            </div>
            <Select value={statusFilter} onValueChange={(v) => setStatusFilter((v || "all") as SessionStatus | "all")}>
              <SelectTrigger className="h-9 rounded-lg text-xs"><SelectValue /></SelectTrigger>
              <SelectContent>
                {STATUS_FILTERS.map((f) => <SelectItem key={f.value} value={f.value}>{t(f.labelKey)}</SelectItem>)}
              </SelectContent>
            </Select>
            <div className="grid grid-cols-3 gap-1" role="group" aria-label="Filter by platform">
              {PLATFORM_FILTERS.map((filter) => (
                <Button
                  key={filter.value}
                  type="button"
                  size="sm"
                  variant={platformFilter === filter.value ? "default" : "ghost"}
                  onClick={() => setPlatformFilter(filter.value)}
                  aria-pressed={platformFilter === filter.value}
                  className="h-8 w-full justify-start rounded-md px-2 text-[11px] font-medium"
                >
                  {filter.filterLabel ?? t("inbox.platformAll")}
                </Button>
              ))}
            </div>
            <div className="relative">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
              <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("inbox.search")} className="h-9 rounded-lg pl-8 text-xs" />
            </div>
          </div>

          <ScrollArea className="flex-1">
            <div className="p-2.5">
              {groupedItems.length === 0 ? (
                <EmptyState
                  icon={query || platformFilter !== "all" ? Search : InboxIcon}
                  title={query || platformFilter !== "all" ? t("inbox.emptyFiltered") : t("inbox.emptyTitle")}
                  description={query || platformFilter !== "all" ? t("inbox.emptyFilteredDesc") : t("inbox.emptyDesc")}
                />
              ) : groupedItems.map((group) => (
                <div key={group.value} className="mb-4 last:mb-0">
                  <div className="flex items-center gap-2 px-1.5 pb-1.5">
                    <span className={cn("size-1.5 shrink-0 rounded-full", group.dotClass)} />
                    <span className="text-[11px] font-semibold tracking-wide text-muted-foreground">{group.label}</span>
                    <span className="h-px flex-1 bg-border" />
                    <span className="text-[11px] tabular-nums text-muted-foreground">{group.items.length}</span>
                  </div>
                  <div className="space-y-1">
                    {group.items.map((item) => (
                      <button
                        key={item.session_id}
                        data-sid={item.session_id}
                        onClick={() => {
                          if (selectMode) { toggleSelected(item.session_id); return; }
                          setActiveId(item.session_id); setNotes(""); setMobileView("chat"); markSeen(item.session_id);
                        }}
                        className={cn(
                          "flex w-full items-start gap-2.5 border px-3 py-2.5 text-left transition-colors",
                          activeId === item.session_id
                            ? "border-primary/40 bg-primary/10"
                            : "border-transparent hover:border-border hover:bg-muted/70",
                          selectMode && selectedIds.has(item.session_id) && "border-primary/60 bg-primary/5",
                        )}
                      >
                        {item.avatar_url && !brokenAvatars.has(item.avatar_url) ? (
                          /* eslint-disable-next-line @next/next/no-img-element */
                          <img
                            src={item.avatar_url}
                            alt=""
                            className="mt-0.5 size-8 shrink-0 rounded-full border border-border object-cover"
                            onError={() => {
                              setBrokenAvatars((prev) => {
                                const next = new Set(prev);
                                next.add(item.avatar_url as string);
                                return next;
                              });
                            }}
                          />
                        ) : (
                          <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full border border-border bg-muted text-[11px] font-semibold text-muted-foreground">
                            {(item.user_display_name || item.title || item.platform_user_id || "?")
                              .trim()
                              .charAt(0)
                              .toUpperCase()}
                          </span>
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="mb-1 flex items-center justify-between gap-2">
                            <p className="min-w-0 flex-1 truncate text-[13px] font-semibold text-foreground">
                              {item.user_display_name || item.title || item.platform_user_id || t("inbox.anonymous")}
                            </p>
                            <div className="flex items-center gap-1.5">
                              {item.last_message_at && item.last_message_at > (lastSeenMap[item.session_id] ?? "") && (
                                <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-label={t("inbox.newMessages")} />
                              )}
                              {item.last_message_at && <span className="shrink-0 text-[10px] tabular-nums text-muted-foreground">{fmtRelTime(item.last_message_at, t("inbox.timeNow"))}</span>}
                              {item.sentiment === "negative" && (
                                <Badge variant="destructive" className="h-4 px-1.5 text-[10px] gap-0.5">
                                  <AlertTriangle className="size-2.5" /> {t("inbox.angry")}
                                </Badge>
                              )}
                              {item.sentiment === "positive" && (
                                <Badge variant="success" className="h-4 px-1.5 text-[10px]">😊</Badge>
                              )}
                              <Badge variant={STATUS_BADGE[item.status].variant} className="h-4 px-1.5 text-[10px]">
                                {t(STATUS_BADGE[item.status].labelKey)}
                              </Badge>
                            </div>
                          </div>
                          <p className="truncate text-xs leading-5 text-muted-foreground">{item.last_message || t("inbox.noMessagesPreview")}</p>
                          <p className="mt-1 text-[11px] text-muted-foreground">
                            {tf("inbox.messagesCount", { n: item.user_message_count + item.model_message_count })}
                          </p>
                        </div>
                      </button>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </ScrollArea>

          {selectMode && (
            <div className="space-y-2 border-t border-border bg-card p-2.5">
              <p className="text-xs text-muted-foreground">{tf("inbox.bulkSelected", { n: selectedIds.size })}</p>
              <div className="grid grid-cols-3 gap-1.5">
                <Button size="sm" variant="outline" disabled={bulkBusy || selectedIds.size === 0} onClick={() => void runBulk("assign")} className="h-7 px-1 text-[11px]">
                  {t("inbox.reassignToMe")}
                </Button>
                <Button size="sm" variant="outline" disabled={bulkBusy || selectedIds.size === 0} onClick={() => void runBulk("close")} className="h-7 px-1 text-[11px]">
                  {t("inbox.bulkClose")}
                </Button>
                <Button size="sm" variant="ghost" disabled={bulkBusy} onClick={() => { setSelectMode(false); setSelectedIds(new Set()); }} className="h-7 text-[11px]">
                  {t("inbox.bulkExit")}
                </Button>
              </div>
            </div>
          )}
        </aside>

        {/* Right: conversation detail */}
        <section className={cn("flex-1 flex flex-col min-w-0 bg-background text-foreground", mobileView === "list" && "hidden lg:flex")}>
          {active ? (
            <ConversationDetail
              key={active.session_id}
              item={active}
              currentUserId={user?.user_id ?? 0}
              notes={notes}
              onNotesChange={setNotes}
              onMutate={() => { void mutateInbox(); }}
              realtimeVersion={realtimeVersion}
              draft={drafts[active.session_id] ?? ""}
              onDraftChange={(value) => updateDraft(active.session_id, value)}
              onBackToList={() => setMobileView("list")}
            />
          ) : (
            <div className="flex-1 flex items-center justify-center">
              <div className="text-center max-w-xs px-6">
                <div className="mx-auto size-14 rounded-2xl bg-muted/40 flex items-center justify-center mb-4">
                  <InboxIcon className="size-7 text-muted-foreground/50" />
                </div>
                <p className="text-sm font-medium text-foreground mb-1">{t("inbox.selectConversation")}</p>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {t("inbox.selectConversationDesc")}
                </p>
              </div>
            </div>
          )}
			</section>
		</div>
		<TestChatDialog open={testChatOpen} onOpenChange={setTestChatOpen} />
		</div>
	);
}

type TestChatMessage = {
	id: number;
	role: "user" | "model";
	content: string;
	sources?: RAGSource[];
	pending?: boolean;
	usedMock?: boolean;
};

function TestChatDialog({ open, onOpenChange }: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { t, tf } = useI18n();
	const [sessionId, setSessionId] = React.useState<string>();
	const [messages, setMessages] = React.useState<TestChatMessage[]>([]);
	const [draft, setDraft] = React.useState("");
	const [sending, setSending] = React.useState(false);
	const messageID = React.useRef(0);
	const messageScrollRef = React.useRef<HTMLDivElement>(null);

	React.useEffect(() => {
		const container = messageScrollRef.current;
		if (container) container.scrollTop = container.scrollHeight;
	}, [messages]);

	const nextMessageID = () => {
		messageID.current += 1;
		return messageID.current;
	};

	const startNewTest = () => {
		setSessionId(undefined);
		setMessages([]);
		setDraft("");
	};

	const sendTest = async () => {
		const content = draft.trim();
		if (!content || sending) return;

		const pendingID = nextMessageID();
		setDraft("");
		setSending(true);
		setMessages((current) => [
			...current,
			{ id: nextMessageID(), role: "user", content },
			{ id: pendingID, role: "model", content: "", pending: true },
		]);

		try {
			const result = await sendTestMessage(content, sessionId);
			setSessionId(result.session_id);
			setMessages((current) => current.map((message) => (
				message.id === pendingID
					? {
						...message,
						content: result.reply,
						sources: result.sources,
						usedMock: result.used_mock,
						pending: false,
					}
					: message
			)));
		} catch (error) {
			setMessages((current) => current.filter((message) => message.id !== pendingID));
			toast.error((error as Error).message || t("testchat.failed"));
		} finally {
			setSending(false);
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="!flex h-[min(44rem,calc(100vh-2rem))] max-h-[calc(100vh-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl" showCloseButton={!sending}>
				<DialogHeader className="border-b border-border px-5 py-4 pr-12">
					<div className="flex items-center justify-between gap-3">
						<div>
							<DialogTitle className="flex items-center gap-2"><Sparkles className="size-4 text-primary" /> {t("testchat.title")}</DialogTitle>
							<DialogDescription className="mt-1 text-xs">{t("testchat.description")}</DialogDescription>
						</div>
						<Button type="button" size="sm" variant="ghost" onClick={startNewTest} disabled={sending} className="h-7 px-2 text-xs">
							{t("testchat.newTest")}
						</Button>
					</div>
				</DialogHeader>

				<div ref={messageScrollRef} className="min-h-0 flex-1 overflow-y-auto bg-muted/20 px-4 py-5 sm:px-6">
					{messages.length === 0 ? (
						<div className="flex h-full items-center justify-center">
							<div className="max-w-sm text-center">
								<Sparkles className="mx-auto mb-3 size-7 text-primary/60" />
								<p className="text-sm font-medium text-foreground">{t("testchat.emptyTitle")}</p>
								<p className="mt-1 text-xs leading-relaxed text-muted-foreground">{t("testchat.emptyDesc")}</p>
							</div>
						</div>
					) : (
						<div className="mx-auto max-w-2xl space-y-4">
							{messages.map((message) => (
								<div key={message.id} className={cn("flex gap-2.5", message.role === "user" && "flex-row-reverse")}>
									<div className={cn(
										"mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md text-[10px] font-semibold",
										message.role === "user" ? "bg-accent text-accent-foreground" : "bg-primary/10 text-primary",
									)}>
										{message.role === "user" ? t("testchat.you") : "AI"}
									</div>
									<div className={cn(
										"max-w-[85%] rounded-md border px-3 py-2.5 text-sm",
										message.role === "user" ? "bg-accent text-accent-foreground" : "bg-card text-card-foreground",
									)}>
										{message.pending ? <Loader2 className="size-4 animate-spin text-muted-foreground" /> : message.role === "user" ? (
											<p className="whitespace-pre-wrap leading-relaxed">{message.content}</p>
										) : <Markdown>{message.content}</Markdown>}
										{message.usedMock && <Badge variant="warning" className="mt-2 h-4 px-1.5 text-[10px]">{t("testchat.mockReply")}</Badge>}
										{message.sources && message.sources.length > 0 && (
											<div className="mt-3 flex flex-wrap gap-1.5 border-t border-border pt-2">
												{message.sources.map((source) => (
													<span key={`${source.doc_id}-${source.title}`} title={tf("testchat.similarity", { score: (source.score * 100).toFixed(0) })} className="inline-flex max-w-full items-center gap-1 rounded border border-border bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
														<FileText className="size-3 shrink-0" />
														<span className="truncate">{source.title}</span>
													</span>
												))}
											</div>
										)}
									</div>
								</div>
							))}
						</div>
					)}
				</div>

				<div className="border-t border-border bg-background p-4 sm:px-5">
					<div className="flex items-end gap-2">
						<Textarea
							value={draft}
							onChange={(event) => setDraft(event.target.value)}
							onKeyDown={(event) => {
								if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
									event.preventDefault();
									void sendTest();
								}
							}}
							placeholder={t("testchat.placeholder")}
							rows={2}
							disabled={sending}
							className="min-h-10 flex-1 resize-none text-sm"
						/>
						<Button type="button" size="sm" onClick={() => void sendTest()} disabled={sending || !draft.trim()} className="h-10 gap-1.5">
							{sending ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
							{t("testchat.send")}
						</Button>
					</div>
					<p className="mt-1.5 text-[11px] text-muted-foreground">{t("testchat.hint")}</p>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function ConversationDetail({
  item, currentUserId, notes, onNotesChange, onMutate, realtimeVersion, draft, onDraftChange, onBackToList,
}: {
  item: InboxItem;
  currentUserId: number;
  notes: string;
  onNotesChange: (v: string) => void;
  onMutate: () => void;
  realtimeVersion: number;
  draft: string;
  onDraftChange: (v: string) => void;
  onBackToList: () => void;
}) {
  // Transcript: newest 200 on mount, then message_id-cursor deltas driven by
  // realtime events with a 15s fallback poll (WS 断线自愈). The component is
  // keyed by session_id upstream, so switching conversations remounts it.
  const { t, tf } = useI18n();
  const [msgList, setMsgList] = React.useState<ChatMessageItem[]>([]);
  const maxMessageIdRef = React.useRef(0);
  const [hasEarlier, setHasEarlier] = React.useState(false);
  const [loadingEarlier, setLoadingEarlier] = React.useState(false);
  const prependingRef = React.useRef(false);
  const [unreadSeen, setUnreadSeen] = React.useState(0);

  // Copilot translation: ONE global toggle in the header (persisted) turns on
  // automatic translation of every customer message into the picked language.
  // Results are cached per message id at module scope, so switching
  // conversations never re-requests the same message.
  const [autoTranslate, setAutoTranslate] = React.useState<boolean>(() => lsGetJSON<boolean>("inbox.autoTranslate", false));
  const [readLang, setReadLang] = React.useState<TranslateTarget>(() => lsGetJSON<TranslateTarget>("inbox.readLang", "zh"));
  const [replyLang, setReplyLang] = React.useState<TranslateTarget>(() => lsGetJSON<TranslateTarget>("inbox.replyLang", "km"));
  const [translations, setTranslations] = React.useState<Record<number, { text: string; target: TranslateTarget }>>({});
  const [translatingDraft, setTranslatingDraft] = React.useState(false);
  const inflightRef = React.useRef<Set<number>>(new Set());

  React.useEffect(() => {
    lsSetJSON("inbox.autoTranslate", autoTranslate);
  }, [autoTranslate]);

  const translateOne = React.useCallback((messageID: number, content: string, target: TranslateTarget) => {
    if (inflightRef.current.has(messageID)) return;
    inflightRef.current.add(messageID);
    void translateText(content, target)
      .then((res) => {
        const entry = { text: res.translation, target: res.target as TranslateTarget };
        translationCache.set(messageID, entry);
        setTranslations((prev) => ({ ...prev, [messageID]: entry }));
      })
      .catch(() => { /* silent — toggling the switch retries */ })
      .finally(() => { inflightRef.current.delete(messageID); });
  }, []);

  // Auto-translate every customer message (cache-first) while the toggle is on.
  React.useEffect(() => {
    if (!autoTranslate) return;
    for (const m of msgList) {
      if (m.role !== "user" || !m.content) continue;
      const cached = translationCache.get(m.message_id);
      if (cached) {
        if (cached.target === readLang) {
          setTranslations((prev) => (prev[m.message_id] === cached ? prev : { ...prev, [m.message_id]: cached }));
          continue;
        }
        if (cached.target !== readLang) {
          // Language changed — re-translate into the newly picked target.
          translateOne(m.message_id, m.content, readLang);
          continue;
        }
      }
      translateOne(m.message_id, m.content, readLang);
    }
  }, [autoTranslate, msgList, readLang, translateOne]);

  const pickReadLang = (target: TranslateTarget) => {
    setReadLang(target);
    lsSetJSON("inbox.readLang", target);
  };

  const handleTranslateDraft = async (target: TranslateTarget) => {
    if (!draft.trim()) return;
    setReplyLang(target);
    lsSetJSON("inbox.replyLang", target);
    setTranslatingDraft(true);
    try {
      const res = await translateText(draft, target);
      onDraftChange(res.translation);
    } catch (err: unknown) {
      toast.error((err as Error).message || t("inbox.translateFailed"));
    } finally {
      setTranslatingDraft(false);
    }
  };

  const pullMessages = React.useCallback(async (after: number) => {
    try {
      const rows = await listSessionMessages(item.session_id, 200, after);
      if (rows.length === 0) return;
      maxMessageIdRef.current = rows[rows.length - 1].message_id;
      const seenMap = lsGetJSON<Record<string, number>>(LS_LASTSEENMSG, {});
      if ((seenMap[item.session_id] ?? 0) < maxMessageIdRef.current) {
        seenMap[item.session_id] = maxMessageIdRef.current;
        lsSetJSON(LS_LASTSEENMSG, seenMap);
      }
      setMsgList((prev) => {
        const known = new Set(prev.map((m) => m.message_id));
        const fresh = rows.filter((m) => !known.has(m.message_id));
        return fresh.length > 0 ? [...prev, ...fresh] : prev;
      });
    } catch {
      // Transient error — the fallback poll retries.
    }
  }, [item.session_id]);

  React.useEffect(() => {
    let cancelled = false;
    void listSessionMessages(item.session_id, 200)
      .then((rows) => {
        if (cancelled) return;
        if (rows.length > 0) maxMessageIdRef.current = rows[rows.length - 1].message_id;
        setHasEarlier(rows.length >= 200);
        // Capture the client-side "last read" marker once per open, then
        // advance the stored marker to the newest message.
        const seenMap = lsGetJSON<Record<string, number>>(LS_LASTSEENMSG, {});
        setUnreadSeen(seenMap[item.session_id] ?? 0);
        if (rows.length > 0 && maxMessageIdRef.current > (seenMap[item.session_id] ?? 0)) {
          seenMap[item.session_id] = maxMessageIdRef.current;
          lsSetJSON(LS_LASTSEENMSG, seenMap);
        }
        setMsgList(rows);
      })
      .catch(() => undefined);
    const timer = window.setInterval(() => {
      void pullMessages(maxMessageIdRef.current);
    }, 15_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [item.session_id, pullMessages]);

  // Backwards paging: prepend the page immediately older than the oldest
  // message in memory; hide the button once the transcript is exhausted.
  const loadEarlier = async () => {
    if (loadingEarlier || msgList.length === 0) return;
    setLoadingEarlier(true);
    try {
      const rows = await listSessionMessages(item.session_id, 100, 0, msgList[0].message_id);
      if (rows.length > 0) {
        prependingRef.current = true;
        setMsgList((prev) => {
          const known = new Set(prev.map((m) => m.message_id));
          const fresh = rows.filter((m) => !known.has(m.message_id));
          return [...fresh, ...prev];
        });
      }
      setHasEarlier(rows.length >= 100);
    } catch {
      // keep the button available for retry
    } finally {
      setLoadingEarlier(false);
    }
  };

  const lastVersionRef = React.useRef(realtimeVersion);
  React.useEffect(() => {
    // Skip the version value present at mount (this pane remounts per session,
    // so an inherited count must not trigger a redundant fetch).
    if (realtimeVersion === lastVersionRef.current) return;
    lastVersionRef.current = realtimeVersion;
    void pullMessages(maxMessageIdRef.current);
  }, [realtimeVersion, pullMessages]);
  const isMine = item.assigned_agent_id === currentUserId;
  const canReply = isMine || item.status === "handoff" || item.status === "pending";

  // Conversation summary — follows the user's language preference and is
  // regenerated server-side when the transcript grows. The key includes the
  // language + refresh counter so changing either re-fetches immediately.
  const { data: prefs } = useSWR("auth-preferences", getPreferences);
  const summaryLang = prefs?.language ?? "auto";
  const [summaryRefresh, setSummaryRefresh] = React.useState(0);
  const summaryForceRef = React.useRef(false);
  const [panelOpen, setPanelOpen] = React.useState(() => typeof window !== "undefined" && window.localStorage.getItem(LS_PANEL) !== "0");
  React.useEffect(() => {
    try { window.localStorage.setItem(LS_PANEL, panelOpen ? "1" : "0"); } catch { /* ignore */ }
  }, [panelOpen]);
  const { data: summaryData, isValidating: summaryLoading } = useSWR(
    item.session_id && msgList.length > 0 && prefs
      ? `inbox-summary-${item.session_id}-${summaryLang}-${summaryRefresh}`
      : null,
    async () => {
      const res = await getSessionSummary(item.session_id, summaryLang, summaryForceRef.current);
      summaryForceRef.current = false;
      return res;
    },
  );

  // Nudge the summary to regenerate a few seconds after the transcript grows
  // (the server caches per transcript, so this stays cheap).
  React.useEffect(() => {
    if (msgList.length === 0) return;
    const timer = window.setTimeout(() => setSummaryRefresh((v) => v + 1), 5_000);
    return () => window.clearTimeout(timer);
  }, [msgList.length]);

  // Non-zero only while the newest message is from the customer — the AI panel
  // uses this to auto-refresh suggested replies.
  const lastUserMessageId = msgList.length > 0 && msgList[msgList.length - 1].role === "user"
    ? msgList[msgList.length - 1].message_id
    : 0;

  const unreadBeforeId = React.useMemo(() => {
    if (unreadSeen <= 0) return null;
    const first = msgList.find((m) => m.message_id > unreadSeen);
    return first ? first.message_id : null;
  }, [msgList, unreadSeen]);

  const [notesOpen, setNotesOpen] = React.useState(false);
  const [sending, setSending] = React.useState(false);
  const [currentTime, setCurrentTime] = React.useState(() => Date.now());
  const [cannedOpen, setCannedOpen] = React.useState(false);
  const [cannedQuery, setCannedQuery] = React.useState("");
  const [composerKind, setComposerKind] = React.useState<ComposerKind>("text");
  const [mediaURL, setMediaURL] = React.useState("");
  const [mediaType, setMediaType] = React.useState<NonNullable<PlatformMessagePayload["media_type"]>>("image");
  const [buttons, setButtons] = React.useState<PlatformReplyButton[]>([{ title: "", payload: "" }]);
  const [templateName, setTemplateName] = React.useState("");
  const [templateLanguage, setTemplateLanguage] = React.useState("en_US");
  const [templateParameters, setTemplateParameters] = React.useState("");
  const isWhatsApp = item.platform === "whatsapp";
  const hasCustomerCareWindow = CARE_WINDOW_PLATFORMS.has(item.platform || "");
  const platformName = PLATFORM_LABELS[item.platform || "web"] || item.platform || "Platform";
  const replyWindowExpiry = item.reply_window_expires_at ? new Date(item.reply_window_expires_at).getTime() : NaN;
  const replyWindowKnown = Number.isFinite(replyWindowExpiry);
  // Messenger/Instagram: human replies may continue for 7 days after the
  // customer's last message via the HUMAN_AGENT tag (WhatsApp is template-only).
  const humanAgentDeadline = replyWindowExpiry + 6 * 24 * 60 * 60 * 1000;
  const replyWindowExtended = hasCustomerCareWindow && !isWhatsApp && replyWindowKnown
    && currentTime > replyWindowExpiry && currentTime < humanAgentDeadline;
  const replyWindowExpired = hasCustomerCareWindow
    && (!replyWindowKnown || currentTime >= (isWhatsApp ? replyWindowExpiry : humanAgentDeadline));
  const isTemplateReply = composerKind === "template";
  const isReplyBlocked = replyWindowExpired && !isTemplateReply;
  const supportsRichReplies = item.platform === "meta" || item.platform === "instagram" || item.platform === "telegram" || item.platform === "whatsapp";
  const buttonLimits = replyButtonLimits(item.platform);
  const normalizedButtons = buttons.map((button) => ({
    title: button.title.trim(),
    payload: button.payload.trim() || button.title.trim(),
  }));
  const templateBodyParams = templateParameters
    .split(/[\n,]/)
    .map((value) => value.trim())
    .filter(Boolean);
  const { data: whatsAppTemplateData, error: whatsAppTemplateError, isLoading: isLoadingWhatsAppTemplates } = useSWR(
    isWhatsApp && composerKind === "template" ? `whatsapp-templates-${item.session_id}` : null,
    () => listSessionWhatsAppTemplates(item.session_id),
  );
  const whatsAppTemplates = whatsAppTemplateData?.templates ?? [];
  const selectedTemplate = whatsAppTemplates.find((template) => template.name === templateName && template.language === templateLanguage);
  const buttonsValid = normalizedButtons.length > 0
    && normalizedButtons.length <= buttonLimits.maxButtons
    && normalizedButtons.every((button) => Boolean(button.title)
      && Array.from(button.title).length <= buttonLimits.maxTitleRunes
      && utf8Bytes(button.payload) <= buttonLimits.maxPayloadBytes);
  const templateReady = selectedTemplate !== undefined
    && templateBodyParams.length === selectedTemplate.body_parameter_count;
  const canSubmitReply = !sending && !isReplyBlocked && (
    composerKind === "text"
      ? Boolean(draft.trim())
      : composerKind === "media"
        ? isPublicHTTPSURL(mediaURL)
        : composerKind === "buttons"
          ? Boolean(draft.trim()) && buttonsValid
          : templateReady
  );

  React.useEffect(() => {
    if (!hasCustomerCareWindow || !replyWindowKnown) return;
    const timer = window.setInterval(() => setCurrentTime(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [hasCustomerCareWindow, replyWindowKnown]);

  // Canned-reply library for this conversation's language (operators curate
  // them in Settings → Canned responses). Falls back gracefully to all
  // languages when none exist for the session's language.
  const { data: canned } = useSWR(
    `canned-${item.language}`,
    () => listCannedResponses(undefined, item.language),
  );
  const cannedAll: CannedResponse[] = (canned ?? []).filter((c) => c.is_active !== false);

  const handleSendReply = async () => {
    if (!canSubmitReply) return;
    let payload: PlatformMessagePayload | undefined;
    if (composerKind === "media") {
      payload = { kind: "media", media_url: mediaURL.trim(), media_type: mediaType };
    } else if (composerKind === "buttons") {
      payload = { kind: "buttons", buttons: normalizedButtons };
    } else if (composerKind === "template") {
      payload = {
        kind: "template",
        template_name: templateName.trim(),
        template_language: templateLanguage.trim(),
        template_body_params: templateBodyParams,
      };
    }
    setSending(true);
    try {
      await agentReply(item.session_id, { content: composerKind === "template" ? "" : draft.trim(), payload });
      onDraftChange("");
      setMediaURL("");
      setButtons([{ title: "", payload: "" }]);
      setTemplateName("");
      setTemplateParameters("");
      setComposerKind("text");
      void pullMessages(maxMessageIdRef.current);
      toast.success(payload ? t("inbox.toastQueued") : t("inbox.toastSent"));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSending(false);
    }
  };

  const insertCanned = (body: string) => {
    const cur = draft.trim();
    // Place the snippet on its own line below the draft.
    onDraftChange(cur ? `${cur}\n\n${body}` : body);
    setCannedOpen(false);
    setCannedQuery("");
  };

  const filteredCanned = cannedQuery
    ? cannedAll.filter((c) =>
        (c.title + " " + c.body + " " + (c.category || "")).toLowerCase().includes(cannedQuery.toLowerCase()))
    : cannedAll;

  // Keep the newest message in view whenever the transcript changes.
  const messagesScrollRef = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    // Prepending history must not yank the viewport to the bottom.
    if (prependingRef.current) { prependingRef.current = false; return; }
    const viewport = messagesScrollRef.current?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]');
    if (viewport) viewport.scrollTop = viewport.scrollHeight;
  }, [msgList.length, item.session_id]);

  const handleTakeover = async () => {
    try { await takeoverSession(item.session_id); toast.success(t("inbox.toastTakenOver")); onMutate(); }
    catch (e) { toast.error((e as Error).message); }
  };
  const handleResolve = async () => {
    try { await updateSessionStatus(item.session_id, "resolved", notes); toast.success(t("inbox.toastResolved")); onMutate(); }
    catch (e) { toast.error((e as Error).message); }
  };
  const handleClose = async () => {
    try { await updateSessionStatus(item.session_id, "closed", notes); toast.success(t("inbox.toastClosed")); onMutate(); }
    catch (e) { toast.error((e as Error).message); }
  };
  const handleAssignToMe = async () => {
    try { await assignSession(item.session_id, currentUserId, notes); toast.success(t("inbox.toastAssigned")); onMutate(); }
    catch (e) { toast.error((e as Error).message); }
  };

  return (
    <div className="flex h-full min-w-0">
      <div className="flex flex-col h-full flex-1 min-w-0">
      {/* Detail header */}
      <div className="px-5 py-3 border-b border-border flex items-center justify-between flex-wrap gap-2">
        <div className="min-w-0">
          <p className="text-base font-semibold truncate">
            {item.user_display_name || item.title || t("inbox.anonymous")}
          </p>
          <p className="text-sm text-muted-foreground">
            {item.platform} · {tf("inbox.sessionLabel", { id: item.session_id.slice(0, 8) })}
            {item.assigned_agent_name && ` · ${tf("inbox.agentLabel", { name: item.assigned_agent_name })}`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {/* Global translation toggle: on → every customer message is
              translated automatically into the picked language (cached). */}
          <div className="flex items-center gap-1 rounded-md border border-border bg-card px-1.5 py-1">
            <button
              type="button"
              role="switch"
              aria-checked={autoTranslate}
              onClick={() => setAutoTranslate((v) => !v)}
              title={t("inbox.autoTranslateTitle")}
              className={cn(
                "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs transition-colors",
                autoTranslate ? "bg-primary/15 text-primary" : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Languages className="size-3.5" />
              <span className="hidden sm:inline">{t("inbox.autoTranslate")}</span>
              <span className={cn("ml-0.5 h-3 w-5 rounded-full transition-colors", autoTranslate ? "bg-primary" : "bg-muted-foreground/30")}>
                <span className={cn("block size-3 rounded-full bg-background transition-transform", autoTranslate && "translate-x-2")} />
              </span>
            </button>
            {autoTranslate && (
              <TranslateLangMenu
                current={readLang}
                onPick={pickReadLang}
                label={t("inbox.translatePick")}
                triggerTitle={t("inbox.translatePick")}
                triggerClassName="inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
              >
                {translateLang(readLang).flag} {translateLang(readLang).label} <ChevronDown className="size-3" />
              </TranslateLangMenu>
            )}
          </div>
          <Button size="sm" variant="ghost" onClick={onBackToList} className="h-7 w-7 p-0 lg:hidden" title={t("inbox.backToList")}>
            <ChevronLeft className="size-4" />
          </Button>
          {!panelOpen && (
            <Button size="sm" variant="outline" onClick={() => setPanelOpen(true)} className="h-7 w-7 p-0" title={t("inbox.aiPanel")}>
              <PanelRight className="size-3.5" />
            </Button>
          )}
          {!isMine && (
            <Button size="sm" onClick={handleTakeover} className="h-7 text-xs gap-1.5">
              <HandMetal className="size-3" />{t("inbox.takeOver")}
            </Button>
          )}
          {isMine && (
            <Button size="sm" variant="outline" onClick={handleAssignToMe} className="h-7 text-xs gap-1.5">
              <UserCircle2 className="size-3" />{t("inbox.reassignToMe")}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={handleResolve} className="h-7 text-xs gap-1.5">
            <CheckCircle2 className="size-3" />{t("inbox.resolve")}
          </Button>
          <Button size="sm" variant="ghost" onClick={handleClose} className="h-7 text-xs gap-1.5 text-muted-foreground">
            <XCircle className="size-3" />{t("inbox.close")}
          </Button>
        </div>
      </div>

      {/* Tags editor — quick conversation triage */}
      <TagsBar sessionId={item.session_id} initial={item.tags ?? []} onMutate={onMutate} />

      {hasCustomerCareWindow && (
        <div className={cn("mx-5 mt-2 flex items-start gap-2 border px-3 py-1.5 text-xs", replyWindowExpired || replyWindowExtended ? "border-warning/40 bg-warning/10 text-warning" : "border-success/30 bg-success/10 text-foreground")}>
          {replyWindowExpired ? <CircleAlert className="mt-0.5 size-3.5 shrink-0" /> : <Clock3 className="mt-0.5 size-3.5 shrink-0" />}
          <div>
            <p className="font-medium">
              {replyWindowExpired
                ? isWhatsApp ? t("inbox.windowTemplateRequired") : tf("inbox.windowExpired", { platform: platformName })
                : replyWindowExtended
                  ? tf("inbox.windowExtended", { platform: platformName })
                  : tf("inbox.windowOpen", { platform: platformName })}
            </p>
            <p className="mt-0.5 leading-relaxed opacity-85">
              {replyWindowKnown
                ? replyWindowExpired
                  ? isWhatsApp
                    ? t("inbox.windowWaitTemplate")
                    : t("inbox.windowWaitReply")
                  : replyWindowExtended
                    ? tf("inbox.windowExtendedUntil", { time: new Date(humanAgentDeadline).toLocaleString('en-US') })
                    : tf("inbox.windowOpenUntil", { time: new Date(replyWindowExpiry).toLocaleString('en-US') })
                : isWhatsApp
                  ? t("inbox.windowNoneTemplate")
                  : t("inbox.windowNone")}
            </p>
          </div>
        </div>
      )}

      {/* Messages — min-h-0 is required: without it the flex item refuses to
          shrink below its content, so tall transcripts push the composer off
          screen and the area itself never scrolls. */}
      <ScrollArea ref={messagesScrollRef} className="min-h-0 flex-1">
        <div className="max-w-3xl mx-auto px-4 py-3 flex flex-col gap-3">
          {msgList.length === 0 ? (
            <p className="text-center text-xs text-muted-foreground py-8">{t("inbox.noMessages")}</p>
          ) : (
            <>
              {hasEarlier && (
                <div className="flex justify-center">
                  <button
                    type="button"
                    onClick={() => void loadEarlier()}
                    disabled={loadingEarlier}
                    className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border bg-card px-3 text-[11px] text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground disabled:opacity-50"
                  >
                    {loadingEarlier ? <Loader2 className="size-3 animate-spin" /> : <ChevronUp className="size-3" />}
                    {loadingEarlier ? t("inbox.loadingMore") : t("inbox.loadEarlier")}
                  </button>
                </div>
              )}
              {groupMessages(msgList).map((group, groupIndex, groups) => {
                const role = group[0].role;
                const isUser = role === "user";
                const prevGroup = groupIndex > 0 ? groups[groupIndex - 1] : undefined;
                const showDay = !prevGroup || dayKey(prevGroup[0].created_at) !== dayKey(group[0].created_at);
                const showUnread = unreadBeforeId != null && group.some((m) => m.message_id === unreadBeforeId);
                return (
                  <React.Fragment key={group[0].message_id}>
                    {showDay && (
                      <div className="flex items-center gap-2">
                        <span className="h-px flex-1 bg-border" />
                        <span className="text-[10px] text-muted-foreground">{fmtDayLabel(group[0].created_at, t("inbox.today"), t("inbox.yesterday"))}</span>
                        <span className="h-px flex-1 bg-border" />
                      </div>
                    )}
                    {showUnread && (
                      <div className="flex items-center gap-2 self-stretch">
                        <span className="h-px flex-1 bg-primary/40" />
                        <span className="text-[10px] font-medium text-primary">{t("inbox.newMessages")}</span>
                        <span className="h-px flex-1 bg-primary/40" />
                      </div>
                    )}
                    <div className={cn("flex flex-col gap-1", isUser ? "items-end" : "items-start")}>
                      <div className={cn("flex items-center gap-1.5 text-[10px] text-muted-foreground", isUser && "flex-row-reverse")}>
                        <span className="font-semibold">{t(roleLabelKey(role))}</span>
                        <span className="tabular-nums">{fmtRelTime(group[0].created_at, t("inbox.timeNow"))}</span>
                      </div>
                      {group.map((m) => (
                        <React.Fragment key={m.message_id}>
                          <div className={cn(
                            "max-w-[85%] rounded-lg px-2.5 py-1.5",
                            m.role === "user" ? "bg-accent text-accent-foreground"
                              : m.role === "agent" ? "bg-warning/15 text-foreground"
                              : m.role === "model" ? "bg-primary/10 text-foreground"
                              : "bg-muted text-muted-foreground",
                          )}>
                            <MessagePayloadPreview messageID={m.message_id} content={m.content} metadata={m.metadata} payload={m.delivery?.payload} />
                            {m.role === "model" && parseSources(m.sources_json).length > 0 && (
                              <div className="mt-1.5 flex flex-wrap gap-1 border-t border-border/60 pt-1.5">
                                {parseSources(m.sources_json).slice(0, 4).map((src) => (
                                  <span key={`${src.doc_id}-${src.title}`} title={(src.content || "").slice(0, 140)} className="inline-flex max-w-full items-center gap-1 rounded border border-border bg-muted/50 px-1.5 py-0.5 text-[10px] text-muted-foreground">
                                    <FileText className="size-2.5 shrink-0" />
                                    <span className="truncate">{src.title}</span>
                                  </span>
                                ))}
                              </div>
                            )}
                            {(m.feedback_rating != null || m.delivery) && (
                              <div className="mt-1 flex items-center gap-2">
                                {m.feedback_rating != null && (
                                  <Badge variant={m.feedback_rating === 1 ? "success" : "destructive"} className="h-3.5 px-1 text-[10px]">
                                    {m.feedback_rating === 1 ? "👍" : "👎"}
                                  </Badge>
                                )}
                                {m.delivery && <MessageDeliveryState delivery={m.delivery} />}
                              </div>
                            )}
                          </div>
                          {/* Translation layer: sits OUTSIDE the customer bubble as a
                              distinct annotation so it reads as an aid, not as the
                              message itself. */}
                          {m.role === "user" && autoTranslate && translations[m.message_id] && (
                            <div className={cn(
                              "max-w-[85%] border-primary/40 py-0.5",
                              isUser ? "border-r-2 pr-2.5 text-right" : "border-l-2 pl-2.5",
                            )}>
                              <p className={cn("mb-0.5 flex items-center gap-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground", isUser && "justify-end")}>
                                <Languages className="size-2.5" />
                                {translateLang(translations[m.message_id].target).label}
                              </p>
                              <p className="whitespace-pre-wrap text-[13px] leading-6 text-foreground">
                                {translations[m.message_id].text}
                              </p>
                            </div>
                          )}
                        </React.Fragment>
                      ))}
                    </div>
                  </React.Fragment>
                );
              })}
            </>
          )}
        </div>
      </ScrollArea>

      {/* Agent reply box — only when the session is in handoff/pending and
          the agent has taken it over (or is the assignee). */}
      {canReply ? (
        <div className="border-t border-border bg-card/40 px-5 py-3">
          <div className="mb-2 flex items-center justify-between gap-3">
            <label className="flex items-center gap-1.5 text-xs font-medium text-foreground">
              <Send className="size-3" /> {t("inbox.replyToCustomer")}
            </label>
            {cannedAll.length > 0 && composerKind !== "template" && (
              <button
                type="button"
                onClick={() => setCannedOpen((v) => !v)}
                className={cn(
                  "inline-flex items-center gap-1 text-[11px] px-1.5 py-0.5 rounded transition-colors",
                  cannedOpen ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground hover:bg-accent/50",
                )}
                title={t("inbox.insertSavedTitle")}
              >
                <Zap className="size-3" />
                {cannedOpen ? t("inbox.hideQuickReplies") : t("inbox.quickReply")}
              </button>
            )}
          </div>

          {supportsRichReplies && (
            <div className="mb-3 flex flex-wrap gap-1" role="tablist" aria-label="Reply format">
              {REPLY_MODES.filter((mode) => (mode.kind !== "template" || isWhatsApp) && (mode.kind !== "buttons" || item.platform !== "instagram")).map((mode) => {
                const Icon = mode.icon;
                const disabled = replyWindowExpired && mode.kind !== "template";
                return (
                  <button
                    key={mode.kind}
                    type="button"
                    role="tab"
                    aria-selected={composerKind === mode.kind}
                    disabled={disabled || sending}
                    onClick={() => setComposerKind(mode.kind)}
                    className={cn(
                      "inline-flex h-7 items-center gap-1.5 border px-2 text-[11px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-45",
                      composerKind === mode.kind
                        ? "border-primary bg-primary text-primary-foreground"
                        : "border-border bg-background text-muted-foreground hover:border-primary/40 hover:text-foreground",
                    )}
                  >
                    <Icon className="size-3" />
                    {t(mode.labelKey)}
                  </button>
                );
              })}
            </div>
          )}

          {cannedOpen && (
            <div className="mb-2 rounded-md border border-border bg-card">
              <div className="p-2 border-b border-border">
                <Input
                  value={cannedQuery}
                  onChange={(e) => setCannedQuery(e.target.value)}
                  placeholder={tf("inbox.cannedSearch", { n: cannedAll.length })}
                  className="h-7 text-xs"
                />
              </div>
              <ScrollArea className="max-h-44">
                <div className="p-1 space-y-0.5">
                  {filteredCanned.length === 0 ? (
                    <p className="text-center text-xs text-muted-foreground py-4">{t("inbox.cannedEmpty")}</p>
                  ) : filteredCanned.slice(0, 30).map((c) => (
                    <button
                      key={c.id}
                      type="button"
                      onClick={() => insertCanned(c.body)}
                      className="w-full text-left rounded px-2 py-1.5 hover:bg-accent transition-colors"
                    >
                      <div className="flex items-center gap-1.5 mb-0.5">
                        <span className="text-xs font-medium truncate flex-1">{c.title}</span>
                        {c.category && (
                          <Badge variant="outline" className="h-3.5 px-1 text-[10px]">{c.category}</Badge>
                        )}
                      </div>
                      <p className="text-xs text-muted-foreground line-clamp-2">{c.body}</p>
                    </button>
                  ))}
                </div>
              </ScrollArea>
            </div>
          )}

          {composerKind === "media" && (
            <div className="mb-3 grid gap-2 sm:grid-cols-[9rem_minmax(0,1fr)]">
              <Select value={mediaType} onValueChange={(value) => setMediaType(value as NonNullable<PlatformMessagePayload["media_type"]>)}>
                <SelectTrigger className="h-9 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="image">{t("inbox.mediaImage")}</SelectItem>
                  {item.platform !== "instagram" && <SelectItem value="document">{t("inbox.mediaDocument")}</SelectItem>}
                  {item.platform !== "instagram" && <SelectItem value="audio">{t("inbox.mediaAudio")}</SelectItem>}
                  <SelectItem value="video">{t("inbox.mediaVideo")}</SelectItem>
                </SelectContent>
              </Select>
              <Input
                value={mediaURL}
                onChange={(event) => setMediaURL(event.target.value)}
                placeholder={t("inbox.mediaUrlPlaceholder")}
                disabled={sending || isReplyBlocked}
                className="h-9 text-xs"
              />
            </div>
          )}

          {composerKind === "buttons" && (
            <div className="mb-3 space-y-2 border-l-2 border-primary/30 pl-3">
              <p className="text-[11px] text-muted-foreground">{tf("inbox.buttonsHint", { max: buttonLimits.maxButtons, chars: buttonLimits.maxTitleRunes, bytes: buttonLimits.maxPayloadBytes })}</p>
              {buttons.map((button, index) => (
                <div key={index} className="flex gap-2">
                  <Input
                    value={button.title}
                    onChange={(event) => setButtons((current) => current.map((entry, entryIndex) => entryIndex === index ? { ...entry, title: clampRunes(event.target.value, buttonLimits.maxTitleRunes) } : entry))}
                    placeholder={tf("inbox.buttonLabelPlaceholder", { used: button.title.length, max: buttonLimits.maxTitleRunes })}
                    disabled={sending || isReplyBlocked}
                    className="h-8 text-xs"
                  />
                  <Input
                    value={button.payload}
                    onChange={(event) => setButtons((current) => current.map((entry, entryIndex) => entryIndex === index ? { ...entry, payload: clampUTF8(event.target.value, buttonLimits.maxPayloadBytes) } : entry))}
                    placeholder={tf("inbox.buttonValuePlaceholder", { used: utf8Bytes(button.payload), max: buttonLimits.maxPayloadBytes })}
                    disabled={sending || isReplyBlocked}
                    className="h-8 text-xs"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={sending || isReplyBlocked || buttons.length === 1}
                    onClick={() => setButtons((current) => current.filter((_, entryIndex) => entryIndex !== index))}
                    className="size-8 shrink-0 text-muted-foreground hover:text-destructive"
                    title={t("inbox.removeButton")}
                    aria-label={t("inbox.removeButton")}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              ))}
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={sending || isReplyBlocked || buttons.length >= buttonLimits.maxButtons}
                onClick={() => setButtons((current) => [...current, { title: "", payload: "" }])}
                className="h-7 gap-1 text-[11px]"
              >
                <Plus className="size-3" /> {t("inbox.addChoice")}
              </Button>
            </div>
          )}

          {composerKind === "template" && (
            <div className="mb-3 space-y-2 border-l-2 border-success/40 pl-3">
              <p className="text-[11px] text-muted-foreground">{t("inbox.templateHint")}</p>
              <Select
                value={selectedTemplate ? whatsAppTemplateKey(selectedTemplate) : undefined}
                onValueChange={(value) => {
                  const selected = whatsAppTemplates.find((template) => whatsAppTemplateKey(template) === value);
                  if (!selected) return;
                  setTemplateName(selected.name);
                  setTemplateLanguage(selected.language);
                  setTemplateParameters("");
                }}
                disabled={sending || isLoadingWhatsAppTemplates || whatsAppTemplates.length === 0}
              >
                <SelectTrigger className="h-8 text-xs"><SelectValue placeholder={isLoadingWhatsAppTemplates ? t("inbox.templateLoading") : t("inbox.templateSelect")} /></SelectTrigger>
                <SelectContent>
                  {whatsAppTemplates.map((template) => (
                    <SelectItem key={whatsAppTemplateKey(template)} value={whatsAppTemplateKey(template)}>
                      {template.name} · {template.language}{template.category ? ` · ${template.category}` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {whatsAppTemplateError ? (
                <p className="text-[11px] text-destructive">{t("inbox.templateError")}</p>
              ) : !isLoadingWhatsAppTemplates && whatsAppTemplates.length === 0 ? (
                <p className="text-[11px] text-warning">{t("inbox.templateNone")}</p>
              ) : selectedTemplate ? (
                <div className="space-y-2 rounded border border-border bg-muted/30 px-2.5 py-2">
                  {selectedTemplate.body_preview && <p className="text-[11px] text-muted-foreground line-clamp-2">{selectedTemplate.body_preview}</p>}
                  {selectedTemplate.body_parameter_count > 0 ? (
                    <Input
                      value={templateParameters}
                      onChange={(event) => setTemplateParameters(event.target.value)}
                      placeholder={tf("inbox.templateParamsPlaceholder", { n: selectedTemplate.body_parameter_count })}
                      disabled={sending}
                      className="h-8 text-xs"
                    />
                  ) : (
                    <p className="text-[11px] text-success">{t("inbox.templateNoVars")}</p>
                  )}
                  {selectedTemplate.body_parameter_count > 0 && templateBodyParams.length !== selectedTemplate.body_parameter_count && (
                    <p className="text-[11px] text-warning">{tf("inbox.templateParamsMissing", { need: selectedTemplate.body_parameter_count, have: templateBodyParams.length })}</p>
                  )}
                </div>
              ) : null}
            </div>
          )}

          {composerKind === "template" && (
            <p className="mb-2 text-[11px] text-muted-foreground">{t("inbox.templateBodyNote")}</p>
          )}

          <div className="flex gap-2">
            <Textarea
              value={draft}
              onChange={(e) => onDraftChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  handleSendReply();
                }
              }}
              placeholder={t("inbox.replyPlaceholder")}
              rows={2}
              disabled={sending || isReplyBlocked || composerKind === "template"}
              className="text-xs resize-none flex-1"
            />
            <TranslateLangMenu
              current={replyLang}
              onPick={(code) => { void handleTranslateDraft(code); }}
              label={t("inbox.translatePick")}
              triggerTitle={t("inbox.translateDraftTitle")}
              disabled={translatingDraft || !draft.trim() || composerKind === "template"}
              triggerClassName={buttonVariants({ variant: "outline", size: "lg", className: "self-end text-xs" })}
            >
              {translatingDraft ? <Loader2 className="size-3 animate-spin" /> : <Languages className="size-3" />}
              {translateLang(replyLang).label}
              <ChevronDown className="size-3 opacity-60" />
            </TranslateLangMenu>
            <Button
              size="sm"
              onClick={handleSendReply}
              disabled={!canSubmitReply}
              className="h-9 text-xs gap-1.5 self-end"
            >
              {sending ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />}
              {composerKind === "template" ? t("inbox.sendTemplate") : t("inbox.send")}
            </Button>
          </div>
        </div>
      ) : null}

      {/* Internal notes — collapsed by default to keep more transcript visible */}
      <div className="px-5 py-2 border-t border-border">
        <button
          type="button"
          onClick={() => setNotesOpen((v) => !v)}
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          <FileText className="size-3" />
          {t("inbox.notesLabel")}
          {notesOpen ? <ChevronUp className="size-3" /> : <ChevronDown className="size-3" />}
        </button>
        {notesOpen && (
          <Textarea
            value={notes}
            onChange={(e) => onNotesChange(e.target.value)}
            placeholder={t("inbox.notesPlaceholder")}
            rows={2}
            className="text-xs resize-none mt-1.5"
          />
        )}
      </div>
      </div>

      {panelOpen && (
        <AiSidePanel
          item={item}
          lastUserMessageId={lastUserMessageId}
          summaryData={summaryData?.summary ?? null}
          summaryLoading={summaryLoading}
          onRegenSummary={() => {
            summaryForceRef.current = true;
            setSummaryRefresh((v) => v + 1);
          }}
          onUseSuggestion={(text) => {
            setComposerKind("text");
            onDraftChange(draft.trim() ? `${draft}\n\n${text}` : text);
          }}
          onClose={() => setPanelOpen(false)}
        />
      )}
    </div>
  );
}

// ============================================
// TagsBar — inline tag editor for conversation triage
// ============================================

/** Compact relative time for list rows: now / 5m / 2h / 3d / 9/1. */
function fmtRelTime(iso: string, nowLabel: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diffMs = Date.now() - then;
  const mins = Math.floor(diffMs / 60_000);
  if (mins < 1) return nowLabel;
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  const d = new Date(iso);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

/** Group consecutive same-role messages so repeat senders render one label. */
function groupMessages(list: ChatMessageItem[]): ChatMessageItem[][] {
  const groups: ChatMessageItem[][] = [];
  for (const m of list) {
    const last = groups[groups.length - 1];
    if (last && last[0].role === m.role) last.push(m);
    else groups.push([m]);
  }
  return groups;
}

function roleLabelKey(role: ChatMessageItem["role"]): string {
  return role === "user"
    ? "inbox.roleUser"
    : role === "model"
      ? "inbox.roleModel"
      : role === "agent"
        ? "inbox.roleAgent"
        : "inbox.roleSystem";
}

// ============================================
// Per-browser UX state (localStorage): drafts, read markers, panel visibility
// ============================================

const LS_PANEL = "khmer-inbox-panel";
const LS_DRAFTS = "khmer-inbox-drafts";
const LS_LASTSEEN = "khmer-inbox-lastseen";
const LS_LASTSEENMSG = "khmer-inbox-lastseen-msg";

function lsGetJSON<T>(key: string, fallback: T): T {
  if (typeof window === "undefined") return fallback;
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function lsSetJSON(key: string, value: unknown) {
  try { window.localStorage.setItem(key, JSON.stringify(value)); } catch { /* storage unavailable */ }
}

/** Local calendar-day key used to group messages by day. */
function dayKey(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

function fmtDayLabel(iso: string, todayLabel: string, yesterdayLabel: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  if (dayKey(iso) === dayKey(now.toISOString())) return todayLabel;
  if (dayKey(iso) === dayKey(new Date(now.getTime() - 86_400_000).toISOString())) return yesterdayLabel;
  return `${d.getFullYear() === now.getFullYear() ? "" : `${d.getFullYear()}/`}${d.getMonth() + 1}/${d.getDate()}`;
}

function parseSources(raw?: string | null): RAGSource[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as RAGSource[]) : [];
  } catch {
    return [];
  }
}

function parseInboundPlatformMedia(metadata?: string | Record<string, unknown> | null): InboundPlatformMedia | null {
  if (!metadata) return null;
  try {
    // metadata can arrive as a serialized string (older rows) or as the
    // parsed JSON object sent by the backend JSONB column.
    const parsed =
      typeof metadata === "string"
        ? (JSON.parse(metadata) as { platform_media?: InboundPlatformMedia })
        : (metadata as { platform_media?: InboundPlatformMedia });
    return parsed.platform_media?.kind ? parsed.platform_media : null;
  } catch {
    return null;
  }
}

function InboundPlatformMediaPreview({ messageID, media }: { messageID: number; media: InboundPlatformMedia }) {
  const { t, tf } = useI18n();
  const [previewURL, setPreviewURL] = React.useState<string | null>(null);
  const [audioURL, setAudioURL] = React.useState<string | null>(null);
  const [loadingPreview, setLoadingPreview] = React.useState(false);
  const isImage = media.kind === "image" || media.kind === "photo";
  const isAudio = media.kind === "audio" || media.kind === "voice";
  const MediaIcon = isImage ? ImageIcon : isAudio ? Volume2 : FileText;
  const status = media.processing_status === "unavailable" ? t("inbox.mediaUnavailable") : media.processing_error ? t("inbox.mediaStoredWarning") : t("inbox.mediaReady");

  // Auto-load inline previews for images & voice notes (single signed-URL
  // fetch per message; documents/videos stay click-to-open).
  const autoLoadRef = React.useRef(false);
  React.useEffect(() => {
    if (autoLoadRef.current) return;
    if (media.processing_status === "unavailable") return;
    if (!isImage && !isAudio) return;
    autoLoadRef.current = true;
    void (async () => {
      try {
        const response = await getInboundPlatformMediaURL(messageID);
        if (isImage) setPreviewURL(response.url);
        else if (isAudio) setAudioURL(response.url);
      } catch { /* keep the manual preview button */ }
    })();
  }, [isImage, isAudio, media.processing_status, messageID]);

  const preview = async () => {
    setLoadingPreview(true);
    try {
      const response = await getInboundPlatformMediaURL(messageID);
      if (isImage) {
        setPreviewURL(response.url);
      } else if (isAudio) {
        // Inline player — no new tab, no forced download.
        setAudioURL(response.url);
      } else {
        window.open(response.url, "_blank", "noopener,noreferrer");
      }
    } catch (err) {
      toast.error((err as Error).message || t("inbox.couldNotOpenAttachment"));
    } finally {
      setLoadingPreview(false);
    }
  };

  return (
    <div className="mt-2 border border-border/80 bg-background/45 p-2.5 text-xs">
      <div className="flex min-w-0 items-center gap-2">
        <MediaIcon className="size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <p className="truncate font-medium text-foreground">{media.filename || (isAudio ? t("inbox.voiceMessage") : tf("inbox.attachment", { kind: media.kind }))}</p>
          <p className="mt-0.5 truncate text-[10px] text-muted-foreground">{media.mime_type || media.kind} · {status}</p>
        </div>
        {media.processing_status !== "unavailable" && (
          <Button type="button" size="sm" variant="outline" onClick={() => void preview()} disabled={loadingPreview} className="h-7 shrink-0 gap-1 px-2 text-[11px]">
            {loadingPreview ? <Loader2 className="size-3 animate-spin" /> : <Paperclip className="size-3" />}
            {t("inbox.preview")}
          </Button>
        )}
      </div>
      {audioURL && isAudio && (
        <audio controls src={audioURL} preload="none" className="mt-2 h-9 w-full" />
      )}
      {previewURL && isImage && (
        <a href={previewURL} target="_blank" rel="noreferrer" className="mt-2 block overflow-hidden border border-border bg-muted/30">
          {/* Signed R2 hosts are runtime-only and cannot be safely allowlisted for next/image. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src={previewURL} alt={media.filename || tf("inbox.attachment", { kind: media.kind })} className="max-h-64 w-full object-contain" />
        </a>
      )}
      {media.processing_error && <p className="mt-2 text-[11px] text-warning">{media.processing_error}</p>}
      {media.extracted_text && (
        <details className="mt-2 border-t border-border pt-2 text-[11px] text-muted-foreground">
          <summary className="cursor-pointer font-medium text-foreground">{t("inbox.extractedText")}</summary>
          <p className="mt-1 whitespace-pre-wrap leading-relaxed">{media.extracted_text}</p>
        </details>
      )}
    </div>
  );
}

function MessagePayloadPreview({ messageID, content, metadata, payload }: { messageID: number; content: string; metadata?: string | Record<string, unknown> | null; payload?: PlatformMessagePayload }) {
  const { t, tf } = useI18n();
  const mediaLabel = payload?.media_type ? `${payload.media_type.slice(0, 1).toUpperCase()}${payload.media_type.slice(1)}` : t("inbox.modeMedia");
  const inboundMedia = parseInboundPlatformMedia(metadata);
  const isStoredPreview = payload && (
    content === "[Buttons sent]"
    || content === `[${mediaLabel} sent]`
    || content === `[WhatsApp template: ${payload.template_name}]`
  );

  return (
    <>
      {!isStoredPreview && content && <p className="text-sm leading-normal whitespace-pre-wrap">{content}</p>}
      {inboundMedia && <InboundPlatformMediaPreview messageID={messageID} media={inboundMedia} />}
      {payload?.kind === "media" && payload.media_url && (
        <div className="mt-1.5">
          {payload.media_type === "image" ? (
            <a href={payload.media_url} target="_blank" rel="noreferrer" className="block max-w-56 overflow-hidden rounded-md border border-border bg-muted/30">
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src={payload.media_url} alt={mediaLabel} className="max-h-44 w-full object-contain" />
            </a>
          ) : (
            <a href={payload.media_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline">
              <Paperclip className="size-3.5" />
              {tf("inbox.openAttachment", { kind: mediaLabel.toLowerCase() })}
            </a>
          )}
        </div>
      )}
      {payload?.kind === "buttons" && (
        <div className="mt-2 flex flex-wrap gap-1.5" aria-label="Customer reply choices">
          {payload.buttons?.map((button) => (
            <span key={`${button.payload}-${button.title}`} className="border border-primary/30 bg-primary/5 px-2 py-1 text-[11px] font-medium text-primary">
              {button.title}
            </span>
          ))}
        </div>
      )}
      {payload?.kind === "template" && (
        <div className="mt-2 border-l-2 border-success/50 pl-2 text-[11px] text-muted-foreground">
          <span className="font-medium text-foreground">{t("inbox.whatsappTemplate")}</span> {payload.template_name} ({payload.template_language})
          {payload.template_body_params?.length ? ` - ${payload.template_body_params.join(", ")}` : ""}
        </div>
      )}
    </>
  );
}

function MessageDeliveryState({ delivery }: { delivery: NonNullable<ChatMessageItem["delivery"]> }) {
  const { t } = useI18n();
  const isQueued = !(delivery.status === "cancelled" || delivery.status === "failed"
    || delivery.provider_status === "failed" || delivery.provider_status === "read"
    || delivery.provider_status === "delivered" || delivery.status === "sent");
  const state = delivery.status === "cancelled"
    ? { label: t("inbox.deliveryNotSent"), detail: delivery.last_error || t("inbox.deliveryNotSentDetail"), icon: CircleAlert, className: "text-warning" }
    : delivery.status === "failed" || delivery.provider_status === "failed"
    ? { label: t("inbox.deliveryFailed"), detail: delivery.last_error || t("inbox.deliveryFailedDetail"), icon: CircleAlert, className: "text-destructive" }
    : delivery.provider_status === "read"
      ? { label: t("inbox.deliveryRead"), detail: delivery.read_at ? new Date(delivery.read_at).toLocaleString('en-US') : t("inbox.deliveryReadFallback"), icon: CheckCheck, className: "text-info" }
      : delivery.provider_status === "delivered"
        ? { label: t("inbox.deliveryDelivered"), detail: delivery.delivered_at ? new Date(delivery.delivered_at).toLocaleString('en-US') : t("inbox.deliveryDeliveredFallback"), icon: CheckCheck, className: "text-success" }
        : delivery.status === "sent"
          ? { label: t("inbox.deliveryAccepted"), detail: delivery.sent_at ? new Date(delivery.sent_at).toLocaleString('en-US') : t("inbox.deliveryAcceptedFallback"), icon: CheckCircle2, className: "text-muted-foreground" }
          : { label: t("inbox.deliveryQueued"), detail: t("inbox.deliveryQueuedDetail"), icon: Loader2, className: "text-muted-foreground" };
  const Icon = state.icon;
  return (
    <span title={state.detail} className={cn("flex w-fit items-center gap-1 text-[10px] font-medium", state.className)}>
      <Icon className={cn("size-3", isQueued && "animate-spin")} />
      {state.label}
    </span>
  );
}

const COMMON_TAGS = ["refund", "vip", "complaint", "faq", "billing", "shipping", "escalated", "resolved"];

function TagsBar({ sessionId, initial, onMutate }: {
  sessionId: string;
  initial: string[];
  onMutate: () => void;
}) {
  const { t } = useI18n();
  const [tags, setTags] = React.useState<string[]>(initial);
  const [input, setInput] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  const addTag = async (tag: string) => {
    const normalized = tag.trim().toLowerCase();
    if (!normalized || tags.includes(normalized)) return;
    const next = [...tags, normalized];
    setTags(next);
    setInput("");
    setSaving(true);
    try {
      await setSessionTags(sessionId, next);
      onMutate();
    } catch (e) {
      setTags(tags); // rollback
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const removeTag = async (tag: string) => {
    const next = tags.filter((existing) => existing !== tag);
    setTags(next);
    setSaving(true);
    try {
      await setSessionTags(sessionId, next);
      onMutate();
    } catch (e) {
      setTags(tags);
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const suggestions = COMMON_TAGS.filter((candidate) => !tags.includes(candidate)).slice(0, 5);

  return (
    <div className="mx-5 mt-2 flex flex-wrap items-center gap-1">
      {tags.map((tag) => (
        <Badge key={tag} variant="secondary" className="gap-1 h-5 text-xs pr-1">
          {tag}
          <button
            onClick={() => removeTag(tag)}
            className="text-muted-foreground hover:text-destructive ml-0.5"
            title={t("inbox.tagRemove")}
            disabled={saving}
          >
            <XCircle className="size-3" />
          </button>
        </Badge>
      ))}
      <input
        value={input}
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && input.trim()) {
            e.preventDefault();
            void addTag(input);
          }
        }}
        placeholder={t("inbox.tagPlaceholder")}
        className="bg-transparent text-xs border-b border-border focus:border-primary focus:outline-none w-20 px-0.5 py-0.5"
        disabled={saving}
      />
      {suggestions.length > 0 && (
        <div className="flex items-center gap-0.5 ml-1">
          {suggestions.map((s) => (
            <button
              key={s}
              onClick={() => addTag(s)}
              className="text-xs text-muted-foreground hover:text-primary px-1 py-0.5 rounded hover:bg-accent transition-colors"
              disabled={saving}
            >
              + {s}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ============================================
// AiSidePanel — right-hand AI insights:
// summary / suggested replies / customer 360.
// Assembles existing backend endpoints:
//   GET  /inbox/sessions/{id}/summary
//   POST /inbox/sessions/{id}/copilot/suggest
//   GET  /customers/{id}
// ============================================

function AiSidePanel({ item, lastUserMessageId, summaryData, summaryLoading, onRegenSummary, onUseSuggestion, onClose }: {
  item: InboxItem;
  lastUserMessageId: number;
  summaryData: string | null;
  summaryLoading: boolean;
  onRegenSummary: () => void;
  onUseSuggestion: (text: string) => void;
  onClose: () => void;
}) {
  const { t, tf } = useI18n();

  // Suggested replies (copilot) — generated on demand, not auto-fetched.
  const [suggestions, setSuggestions] = React.useState<string[]>([]);
  const [grounded, setGrounded] = React.useState(true);
  const [suggestLoading, setSuggestLoading] = React.useState(false);
  const [suggestError, setSuggestError] = React.useState("");

  const generate = async () => {
    setSuggestLoading(true);
    setSuggestError("");
    try {
      const res = await copilotSuggest(item.session_id, 3);
      setSuggestions(res.suggestions ?? []);
      setGrounded(res.grounded !== false);
    } catch (e) {
      setSuggestError((e as Error).message || t("inbox.suggestFailed"));
    } finally {
      setSuggestLoading(false);
    }
  };

  // Auto-refresh suggestions when the conversation opens and whenever a new
  // customer message lands (debounced; the manual button still works).
  const generateRef = React.useRef(generate);
  React.useEffect(() => { generateRef.current = generate; });
  React.useEffect(() => {
    if (lastUserMessageId <= 0) return;
    const timer = window.setTimeout(() => { void generateRef.current(); }, 4_000);
    return () => window.clearTimeout(timer);
  }, [lastUserMessageId]);

  // Customer resolution: the inbox item carries platform_user_id; map it to a
  // tenant-scoped customer_profiles row, then load the 360 view by profile_id.
  const { data: profiles } = useSWR("customer-profiles", () => listCustomers());
  const profile: CustomerProfile | undefined = React.useMemo(
    () => (profiles ?? []).find((p) => p.platform_user_id && p.platform_user_id === item.platform_user_id),
    [profiles, item.platform_user_id],
  );
  const { data: c360, mutate: mutateC360 } = useSWR(
    profile ? `customer360-${profile.profile_id}` : null,
    () => getCustomer360(profile!.profile_id),
  );

  // Inline customer-notes editing.
  const [notesEditing, setNotesEditing] = React.useState(false);
  const [notesDraft, setNotesDraft] = React.useState("");
  const [notesSaving, setNotesSaving] = React.useState(false);

  const saveNotes = async () => {
    if (!profile) return;
    setNotesSaving(true);
    try {
      await updateCustomerNotes(profile.profile_id, notesDraft);
      await mutateC360();
      setNotesEditing(false);
      toast.success(t("inbox.notesSaved"));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setNotesSaving(false);
    }
  };

  return (
    <aside className="hidden lg:flex w-80 shrink-0 flex-col border-l border-border bg-gradient-to-b from-muted/40 to-transparent min-h-0">
      <div className="px-3.5 py-3 border-b border-border flex items-center justify-between gap-2 bg-card/60">
        <p className="flex items-center gap-2 text-[13px] font-semibold tracking-tight">
          <span className="flex size-5 items-center justify-center rounded-md bg-primary/10">
            <Sparkles className="size-3 text-primary" />
          </span>
          {t("inbox.aiPanel")}
        </p>
        <Button variant="ghost" size="icon" className="size-6 text-muted-foreground hover:text-foreground" onClick={onClose} title={t("inbox.aiPanelHide")}>
          <ChevronRight className="size-4" />
        </Button>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto px-3 pb-4">
        {/* Summary */}
        <section className="pt-3 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <p className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
              <Sparkles className="size-3" /> {t("inbox.summary")}
            </p>
            {summaryData && (
              <button
                type="button"
                onClick={onRegenSummary}
                disabled={summaryLoading}
                title={t("inbox.summaryRegenTitle")}
                className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50"
              >
                <RefreshCw className={cn("size-3", summaryLoading && "animate-spin")} /> {t("inbox.panelRegen")}
              </button>
            )}
          </div>
          {summaryLoading ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground py-3">
              <Loader2 className="size-3.5 animate-spin" /> {t("inbox.summaryLoading")}
            </div>
          ) : summaryData ? (
            <div className="rounded-lg border border-info/25 bg-info/5 p-3">
              <p className="text-xs leading-relaxed whitespace-pre-wrap text-foreground">{summaryData}</p>
            </div>
          ) : (
            <p className="text-[11px] text-muted-foreground py-1">{t("inbox.summaryNone")}</p>
          )}
        </section>

        {/* Suggested replies */}
        <section className="pt-4 mt-4 border-t border-border/70 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <p className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
              <Zap className="size-3" /> {t("inbox.panelSuggest")}
            </p>
            <button
              type="button"
              onClick={() => void generate()}
              disabled={suggestLoading}
              className="inline-flex h-6 items-center gap-1 rounded border border-border bg-background px-2 text-[11px] text-foreground transition-colors hover:border-primary/40 hover:text-primary disabled:opacity-50"
            >
              {suggestLoading ? <Loader2 className="size-3 animate-spin" /> : <Sparkles className="size-3 text-primary" />}
              {t("inbox.suggestGenerate")}
            </button>
          </div>
          {!grounded && suggestions.length > 0 && (
            <p className="rounded-lg border border-warning/40 bg-warning/10 px-2.5 py-1.5 text-[11px] leading-relaxed text-warning">{t("inbox.suggestUngrounded")}</p>
          )}
          {suggestions.map((s, i) => (
            <div key={i} className="rounded-lg border border-border bg-background/70 p-2.5 transition-colors hover:border-primary/40">
              <p className="text-xs leading-relaxed whitespace-pre-wrap text-foreground">{s}</p>
              <div className="mt-1.5 flex justify-end">
                <Button variant="ghost" size="sm" className="h-6 gap-1 px-2 text-[11px] text-primary hover:bg-primary/10" onClick={() => onUseSuggestion(s)}>
                  <Zap className="size-3" /> {t("inbox.useSuggestion")}
                </Button>
              </div>
            </div>
          ))}
          {suggestError && <p className="text-[11px] leading-relaxed text-destructive">{suggestError}</p>}
          {suggestions.length === 0 && !suggestLoading && !suggestError && (
            <p className="text-[11px] leading-relaxed text-muted-foreground">{t("inbox.suggestEmpty")}</p>
          )}
        </section>

        {/* Customer 360 (read-only) */}
        <section className="pt-4 mt-4 border-t border-border/70 space-y-2">
          <p className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
            <UserCircle2 className="size-3" /> {t("inbox.panelCustomer")}
          </p>
          {!item.platform_user_id ? (
            <p className="text-[11px] text-muted-foreground">{t("inbox.customerEmpty")}</p>
          ) : profiles === undefined ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground py-2">
              <Loader2 className="size-3.5 animate-spin" /> {t("inbox.summaryLoading")}
            </div>
          ) : !profile ? (
            <p className="text-[11px] text-muted-foreground">{t("inbox.customerEmpty")}</p>
          ) : (
            <div className="space-y-3">
              <div className="flex items-center gap-2.5 rounded-lg border border-border bg-background/70 p-2.5">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-primary/10 text-sm font-semibold text-primary">
                  {(c360?.profile?.display_name || profile.display_name || "?").trim().charAt(0).toUpperCase()}
                </span>
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold text-foreground">{c360?.profile?.display_name || profile.display_name}</p>
                  <p className="truncate text-[11px] text-muted-foreground">{PLATFORM_LABELS[profile.platform] || profile.platform} · {profile.platform_user_id}</p>
                </div>
              </div>

              {((item.sentiment && item.sentiment !== "neutral") || item.intent) && (
                <div className="flex flex-wrap gap-1">
                  {item.sentiment === "negative" && (
                    <Badge variant="destructive" className="h-4 gap-0.5 px-1.5 text-[10px]"><AlertTriangle className="size-2.5" /> {t("inbox.angry")}</Badge>
                  )}
                  {item.sentiment === "positive" && (
                    <Badge variant="success" className="h-4 px-1.5 text-[10px]">😊</Badge>
                  )}
                  {item.intent && <Badge variant="outline" className="h-4 px-1.5 text-[10px]">{item.intent}</Badge>}
                </div>
              )}

              {(c360?.profile?.phone || c360?.profile?.email) && (
                <div className="space-y-1 rounded-lg border border-border bg-background/70 px-2.5 py-2">
                  {c360?.profile?.phone && (
                    <p className="flex items-center gap-2 text-[11px] text-muted-foreground"><Phone className="size-3 shrink-0" /><span className="truncate">{c360.profile.phone}</span></p>
                  )}
                  {c360?.profile?.email && (
                    <p className="flex items-center gap-2 text-[11px] text-muted-foreground"><Mail className="size-3 shrink-0" /><span className="truncate">{c360.profile.email}</span></p>
                  )}
                </div>
              )}

              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                <span>{tf("inbox.customerStats", { sessions: profile.total_sessions, messages: profile.total_messages })}</span>
                {c360?.profile?.last_seen_at && (
                  <span>· {t("inbox.customerLastSeen")} {fmtRelTime(c360.profile.last_seen_at, t("inbox.timeNow"))}</span>
                )}
              </div>

              {(c360?.profile?.tags ?? profile.tags ?? []).length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {(c360?.profile?.tags ?? profile.tags ?? []).map((tag) => (
                    <Badge key={tag} variant="secondary" className="h-4 px-1.5 text-[10px]">{tag}</Badge>
                  ))}
                </div>
              )}

              {notesEditing ? (
                <div className="rounded-lg border border-border bg-muted/30 p-2.5">
                  <p className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">{t("inbox.notesLabel")}</p>
                  <Textarea value={notesDraft} onChange={(e) => setNotesDraft(e.target.value)} rows={3} className="bg-background text-[11px] resize-none" />
                  <div className="mt-1.5 flex justify-end gap-1.5">
                    <Button variant="ghost" size="sm" className="h-6 px-2 text-[11px]" onClick={() => setNotesEditing(false)}>{t("inbox.panelCancel")}</Button>
                    <Button size="sm" className="h-6 px-2 text-[11px]" disabled={notesSaving} onClick={() => void saveNotes()}>
                      {notesSaving ? <Loader2 className="size-3 animate-spin" /> : t("inbox.notesSave")}
                    </Button>
                  </div>
                </div>
              ) : c360?.profile?.notes ? (
                <div className="rounded-lg border border-border bg-muted/30 p-2.5">
                  <div className="mb-1 flex items-center justify-between gap-2">
                    <p className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">{t("inbox.notesLabel")}</p>
                    <button type="button" onClick={() => { setNotesDraft(c360.profile?.notes ?? ""); setNotesEditing(true); }} className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground">
                      <Pencil className="size-3" /> {t("inbox.notesEdit")}
                    </button>
                  </div>
                  <p className="whitespace-pre-wrap text-[11px] leading-relaxed text-muted-foreground">{c360.profile.notes}</p>
                </div>
              ) : (
                <button type="button" onClick={() => { setNotesDraft(""); setNotesEditing(true); }} className="inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground">
                  <Pencil className="size-3" /> {t("inbox.notesAdd")}
                </button>
              )}

              {(c360?.sessions?.length ?? 0) > 0 && (
                <div>
                  <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">{t("inbox.customerSessions")}</p>
                  <div className="space-y-1">
                    {c360!.sessions.slice(0, 5).map((s) => (
                      <div key={s.session_id} className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background/70 px-2.5 py-1.5 transition-colors hover:border-primary/30">
                        <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">{s.title || s.session_id.slice(0, 8)}</span>
                        <span className="text-[10px] tabular-nums text-muted-foreground">{fmtRelTime(s.created_at, t("inbox.timeNow"))}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </section>
      </div>
    </aside>
  );
}
