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
} from "@/lib/api";
import { PageHeader } from "@/components/page-header";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
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
	AlertTriangle, RefreshCw,
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

type PlatformFilter = "all" | "telegram" | "meta" | "instagram" | "whatsapp" | "line" | "web";
type PlatformCategory = Exclude<PlatformFilter, "all">;
type ComposerKind = PlatformMessageKind;

const PLATFORM_GROUPS: { value: PlatformCategory; label: string; filterLabel: string; dotClass: string }[] = [
  { value: "telegram", label: "Telegram", filterLabel: "Telegram", dotClass: "bg-sky-400" },
  { value: "meta", label: "Messenger", filterLabel: "Messenger", dotClass: "bg-indigo-400" },
  { value: "instagram", label: "Instagram", filterLabel: "Instagram", dotClass: "bg-pink-400" },
  { value: "whatsapp", label: "WhatsApp", filterLabel: "WhatsApp", dotClass: "bg-emerald-400" },
  { value: "line", label: "LINE", filterLabel: "LINE", dotClass: "bg-lime-500" },
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
  web: "Website",
};

const CARE_WINDOW_PLATFORMS = new Set(["meta", "instagram", "whatsapp"]);

const REPLY_MODES = [
  { kind: "text" as const, labelKey: "inbox.modeText", icon: MessageSquare },
  { kind: "media" as const, labelKey: "inbox.modeMedia", icon: ImageIcon },
  { kind: "buttons" as const, labelKey: "inbox.modeButtons", icon: ListChecks },
  { kind: "template" as const, labelKey: "inbox.modeTemplate", icon: FileCode2 },
];

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
  const [activeId, setActiveId] = React.useState<string | null>(null);
	const [notes, setNotes] = React.useState("");
	const [realtimeVersion, setRealtimeVersion] = React.useState(0);
	const [testChatOpen, setTestChatOpen] = React.useState(false);
  const [brokenAvatars, setBrokenAvatars] = React.useState<Set<string>>(new Set());
  const refreshTimerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  const inboxKey = `inbox-${statusFilter}`;
  const { data: inboxData, mutate: mutateInbox } = useSWR(
    inboxKey,
    () => listInbox({ status: statusFilter === "all" ? undefined : statusFilter, pageSize: 100 }),
    // WS 事件驱动是主通道 (收到即刷新); 此轮询仅为 WS 断线时的自愈兜底.
    { refreshInterval: 15_000 },
  );
  const items = (inboxData?.data ?? []).filter((item) => {
    const matchesQuery = !query || (item.title || item.user_display_name || item.last_message || "").toLowerCase().includes(query.toLowerCase());
    return matchesQuery && (platformFilter === "all" || platformCategory(item.platform) === platformFilter);
  });
  const groupedItems = React.useMemo(() => {
    const groups = platformFilter === "all"
      ? PLATFORM_GROUPS
      : PLATFORM_GROUPS.filter((group) => group.value === platformFilter);
    return groups
      .map((group) => ({ ...group, items: items.filter((item) => platformCategory(item.platform) === group.value) }))
      .filter((group) => group.items.length > 0);
  }, [items, platformFilter]);
  const selectedSessionID = activeId ?? searchParams.get("session");
  const active = items.find((item) => item.session_id === selectedSessionID) ?? null;

  const handleRealtimeEvent = React.useCallback((event: { session_id: string }) => {
    if (event.session_id === activeId) {
      setRealtimeVersion((version) => version + 1);
    }
    if (refreshTimerRef.current) clearTimeout(refreshTimerRef.current);
    refreshTimerRef.current = setTimeout(() => { void mutateInbox(); }, 150);
  }, [activeId, mutateInbox]);

  useInboxRealtime(token, handleRealtimeEvent);

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
        <aside className="w-80 shrink-0 flex flex-col border-r border-border bg-card text-card-foreground">
          <div className="space-y-3 border-b border-border p-3">
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
                <p className="text-center text-xs text-muted-foreground py-12">{t("inbox.noConversations")}</p>
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
                        onClick={() => { setActiveId(item.session_id); setNotes(""); }}
                        className={cn(
                          "flex w-full items-start gap-2.5 border px-3 py-2.5 text-left transition-colors",
                          activeId === item.session_id
                            ? "border-primary/40 bg-primary/10"
                            : "border-transparent hover:border-border hover:bg-muted/70",
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
        </aside>

        {/* Right: conversation detail */}
        <section className="flex-1 flex flex-col min-w-0 bg-background text-foreground">
          {active ? (
            <ConversationDetail
              key={active.session_id}
              item={active}
              currentUserId={user?.user_id ?? 0}
              notes={notes}
              onNotesChange={setNotes}
              onMutate={() => { void mutateInbox(); }}
              realtimeVersion={realtimeVersion}
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
  item, currentUserId, notes, onNotesChange, onMutate, realtimeVersion,
}: {
  item: InboxItem;
  currentUserId: number;
  notes: string;
  onNotesChange: (v: string) => void;
  onMutate: () => void;
  realtimeVersion: number;
}) {
  // Transcript: newest 200 on mount, then message_id-cursor deltas driven by
  // realtime events with a 15s fallback poll (WS 断线自愈). The component is
  // keyed by session_id upstream, so switching conversations remounts it.
  const { t, tf } = useI18n();
  const [msgList, setMsgList] = React.useState<ChatMessageItem[]>([]);
  const maxMessageIdRef = React.useRef(0);

  const pullMessages = React.useCallback(async (after: number) => {
    try {
      const rows = await listSessionMessages(item.session_id, 200, after);
      if (rows.length === 0) return;
      maxMessageIdRef.current = rows[rows.length - 1].message_id;
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

  const [reply, setReply] = React.useState("");
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
  const replyWindowExpired = hasCustomerCareWindow && (!replyWindowKnown || currentTime >= replyWindowExpiry);
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
      ? Boolean(reply.trim())
      : composerKind === "media"
        ? isPublicHTTPSURL(mediaURL)
        : composerKind === "buttons"
          ? Boolean(reply.trim()) && buttonsValid
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
      await agentReply(item.session_id, { content: composerKind === "template" ? "" : reply.trim(), payload });
      setReply("");
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
    setReply((prev) => {
      const cur = prev.trim();
      if (!cur) return body;
      // Place the snippet on its own line below the draft.
      return `${cur}\n\n${body}`;
    });
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
    <div className="flex flex-col h-full">
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

      {/* AI-generated summary (lazy) */}
      {summaryData?.summary && (
        <div className="mx-5 mt-3 rounded-md border border-info/30 bg-info/10 px-3 py-2 text-xs flex items-start gap-2">
          <Sparkles className="size-3.5 text-info flex-shrink-0 mt-0.5" />
          <div className="flex-1">
            <p className="text-xs font-semibold uppercase tracking-wide text-info mb-0.5">
              {t("inbox.summary")} {summaryData.cached ? t("inbox.summaryCached") : ""}
            </p>
            <p className="text-foreground leading-relaxed">{summaryData.summary}</p>
          </div>
          <Button
            variant="ghost"
            size="icon"
            className="size-6 shrink-0"
            title={t("inbox.summaryRegenTitle")}
            disabled={summaryLoading}
            onClick={() => {
              summaryForceRef.current = true;
              setSummaryRefresh((v) => v + 1);
            }}
          >
            <RefreshCw className={cn("size-3", summaryLoading && "animate-spin")} />
          </Button>
        </div>
      )}

      {hasCustomerCareWindow && (
        <div className={cn("mx-5 mt-3 flex items-start gap-2 border px-3 py-2 text-xs", replyWindowExpired ? "border-warning/40 bg-warning/10 text-warning" : "border-success/30 bg-success/10 text-foreground")}>
          {replyWindowExpired ? <CircleAlert className="mt-0.5 size-3.5 shrink-0" /> : <Clock3 className="mt-0.5 size-3.5 shrink-0 text-success" />}
          <div>
            <p className="font-medium">
              {replyWindowExpired
                ? isWhatsApp ? t("inbox.windowTemplateRequired") : tf("inbox.windowExpired", { platform: platformName })
                : tf("inbox.windowOpen", { platform: platformName })}
            </p>
            <p className="mt-0.5 leading-relaxed opacity-85">
              {replyWindowKnown
                ? replyWindowExpired
                  ? isWhatsApp
                    ? t("inbox.windowWaitTemplate")
                    : t("inbox.windowWaitReply")
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
        <div className="max-w-3xl mx-auto px-5 py-5 space-y-3">
          {msgList.length === 0 ? (
            <p className="text-center text-xs text-muted-foreground py-8">{t("inbox.noMessages")}</p>
          ) : msgList.map((m) => (
            <div key={m.message_id} className={cn("flex gap-2", m.role === "user" && "flex-row-reverse")}>
              <div className={cn(
                "size-6 rounded-md flex items-center justify-center flex-shrink-0 mt-0.5",
                m.role === "user" ? "bg-accent" : m.role === "agent" ? "bg-warning/20" : "bg-primary/10",
              )}>
                <span className="text-[10px] font-semibold text-muted-foreground">
                  {m.role === "user" ? t("inbox.roleUser") : m.role === "model" ? t("inbox.roleModel") : m.role === "agent" ? t("inbox.roleAgent") : t("inbox.roleSystem")}
                </span>
              </div>
              <div className={cn(
                "max-w-[75%] rounded-md border px-3 py-2",
                m.role === "user" ? "bg-accent border-border text-accent-foreground" : "bg-card border-border text-card-foreground",
              )}>
				<MessagePayloadPreview messageID={m.message_id} content={m.content} metadata={m.metadata} payload={m.delivery?.payload} />
                {m.feedback_rating != null && (
                  <Badge variant={m.feedback_rating === 1 ? "success" : "destructive"} className="h-3.5 px-1 mt-1 text-[10px]">
                    {m.feedback_rating === 1 ? "👍" : "👎"}
                  </Badge>
                )}
                {m.delivery && <MessageDeliveryState delivery={m.delivery} />}
              </div>
            </div>
          ))}
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
              value={reply}
              onChange={(e) => setReply(e.target.value)}
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

      {/* Internal notes */}
      <div className="px-5 py-3 border-t border-border">
        <label className="text-xs text-muted-foreground block mb-1">{t("inbox.notesLabel")}</label>
        <Textarea
          value={notes}
          onChange={(e) => onNotesChange(e.target.value)}
          placeholder={t("inbox.notesPlaceholder")}
          rows={2}
          className="text-xs resize-none"
        />
      </div>
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
      {!isStoredPreview && content && <p className="text-sm leading-relaxed whitespace-pre-wrap">{content}</p>}
      {inboundMedia && <InboundPlatformMediaPreview messageID={messageID} media={inboundMedia} />}
      {payload?.kind === "media" && payload.media_url && (
        <div className="mt-2">
          <a href={payload.media_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline">
            {payload.media_type === "image" ? <ImageIcon className="size-3.5" /> : <Paperclip className="size-3.5" />}
            {tf("inbox.openAttachment", { kind: mediaLabel.toLowerCase() })}
          </a>
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
    <span title={state.detail} className={cn("mt-2 flex w-fit items-center gap-1 text-[10px] font-medium", state.className)}>
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
