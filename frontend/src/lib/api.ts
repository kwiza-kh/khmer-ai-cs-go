import { API_BASE, signalAuthExpired } from "./auth-client";
import { localizeCurrentLang } from "./api-errors";

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export async function apiFetch<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  const token = localStorage.getItem("token");
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(options.headers as Record<string, string>),
  };
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(`${API_BASE}${path}`, { ...options, headers });

  // 204 No Content / 空 body 时直接返回, 否则 res.json() 会抛错.
  if (res.status === 204) {
    return undefined as T;
  }

  const contentType = res.headers.get("content-type") || "";
  let data: unknown = null;
  if (contentType.includes("application/json")) {
    data = await res.json();
  } else {
    // 非 JSON 响应 (HTML 502 网关错误, 纯文本等), 仍要让调用方看到状态码.
    const text = await res.text().catch(() => "");
    throw new ApiError(localizeCurrentLang(text) || localizeCurrentLang("请求失败") + ` (HTTP ${res.status})`, res.status);
  }

  if (!res.ok) {
    const message = localizeCurrentLang((data as { error?: string } | null)?.error || `请求失败 (HTTP ${res.status})`);
    // Token 过期或无效: 清凭据 + 广播登出, AuthGuard 立即跳转登录页.
    if (res.status === 401) {
      signalAuthExpired();
    }
    throw new ApiError(message, res.status);
  }
  return data as T;
}

// ============================================
// 共享响应类型 (与后端 models.*Response 对齐)
// ============================================

export interface UserItem {
  user_id: number;
  username: string;
  email: string;
  role: string;
  is_active: boolean;
  created_at: string;
  updated_at?: string;
}

export interface ModelItem {
  config_id: number;
  name: string;
  provider: string;
  model_name: string;
  has_api_key: boolean;
  system_prompt: string;
  temperature: number;
  max_tokens: number;
  context_cache_ttl: number;
  is_default: boolean;
}

export interface ModelTestResponse {
  reply: string;
  model_name: string;
  prompt_tokens: number;
  output_tokens: number;
}

export interface AvailableModel {
  name: string;
  display_name: string;
}

export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface ChatResponse {
  session_id: string;
  reply: string;
  translation?: string;
  tokens_used: number;
  cached_tokens: number;
  sources?: RAGSource[];
  used_mock?: boolean;
}

/**
 * Over-the-wire form of an inline image attachment shipped to Gemini's
 * multimodal endpoint. `data` is base64-encoded image bytes with NO "data:"
 * prefix — the backend tolerates a stray prefix but prefers it stripped.
 */
export interface ChatImageInput {
  data: string;
  mime_type: string; // image/png | image/jpeg | image/webp | image/gif | ...
}

/** Result of POST /chat/voice — server-side transcription via Gemini. */
export interface VoiceTranscription {
  transcript: string;
  tokens_used: number;
  language: string;
  used_mock: boolean;
}

export interface RAGSource {
  doc_id: number;
  title: string;
  content: string;
  score: number;
}

export interface RAGQueryResponse {
  query: string;
  answer: string;
  sources: RAGSource[];
  tokens: number;
}

export interface KnowledgeDocument {
  doc_id: number;
  title: string;
  content?: string;
  language: string;
  category?: string;
  tags?: string[];
  source?: string;
  chunk_count: number;
  index_status: "pending" | "indexing" | "ready" | "failed";
  index_error?: string;
  created_at?: string;
  updated_at?: string;
}

export interface TokenStatsResponse {
  total_tokens: number;
  total_cost: number;
  cache_hit_rate: number;
  daily_usage: { date: string; tokens: number; cost: number }[];
}

// ============================================
// Sessions / messages (B2)
// ============================================

export type SessionStatus = "active" | "pending" | "handoff" | "resolved" | "closed";

export type PlatformMessageKind = "text" | "media" | "buttons" | "template";

export interface PlatformReplyButton {
  title: string;
  payload: string;
}

export interface PlatformMessagePayload {
  kind: Exclude<PlatformMessageKind, "text">;
  media_url?: string;
  media_type?: "image" | "video" | "audio" | "document";
  buttons?: PlatformReplyButton[];
  template_name?: string;
  template_language?: string;
  template_body_params?: string[];
}

export interface WhatsAppTemplate {
  name: string;
  language: string;
  category?: string;
  body_preview?: string;
  body_parameter_count: number;
}

export interface AgentReplyRequest {
  content: string;
  payload?: PlatformMessagePayload;
}

export interface ChatSession {
  session_id: string;
  user_id: number;
  status: SessionStatus;
  language: string;
  title?: string;
  platform?: string;
  user_message_count: number;
  model_message_count: number;
  first_response_at?: string | null;
  created_at: string;
  closed_at?: string | null;
}

export interface ChatMessageItem {
  message_id: number;
  session_id: string;
  role: "user" | "model" | "system" | "agent";
  message_type: string;
  content: string;
  tokens_used?: number;
  model_name?: string;
  used_mock?: boolean;
  feedback_rating?: number | null; // -1 / 1 / null
  feedback_comment?: string;
  feedback_at?: string | null;
  // RAG citations (parsed from sources_json on the backend side).
  sources_json?: string | null;
  // Inbound platform media is retained as tenant-scoped metadata. Its object
  // key is never returned; use getInboundPlatformMediaURL for a preview URL.
  metadata?: string | Record<string, unknown> | null;
  delivery?: {
    status: "pending" | "processing" | "sent" | "failed" | "cancelled";
    provider_status?: "sent" | "delivered" | "read" | "failed";
    sent_at?: string | null;
    delivered_at?: string | null;
    read_at?: string | null;
    last_error?: string;
    payload?: PlatformMessagePayload;
  };
  created_at: string;
}

export interface AnalyticsOverview {
  total_sessions: number;
  active_sessions: number;
  pending_handoff: number;
  avg_first_response_ms: number;
  avg_resolution_ms: number;
  csat: number;        // 0..1
  escalation_rate: number; // 0..1
  deflection_rate: number; // 0..1
  total_tokens: number;
  total_cost: number;
  cache_hit_rate: number;
}

export interface TimelinePoint {
  date: string;
  tokens: number;
  cost: number;
  sessions: number;
  messages: number;
  avg_response_ms: number;
  deflection_rate: number; // 0..1, per-day cohort deflection (resolved/sessions)
}

export interface InboxItem {
  session_id: string;
  user_id: number;
  platform?: string;
  platform_user_id?: string;
  user_display_name?: string;
  avatar_url?: string | null;
  status: SessionStatus;
  language: string;
  title?: string;
  user_message_count: number;
  model_message_count: number;
  assigned_agent_id?: number | null;
  assigned_agent_name?: string;
  last_message?: string;
  last_message_at?: string | null;
  last_inbound_at?: string | null;
  reply_window_expires_at?: string | null;
  first_response_at?: string | null;
  escalated_at?: string | null;
  created_at: string;
  sentiment?: "neutral" | "positive" | "negative";
}

export type HumanHandoffRequestStatus = "pending" | "assigned" | "resolved";
export type HumanHandoffPriority = "normal" | "high";
export type HumanHandoffTrigger = "customer_request" | "negative_feedback" | "ai_decision" | "manual";

export interface HumanHandoffRequest {
  request_id: string;
  session_id: string;
  status: HumanHandoffRequestStatus;
  priority: HumanHandoffPriority;
  trigger: HumanHandoffTrigger;
  reason: string;
  assigned_agent_id?: number | null;
  assigned_agent_name?: string;
  created_at: string;
  assigned_at?: string | null;
  resolved_at?: string | null;
  resolution_note?: string;
  platform?: string;
  platform_user_id?: string;
  user_display_name?: string;
  session_title?: string;
  last_message?: string;
  last_message_at?: string | null;
}

export interface CreateHumanHandoffRequestInput {
  session_id: string;
  reason: string;
  priority: HumanHandoffPriority;
}

export interface CreateHumanHandoffRequestResult {
  data: HumanHandoffRequest;
  created: boolean;
}

export interface TopQuery { query: string; count: number }
export interface LanguageBreakdown { language: string; count: number }

// Chat
export async function sendMessage(message: string, sessionId?: string, language?: string) {
  return apiFetch<ChatResponse>("/chat", {
    method: "POST",
    body: JSON.stringify({ message, session_id: sessionId, language: language || "km" }),
  });
}

/** Sends an internal RAG-backed trial that is excluded from the customer Inbox. */
export async function sendTestMessage(message: string, sessionId?: string, language?: string) {
  return apiFetch<ChatResponse>("/chat", {
    method: "POST",
    body: JSON.stringify({ message, session_id: sessionId, language: language || "km", test: true }),
  });
}

/** One simulated customer turn in the AI test bench report. */
export interface SimulatedTurn {
  index: number;
  customer_message: string;
  detected_language: string;
  decision: "ai_reply" | "handoff_customer_request" | "handoff_no_knowledge_base" | "handoff_ai_decision" | "handoff_low_confidence";
  decision_reason: string;
  reply: string;
  sources: RAGSource[];
  top_score?: number | null;
  elapsed_ms: number;
  used_mock: boolean;
}

export interface SimulateResponse {
  session_id: string;
  platform?: string | null;
  turns: SimulatedTurn[];
  summary: { total_turns: number; ai_replies: number; handoffs: number; avg_latency_ms: number };
}

/**
 * Runs a scripted multi-turn customer simulation through the real reply
 * pipeline (keyword escalation → AI handoff classifier → RAG → Gemini) in a
 * private is_test session. Escalations are reported per turn, never executed.
 */
export async function runTestSimulation(body: {
  platform?: string;
  language?: string;
  messages: string[];
  session_id?: string;
}) {
  return apiFetch<SimulateResponse>("/chat/test-simulate", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

/**
 * Transcribe an audio blob server-side via Gemini (POST /chat/voice, multipart).
 * Returns the transcript text; the caller feeds it into the normal /chat/stream
 * pipeline so voice turns still get full RAG + history + streaming treatment.
 *
 * The client feeds back the transcript rather than relying on the browser's
 * SpeechRecognition because Khmer support in the Web Speech API is poor, while
 * Gemini transcribes Khmer reliably.
 */
export async function chatVoice(
  audio: Blob,
  language = "km",
  opts?: { signal?: AbortSignal },
): Promise<VoiceTranscription> {
  const token = localStorage.getItem("token");
  const form = new FormData();
  // Preserve the recording mime so the backend can pass the right MIMEType to Gemini.
  form.append("audio", audio, `voice.${(audio.type.split("/")[1] || "webm").split(";")[0]}`);
  form.append("language", language);

  const res = await fetch(`${API_BASE}/chat/voice`, {
    method: "POST",
    headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: form,
    signal: opts?.signal,
  });
  if (res.status === 401) signalAuthExpired();

  const contentType = res.headers.get("content-type") || "";
  let data: unknown = null;
  if (contentType.includes("application/json")) {
    data = await res.json();
  } else {
    const text = await res.text().catch(() => "");
    throw new ApiError(text || `Transcription failed (HTTP ${res.status})`, res.status);
  }
  if (!res.ok) {
    const message = (data as { error?: string } | null)?.error || `Transcription failed (HTTP ${res.status})`;
    throw new ApiError(message, res.status);
  }
  return data as VoiceTranscription;
}

// RAG Knowledge
export async function ragQuery(query: string, topK = 5, language?: string) {
  return apiFetch<RAGQueryResponse>("/rag/query", {
    method: "POST",
    body: JSON.stringify({ query, top_k: topK, language }),
  });
}

export async function uploadKnowledge(data: {
  title: string;
  content: string;
  language?: string;
  category?: string;
  tags?: string[];
}) {
  return apiFetch<KnowledgeDocument>("/knowledge/upload", {
    method: "POST",
    body: JSON.stringify(data),
  });
}

/**
 * Upload a file (PDF/DOCX/TXT/MD/CSV) for RAG ingestion.
 * Uses multipart/form-data — bypasses the default JSON headers in apiFetch.
 * Returns the created KnowledgeDocument.
 */
export async function uploadKnowledgeFile(
  file: File,
  opts?: { category?: string; language?: string },
): Promise<KnowledgeDocument> {
  const token = localStorage.getItem("token");
  const form = new FormData();
  form.append("file", file);
  if (opts?.category) form.append("category", opts.category);
  if (opts?.language) form.append("language", opts.language);

  const res = await fetch(`${API_BASE}/knowledge/upload/file`, {
    method: "POST",
    headers: {
      // Do NOT set Content-Type — browser sets it with the boundary for FormData.
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: form,
  });
  if (res.status === 401) signalAuthExpired();

  const contentType = res.headers.get("content-type") || "";
  let data: unknown = null;
  if (contentType.includes("application/json")) {
    data = await res.json();
  } else {
    const text = await res.text().catch(() => "");
    throw new ApiError(text || `Upload failed (HTTP ${res.status})`, res.status);
  }
  if (!res.ok) {
    const message = (data as { error?: string } | null)?.error || `Upload failed (HTTP ${res.status})`;
    throw new ApiError(message, res.status);
  }
  return data as KnowledgeDocument;
}

/**
 * Ingest a web page by URL: the backend fetches the page, strips HTML to clean
 * text, then runs it through the same chunk → embed → store RAG pipeline as a
 * file upload. Falls back to the page <title> when no title is supplied.
 */
export async function ingestKnowledgeURL(data: {
  url: string;
  title?: string;
  language?: string;
  category?: string;
}): Promise<KnowledgeDocument> {
  return apiFetch<KnowledgeDocument>("/knowledge/upload/url", {
    method: "POST",
    body: JSON.stringify({ language: "km", ...data }),
  });
}

export interface AcceptedFileTypes {
  extensions: Record<string, string>;
  max_bytes: number;
  max_megabytes: number;
}

export async function getAcceptedFileTypes() {
  return apiFetch<AcceptedFileTypes>("/knowledge/accepted-types");
}

export async function listKnowledge(page = 1, pageSize = 20) {
  return apiFetch<PaginatedResponse<KnowledgeDocument>>(`/knowledge?page=${page}&page_size=${pageSize}`);
}

export async function getKnowledgeDocument(docId: number) {
  return apiFetch<KnowledgeDocument>(`/knowledge/${docId}`);
}

export async function updateKnowledgeDocument(
  docId: number,
  data: { content: string; title?: string; language?: string; category?: string; tags?: string[] },
) {
  return apiFetch<KnowledgeDocument>(`/knowledge/${docId}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });
}

export async function retryKnowledge(docId: number) {
  return apiFetch<{ message: string }>(`/knowledge/${docId}/retry`, { method: "POST" });
}

export async function deleteKnowledge(docId: number) {
  return apiFetch<{ message: string }>(`/knowledge/${docId}`, { method: "DELETE" });
}

// Admin
export async function listUsers(page = 1, pageSize = 20) {
  return apiFetch<PaginatedResponse<UserItem>>(`/admin/users?page=${page}&page_size=${pageSize}`);
}

export async function updateUserRole(userId: number, role: string, isActive?: boolean) {
  return apiFetch<{ message: string }>(`/admin/users/${userId}/role`, {
    method: "PUT",
    body: JSON.stringify({ role, is_active: isActive }),
  });
}

export async function getTokenStats(days = 30) {
  return apiFetch<TokenStatsResponse>(`/admin/tokens/stats?days=${days}`);
}

export async function listModelConfigs() {
  return apiFetch<ModelItem[]>("/admin/models");
}

export async function updateModelConfig(configId: number, data: Record<string, unknown>) {
  return apiFetch<{ message: string }>(`/admin/models/${configId}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });
}

export async function testModelConfig(configId: number, message: string) {
  return apiFetch<ModelTestResponse>(`/admin/models/${configId}/test`, {
    method: "POST",
    body: JSON.stringify({ message }),
  });
}

export async function listAvailableModels(configId: number) {
  return apiFetch<AvailableModel[]>(`/admin/models/${configId}/available`);
}

// ============================================
// Sessions (B2)
// ============================================

export async function listSessions(page = 1, pageSize = 50, q?: string) {
  const qs = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (q) qs.set("q", q);
  return apiFetch<PaginatedResponse<ChatSession>>(`/chat/sessions?${qs}`);
}

export async function createSession(data: { title?: string; language?: string }) {
  return apiFetch<ChatSession>("/chat/sessions", {
    method: "POST",
    body: JSON.stringify(data),
  });
}

export async function updateSession(id: string, data: { title?: string; status?: SessionStatus }) {
  return apiFetch<{ message: string }>(`/chat/sessions/${id}`, {
    method: "PATCH",
    body: JSON.stringify(data),
  });
}

export async function deleteSession(id: string) {
  return apiFetch<{ message: string }>(`/chat/sessions/${id}`, { method: "DELETE" });
}

export async function listSessionMessages(id: string, limit = 100, after = 0) {
  const qs = new URLSearchParams({ limit: String(limit) });
  if (after > 0) qs.set("after", String(after));
  return apiFetch<ChatMessageItem[]>(`/chat/sessions/${id}/messages?${qs}`);
}

export interface InboundPlatformMedia {
  kind: string;
  filename?: string;
  mime_type?: string;
  processing_status?: "stored" | "ready" | "unavailable";
  processing_error?: string;
  extracted_text?: string;
}

export async function getInboundPlatformMediaURL(messageId: number) {
  return apiFetch<{ url: string; expires_at: string }>(`/inbox/messages/${messageId}/media-url`);
}

export async function sendFeedback(messageId: number, rating: -1 | 1, comment?: string) {
  return apiFetch<{ message: string }>(`/chat/messages/${messageId}/feedback`, {
    method: "POST",
    body: JSON.stringify({ rating, comment: comment || "" }),
  });
}

/**
 * streamChat opens an SSE connection to /chat/stream and invokes callbacks as
 * tokens arrive. Returns an AbortController so the caller can implement "Stop".
 *
 * The SSE wire format is:
 *   event: session\ndata: {"session_id":"..."}\n\n
 *   event: token\ndata: {"text":"..."}\n\n   (repeated)
 *   event: done\ndata: {"reply":"full text","tokens_used":N,...}\n\n
 *   event: error\ndata: {"message":"..."}\n\n
 */
export function streamChat(
  body: { message: string; session_id?: string; language?: string; images?: ChatImageInput[] },
  handlers: {
    onSession?: (sessionId: string) => void;
    onSources?: (sources: RAGSource[]) => void;
    onToken?: (chunk: string) => void;
    onDone?: (final: { reply: string; tokens_used: number; cached_tokens: number; used_mock: boolean }) => void;
    onError?: (message: string) => void;
  },
): AbortController {
  const controller = new AbortController();
  const token = localStorage.getItem("token");

  (async () => {
    let res: Response;
    try {
      res = await fetch(`${API_BASE}/chat/stream`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({ ...body, language: body.language || "km" }),
        signal: controller.signal,
      });
    } catch (err) {
      if ((err as Error).name !== "AbortError") {
        handlers.onError?.((err as Error).message);
      }
      return;
    }

    if (!res.ok || !res.body) {
      if (res.status === 401) signalAuthExpired();
      let msg = `HTTP ${res.status}`;
      try { msg = (await res.json()).error || msg; } catch { /* ignore */ }
      handlers.onError?.(msg);
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let receivedTerminalEvent = false;

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        // SSE events are separated by a blank line.
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const raw = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const evt = parseSSE(raw);
          if (!evt) continue;
          switch (evt.event) {
            case "session":
              handlers.onSession?.((evt.data as { session_id: string }).session_id);
              break;
            case "sources":
              handlers.onSources?.(evt.data as RAGSource[]);
              break;
            case "token":
              handlers.onToken?.((evt.data as { text: string }).text);
              break;
            case "done":
              receivedTerminalEvent = true;
              handlers.onDone?.(evt.data as { reply: string; tokens_used: number; cached_tokens: number; used_mock: boolean });
              break;
            case "error":
              receivedTerminalEvent = true;
              handlers.onError?.((evt.data as { message: string }).message);
              break;
          }
        }
      }
      if (!receivedTerminalEvent && !controller.signal.aborted) {
        handlers.onError?.("The response stream ended before completion.");
      }
    } catch (err) {
      if ((err as Error).name !== "AbortError") {
        handlers.onError?.((err as Error).message);
      }
    }
  })();

  return controller;
}

function parseSSE(raw: string): { event: string; data: unknown } | null {
  let event = "message";
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
  }
  if (dataLines.length === 0) return null;
  try {
    return { event, data: JSON.parse(dataLines.join("\n")) };
  } catch {
    return { event, data: {} };
  }
}

// ============================================
// Analytics + Inbox (B4)
// ============================================

export async function getAnalyticsOverview(days = 30) {
  return apiFetch<AnalyticsOverview>(`/admin/analytics/overview?days=${days}`);
}

export async function getAnalyticsTimeline(days = 30) {
  return apiFetch<TimelinePoint[]>(`/admin/analytics/timeline?days=${days}`);
}

export async function getTopQueries(days = 30, limit = 10) {
  return apiFetch<TopQuery[]>(`/admin/analytics/top-queries?days=${days}&limit=${limit}`);
}

export async function getLanguageBreakdown(days = 30) {
  return apiFetch<LanguageBreakdown[]>(`/admin/analytics/languages?days=${days}`);
}

export async function listFeedback(page = 1, pageSize = 50, rating?: -1 | 1) {
  const qs = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
  if (rating) qs.set("rating", String(rating));
  return apiFetch<PaginatedResponse<ChatMessageItem>>(`/admin/feedback?${qs}`);
}

export async function listInbox(params?: { status?: SessionStatus; page?: number; pageSize?: number }) {
  const qs = new URLSearchParams();
  if (params?.status) qs.set("status", params.status);
  qs.set("page", String(params?.page ?? 1));
  qs.set("page_size", String(params?.pageSize ?? 50));
  return apiFetch<PaginatedResponse<InboxItem>>(`/inbox?${qs}`);
}

export async function listHumanHandoffRequests(params?: { status?: HumanHandoffRequestStatus; page?: number; pageSize?: number }) {
  const qs = new URLSearchParams();
  if (params?.status) qs.set("status", params.status);
  qs.set("page", String(params?.page ?? 1));
  qs.set("page_size", String(params?.pageSize ?? 100));
  return apiFetch<PaginatedResponse<HumanHandoffRequest>>(`/handoff-requests?${qs}`);
}

export async function createHumanHandoffRequest(input: CreateHumanHandoffRequestInput) {
  return apiFetch<CreateHumanHandoffRequestResult>("/handoff-requests", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function assignSession(sessionId: string, agentId: number, note?: string) {
  return apiFetch<{ message: string }>(`/inbox/sessions/${sessionId}/assign`, {
    method: "POST",
    body: JSON.stringify({ agent_id: agentId, note: note || "" }),
  });
}

export async function takeoverSession(sessionId: string) {
  return apiFetch<{ message: string }>(`/inbox/sessions/${sessionId}/takeover`, { method: "POST" });
}

export async function updateSessionStatus(sessionId: string, status: SessionStatus, internalNotes?: string) {
  return apiFetch<{ message: string }>(`/inbox/sessions/${sessionId}/status`, {
    method: "PATCH",
    body: JSON.stringify({ status, internal_notes: internalNotes || "" }),
  });
}

/** Agent sends a reply in a handoff session. Returns the stored message. */
export async function agentReply(sessionId: string, request: AgentReplyRequest) {
  return apiFetch<ChatMessageItem>(`/inbox/sessions/${sessionId}/reply`, {
    method: "POST",
    body: JSON.stringify(request),
  });
}

export async function listSessionWhatsAppTemplates(sessionId: string) {
  return apiFetch<{ templates: WhatsAppTemplate[] }>(`/inbox/sessions/${sessionId}/whatsapp-templates`);
}

/** Get (or lazily generate) a conversation summary. Language follows the
 *  user's preference server-side unless overridden; refresh=true forces
 *  regeneration even when a valid cache exists. */
export async function getSessionSummary(sessionId: string, language?: string, refresh = false) {
  const qs = new URLSearchParams();
  if (language && language !== "auto") qs.set("language", language);
  if (refresh) qs.set("refresh", "1");
  const suffix = qs.toString() ? `?${qs}` : "";
  return apiFetch<{ summary: string; cached: boolean; language: string }>(
    `/inbox/sessions/${sessionId}/summary${suffix}`,
  );
}

/** The caller's saved language ("auto" | "km" | "en" | "zh") + notification preference. */
export async function getPreferences() {
  return apiFetch<{ language: string; notification_pref: string }>("/auth/preferences");
}

// ============================================
// Business hours + canned responses + session tags (batch 3)
// ============================================

export interface BusinessHours {
  id?: number;
  weekday: number;        // 0=Sun..6=Sat
  open_time?: string;     // "HH:MM", empty = closed
  close_time?: string;
  platform?: SessionStatus | string | null;
  is_active?: boolean;
}

export async function listBusinessHours() {
  return apiFetch<BusinessHours[]>("/admin/business-hours");
}

export async function upsertBusinessHours(entries: BusinessHours[]) {
  return apiFetch<{ message: string }>("/admin/business-hours", {
    method: "PUT",
    body: JSON.stringify({ entries }),
  });
}

export async function isBusinessOpen(platform?: string) {
  const qs = platform ? `?platform=${platform}` : "";
  return apiFetch<{ open: boolean; reason?: string; weekday: number; time: string; open_time?: string; close_time?: string }>(`/admin/business-hours/open${qs}`);
}

export interface CannedResponse {
  id: number;
  title: string;
  body: string;
  category?: string;
  language: string;
  is_active?: boolean;
}

export async function listCannedResponses(category?: string, language?: string) {
  const qs = new URLSearchParams();
  if (category) qs.set("category", category);
  if (language) qs.set("language", language);
  const suffix = qs.toString() ? `?${qs}` : "";
  return apiFetch<CannedResponse[]>(`/admin/canned-responses${suffix}`);
}

export async function createCannedResponse(data: { title: string; body: string; category?: string; language?: string }) {
  return apiFetch<CannedResponse>("/admin/canned-responses", {
    method: "POST",
    body: JSON.stringify(data),
  });
}

export async function deleteCannedResponse(id: number) {
  return apiFetch<{ message: string }>(`/admin/canned-responses/${id}`, { method: "DELETE" });
}

export async function setSessionTags(sessionId: string, tags: string[]) {
  return apiFetch<{ message: string; tags: string[] }>(`/inbox/sessions/${sessionId}/tags`, {
    method: "PUT",
    body: JSON.stringify({ tags }),
  });
}

// ============================================
// Growth features (F1–F9): customers, FAQ, team, campaigns, billing
// ============================================

export interface CustomerProfile {
  profile_id: number;
  platform: string;
  platform_user_id: string;
  display_name: string;
  phone?: string | null;
  email?: string | null;
  tags: string[];
  total_sessions: number;
  total_messages: number;
  last_seen_at?: string | null;
  notes: string;
  created_at: string;
  updated_at: string;
}

export interface CustomerSession360 {
  session_id: string;
  platform?: string | null;
  status: string;
  title?: string | null;
  sentiment: string;
  summary?: string | null;
  user_message_count: number;
  model_message_count: number;
  satisfaction_score?: number | null;
  created_at: string;
  resolved_at?: string | null;
}

export interface Customer360 {
  profile: CustomerProfile;
  sessions: CustomerSession360[];
}

export interface FaqSuggestion {
  suggestion_id: number;
  question: string;
  answer: string;
  frequency: number;
  status: string;
  source: string;
  created_at: string;
}

export interface AgentMember {
  team_id: number;
  agent_user_id: number;
  display_name: string;
  skills: string[];
  is_active: boolean;
  username: string;
  email: string;
}

export interface CampaignItem {
  campaign_id: number;
  name: string;
  platform: string;
  config_id: number;
  template_name: string;
  template_language: string;
  body_params: string[];
  recipient_filter: string;
  tag_filter?: string | null;
  scheduled_at: string;
  status: string;
  sent_count: number;
  created_at: string;
}

export interface BillingInfo {
  plan: string;
  monthly_message_quota: number;
  monthly_doc_quota: number;
  messages_used: number;
  docs_used: number;
  cycle_start: string;
  cycle_end: string;
}

export function listCustomers(q?: string) {
  const qs = q ? `?q=${encodeURIComponent(q)}` : "";
  return apiFetch<CustomerProfile[]>(`/customers${qs}`);
}
export function getCustomer360(profileId: number) {
  return apiFetch<Customer360>(`/customers/${profileId}`);
}
export function updateCustomerNotes(profileId: number, notes: string) {
  return apiFetch<{ message: string }>(`/customers/${profileId}/notes`, { method: "PUT", body: JSON.stringify({ notes }) });
}
export function listFaqSuggestions(status?: string) {
  const qs = status ? `?status=${status}` : "";
  return apiFetch<FaqSuggestion[]>(`/admin/faq/suggestions${qs}`);
}
export function acceptFaqSuggestion(id: number, title: string) {
  return apiFetch<unknown>(`/admin/faq/suggestions/${id}/accept`, { method: "POST", body: JSON.stringify({ title }) });
}
export function dismissFaqSuggestion(id: number) {
  return apiFetch<{ message: string }>(`/admin/faq/suggestions/${id}/dismiss`, { method: "POST" });
}
export function listTeam() {
  return apiFetch<AgentMember[]>("/team");
}
export function addAgent(userId: number, displayName: string, skills: string[]) {
  return apiFetch<{ message: string }>("/team/agents", { method: "POST", body: JSON.stringify({ user_id: userId, display_name: displayName, skills }) });
}
export function removeAgent(teamId: number) {
  return apiFetch<{ message: string }>(`/team/agents/${teamId}`, { method: "DELETE" });
}
export function listCampaigns() {
  return apiFetch<CampaignItem[]>("/campaigns");
}
export function createCampaign(input: {
  name: string; platform: string; config_id: number; template_name: string; template_language: string;
  body_params?: string[]; recipient_filter: string; scheduled_at: string;
}) {
  return apiFetch<{ campaign_id: number }>("/campaigns", { method: "POST", body: JSON.stringify(input) });
}
export function cancelCampaign(id: number) {
  return apiFetch<{ message: string }>(`/campaigns/${id}/cancel`, { method: "POST" });
}
export function getBilling() {
  return apiFetch<BillingInfo>("/billing");
}
export function setPlan(plan: string) {
  return apiFetch<{ message: string }>("/billing/plan", { method: "PUT", body: JSON.stringify({ plan }) });
}

// ============================================
// Platform super-admin (cross-tenant management)
// ============================================

export interface TenantItem {
  user_id: number;
  username: string;
  email: string;
  role: string;
  is_active: boolean;
  created_at: string;
  plan: string;
  messages_used: number;
  message_quota: number;
  docs_used: number;
  doc_quota: number;
  total_sessions: number;
  total_messages: number;
  total_documents: number;
}

export interface TenantSessionRow {
  session_id: string;
  platform?: string | null;
  status: string;
  title?: string | null;
  sentiment: string;
  user_message_count: number;
  model_message_count: number;
  created_at: string;
}

export interface TenantDetail {
  tenant: TenantItem;
  sessions: TenantSessionRow[];
}

export interface PlatformAnalytics {
  total_tenants: number;
  active_tenants: number;
  total_sessions: number;
  total_messages: number;
  total_tokens: number;
  total_documents: number;
  plan_distribution: { plan: string; count: number }[];
  daily_messages: { date: string; messages: number }[];
}

export interface PaginatedTenants {
  data: TenantItem[];
  total: number;
  page: number;
  page_size: number;
}

export function listTenants(params?: { q?: string; page?: number; pageSize?: number }) {
  const qs = new URLSearchParams();
  if (params?.q) qs.set("q", params.q);
  qs.set("page", String(params?.page ?? 1));
  qs.set("page_size", String(params?.pageSize ?? 50));
  return apiFetch<PaginatedTenants>(`/platform/tenants?${qs}`);
}
export function getTenantDetail(userId: number) {
  return apiFetch<TenantDetail>(`/platform/tenants/${userId}`);
}
export function setTenantStatus(userId: number, isActive: boolean) {
  return apiFetch<{ message: string }>(`/platform/tenants/${userId}/status`, { method: "PUT", body: JSON.stringify({ is_active: isActive }) });
}
export function setTenantPlan(userId: number, plan: string) {
  return apiFetch<{ message: string }>(`/platform/tenants/${userId}/plan`, { method: "PUT", body: JSON.stringify({ plan }) });
}
export function getPlatformAnalytics() {
  return apiFetch<PlatformAnalytics>("/platform/analytics");
}
export function createTenant(input: { username: string; email: string; password: string; plan?: string }) {
  return apiFetch<{ user_id: number; message: string }>("/platform/tenants", { method: "POST", body: JSON.stringify(input) });
}

export interface AuditLogItem {
  log_id: number;
  username?: string;
  action: string;
  target_type?: string;
  target_id?: string;
  /** JSON-encoded request details (method/path/status/request_id). */
  details?: string;
  ip_address?: string;
  created_at?: string;
}

export function listAuditLogs(params?: { q?: string; page?: number; pageSize?: number }) {
  const qs = new URLSearchParams();
  if (params?.q) qs.set("q", params.q);
  qs.set("page", String(params?.page ?? 1));
  qs.set("page_size", String(params?.pageSize ?? 50));
  return apiFetch<{ data: AuditLogItem[]; total: number; page: number; page_size: number }>(`/platform/audit-logs?${qs}`);
}

// ============================================
// Notifications center
// ============================================

export interface NotificationItem {
  notification_id: number;
  kind: string;
  title: string;
  body: string;
  session_id?: string | null;
  is_read: boolean;
  created_at: string;
}

export function listNotifications(limit = 30) {
  return apiFetch<NotificationItem[]>(`/notifications?limit=${limit}`);
}
export function notificationsUnread() {
  return apiFetch<{ unread: number }>("/notifications/unread-count");
}
export function notificationsMarkRead(id: number) {
  return apiFetch<{ message: string }>(`/notifications/${id}/read`, { method: "POST" });
}
export function notificationsReadAll() {
  return apiFetch<{ message: string }>("/notifications/read-all", { method: "POST" });
}

// ============================================
// CSV report export
// ============================================

export interface ReportQuery { from: string; to: string }

/** Trigger a browser download of a CSV report. */
export async function downloadReport(kind: "sessions" | "messages" | "tokens", from: string, to: string) {
  const qs = new URLSearchParams({ from, to });
  const token = localStorage.getItem("token");
  // Use the same absolute API_BASE as every other call — the Next.js server has
  // no /api/v1 rewrite, so a relative path would hit the frontend (3000) and 404.
  const resp = await fetch(`${API_BASE}/reports/${kind}?${qs}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (!resp.ok) {
    let msg = `export failed (${resp.status})`;
    try { const d = await resp.json(); msg = d.error ?? msg; } catch { /* ignore */ }
    throw new Error(msg);
  }
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${kind}-${from.slice(0, 10)}-${to.slice(0, 10)}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// ============================================
// Enterprise agent platform (SLA, routing, macros, RBAC, 2FA, webhooks, copilot, analytics)
// ============================================

export interface SlaPolicy {
  sla_id: number;
  name: string;
  first_response_secs: number;
  resolution_secs?: number | null;
  business_hours_only: boolean;
  priority: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface SlaBreach {
  breach_id: number;
  session_id: string;
  sla_id?: number | null;
  breach_type: string;
  breached_at: string;
  resolved: boolean;
  resolved_at?: string | null;
  title?: string | null;
  platform?: string | null;
}

export interface RoutingRule {
  rule_id: number;
  name: string;
  conditions: Record<string, unknown>;
  target_type: string;
  target_agent_id?: number | null;
  target_skills: string[];
  priority: number;
  is_active: boolean;
  created_at: string;
}

export interface Macro {
  macro_id: number;
  title: string;
  steps: { content: string; delay_ms?: number }[];
  category?: string | null;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface Role {
  role_id: number;
  name: string;
  permissions: string[];
  is_system: boolean;
  created_at: string;
}

export interface WebhookSubscription {
  subscription_id: number;
  url: string;
  events: string[];
  is_active: boolean;
  created_at: string;
}

export interface WebhookDelivery {
  delivery_id: number;
  subscription_id: number;
  event: string;
  status_code?: number | null;
  success: boolean;
  error?: string | null;
  created_at: string;
}

export interface AgentPerformance {
  user_id: number;
  username: string;
  display_name: string;
  resolved_count: number;
  handled_count: number;
  avg_handle_secs: number;
  avg_csat: number;
  reply_count: number;
}

export interface IntentCount {
  intent: string;
  count: number;
  avg_confidence?: number | null;
}

export interface IntegrationStatus {
  email: { enabled: boolean; configured: boolean; inbound_domains: string };
  voice: { enabled: boolean; configured: boolean; from_number: string };
  sso: { enabled: boolean; configured: boolean; provider: string };
}

// 2FA
export function totpStatus() {
  return apiFetch<{ enabled: boolean }>("/auth/totp/status");
}
export function totpSetup() {
  return apiFetch<{ secret: string; otpauth_uri: string }>("/auth/totp/setup", { method: "POST" });
}
export function totpVerify(code: string) {
  return apiFetch<{ message: string }>("/auth/totp/verify", { method: "POST", body: JSON.stringify({ code }) });
}
export function totpDisable(code: string) {
  return apiFetch<{ message: string }>("/auth/totp/disable", { method: "POST", body: JSON.stringify({ code }) });
}

// SLA
export function listSlaPolicies() {
  return apiFetch<SlaPolicy[]>("/sla");
}
export function upsertSlaPolicy(input: { name: string; first_response_secs: number; resolution_secs?: number; priority?: string }) {
  return apiFetch<{ sla_id: number }>("/sla", { method: "POST", body: JSON.stringify(input) });
}
export function deleteSlaPolicy(id: number) {
  return apiFetch<{ message: string }>(`/sla/${id}`, { method: "DELETE" });
}
export function listSlaBreaches(open = true) {
  return apiFetch<SlaBreach[]>(`/sla/breaches?open=${open}`);
}

// Routing
export function listRoutingRules() {
  return apiFetch<RoutingRule[]>("/routing");
}
export function upsertRoutingRule(input: { name: string; conditions: Record<string, unknown>; target_type?: string; target_agent_id?: number; target_skills?: string[]; priority?: number }) {
  return apiFetch<{ rule_id: number }>("/routing", { method: "POST", body: JSON.stringify(input) });
}
export function deleteRoutingRule(id: number) {
  return apiFetch<{ message: string }>(`/routing/${id}`, { method: "DELETE" });
}

// Macros
export function listMacros() {
  return apiFetch<Macro[]>("/macros");
}
export function createMacro(input: { title: string; steps: { content: string; delay_ms?: number }[] }) {
  return apiFetch<{ macro_id: number }>("/macros", { method: "POST", body: JSON.stringify(input) });
}
export function deleteMacro(id: number) {
  return apiFetch<{ message: string }>(`/macros/${id}`, { method: "DELETE" });
}

// Roles
export function listRoles() {
  return apiFetch<Role[]>("/roles");
}
export function createRole(input: { name: string; permissions: string[] }) {
  return apiFetch<{ role_id: number }>("/roles", { method: "POST", body: JSON.stringify(input) });
}
export function deleteRole(id: number) {
  return apiFetch<{ message: string }>(`/roles/${id}`, { method: "DELETE" });
}

// Outbound webhooks
export function listWebhookSubscriptions() {
  return apiFetch<WebhookSubscription[]>("/webhooks/subscriptions");
}
export function createWebhookSubscription(input: { url: string; events: string[]; secret?: string }) {
  return apiFetch<{ subscription_id: number }>("/webhooks/subscriptions", { method: "POST", body: JSON.stringify(input) });
}
export function deleteWebhookSubscription(id: number) {
  return apiFetch<{ message: string }>(`/webhooks/subscriptions/${id}`, { method: "DELETE" });
}
export function listWebhookDeliveries(limit = 50) {
  return apiFetch<WebhookDelivery[]>(`/webhooks/deliveries?limit=${limit}`);
}

// Copilot
export function copilotSuggest(sessionId: string, count = 3) {
  return apiFetch<{ suggestions: string[]; grounded: boolean }>(`/inbox/sessions/${sessionId}/copilot/suggest`, {
    method: "POST",
    body: JSON.stringify({ count }),
  });
}
export function copilotKnowledge(sessionId: string) {
  return apiFetch<{ sources: RAGSource[] }>(`/inbox/sessions/${sessionId}/copilot/knowledge`);
}

// Notes
export function listMessageNotes(sessionId: string) {
  return apiFetch<{ note_id: number; body: string; username: string; created_at: string }[]>(`/inbox/sessions/${sessionId}/notes`);
}
export function createMessageNote(sessionId: string, body: string) {
  return apiFetch<{ note_id: number }>(`/inbox/sessions/${sessionId}/notes`, { method: "POST", body: JSON.stringify({ body }) });
}

// Performance + intent analytics + integrations
export function getAgentPerformance(days = 30) {
  return apiFetch<AgentPerformance[]>(`/admin/agent-performance?days=${days}`);
}
export function getIntentAnalytics(days = 30) {
  return apiFetch<IntentCount[]>(`/admin/intent-analytics?days=${days}`);
}
export function getIntegrationStatus() {
  return apiFetch<IntegrationStatus>("/admin/integrations/status");
}

