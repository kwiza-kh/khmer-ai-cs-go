"use client";

// Public website-chat widget UI. Rendered inside the iframe opened by
// widget-embed.js. Authenticated solely by the ?t= embed token; no JWT.
// Deliberately self-contained (inline styles + one injected <style> block):
// the page ships inside third-party sites' iframes and must not depend on
// the app's Tailwind theme or i18n provider.
import * as React from "react";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { API_BASE } from "@/lib/auth-client";

// The API base is the build-time NEXT_PUBLIC_API_URL, never a query
// parameter: trusting ?api= let any page frame the widget with the tenant's
// public token and api=<attacker origin>, then poison the persisted session
// id with an attacker-chosen value.
const FIXED_API_BASE = API_BASE.replace(/\/$/, "");

// Server-issued session ids are UUIDs; validating the persisted sid on
// restore keeps attacker-chosen junk (from a poisoned storage context) out of
// transcript URLs.
const SID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

type Msg = {
  id: number;
  dbId?: number;      // chat_messages.message_id (present once synced)
  role: "user" | "model" | "system";
  content: string;
  rating?: number | null;
  pending?: boolean;
};

type WidgetCfg = {
  name?: string;
  theme?: "light" | "dark" | "auto";
  primary_color?: string;
  greeting_km?: string;
  greeting_en?: string;
  suggested_questions?: string[];
};

type Lang = "km" | "en" | "zh";

const STR: Record<Lang, Record<string, string>> = {
  km: {
    title: "ជំនួយការអតិថិជន", intro: "សួស្តី! តើយើងអាចជួយអ្នកដោយរបៀបណា?",
    placeholder: "សរសេរសារ...",
    send: "ផ្ញើ", powered: "ដំណើរការដោយ RelayChat", online: "អនឡាញ", ai: "AI",
  },
  en: {
    title: "Support Assistant", intro: "Hi! How can we help you today?", placeholder: "Type a message…",
    send: "Send", powered: "Powered by RelayChat", online: "online", ai: "AI",
  },
  zh: {
    title: "客服助手", intro: "您好！有什么可以帮您？", placeholder: "输入消息…",
    send: "发送", powered: "由 RelayChat 提供", online: "在线", ai: "AI",
  },
};

// Injected once: keyframes for the presence dot, typing dots, message
// entrance and the suggestion-chip hover. Inline styles can't animate.
const WIDGET_CSS = `
@keyframes kw-pulse { 0% { box-shadow: 0 0 0 0 rgba(74,222,128,.55); } 70% { box-shadow: 0 0 0 5px rgba(74,222,128,0); } 100% { box-shadow: 0 0 0 0 rgba(74,222,128,0); } }
@keyframes kw-dot-lift { 0%, 80%, 100% { transform: translateY(0); opacity: .45; } 40% { transform: translateY(-3px); opacity: 1; } }
@keyframes kw-in { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: translateY(0); } }
.kw-msg { animation: kw-in .18s ease-out; }
.kw-dot { width: 7px; height: 7px; border-radius: 50%; background: #4ade80; animation: kw-pulse 1.8s ease-out infinite; }
.kw-typing span { width: 5px; height: 5px; border-radius: 50%; background: currentColor; display: inline-block; margin-right: 3px; animation: kw-dot-lift 1s infinite ease-in-out; }
.kw-typing span:nth-child(2) { animation-delay: .15s; }
.kw-typing span:nth-child(3) { animation-delay: .3s; }
.kw-chip { transition: border-color .12s ease, color .12s ease, background .12s ease; }
.kw-chip:hover { border-color: currentColor !important; }
`;

function parseLine(raw: string): { event: string; data: unknown } | null {
  let event = "message";
  const dataLines: string[] = [];
  for (const line of raw.split("\n")) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
  }
  if (dataLines.length === 0) return null;
  try { return { event, data: JSON.parse(dataLines.join("\n")) }; } catch { return { event, data: {} }; }
}

// Session-id fallback for when localStorage is unavailable or cleared:
// losing the id strands a returning visitor in a brand-new session whose AI
// has zero prior context. Cookies survive localStorage clears (and vice
// versa), so the two together cover most data-wiping scenarios.
const widgetSidCookieName = (token: string) => `khmer_widget_sid_${token.replace(/[^a-zA-Z0-9]/g, "").slice(0, 32)}`;

function readWidgetSidCookie(token: string): string | null {
  try {
    const name = `${widgetSidCookieName(token)}=`;
    for (const part of document.cookie.split("; ")) {
      if (part.startsWith(name)) return decodeURIComponent(part.slice(name.length));
    }
  } catch { /* malformed cookie value or cookies unavailable */ }
  return null;
}

function writeWidgetSidCookie(token: string, sid: string) {
  try {
    document.cookie = `${widgetSidCookieName(token)}=${encodeURIComponent(sid)}; path=/; max-age=31536000; samesite=lax; secure; partitioned`;
  } catch { /* cookies unavailable (e.g. blocked in third-party iframe) */ }
}

function WidgetInner() {
  const params = useSearchParams();
  const token = params.get("t") || "";
  // API base is fixed at build time (see FIXED_API_BASE above); the old ?api=
  // query override was removed in the security-audit remediation.
  const apiBase = FIXED_API_BASE;
  const langParam = params.get("lang");
  const lang: Lang = langParam === "en" ? "en" : langParam === "zh" ? "zh" : "km";
  const colorParam = params.get("color") || "";
  const s = STR[lang];

  const [cfg, setCfg] = React.useState<WidgetCfg | null>(null);
  const [messages, setMessages] = React.useState<Msg[]>([]);
  const [draft, setDraft] = React.useState("");
  const [sessionId, setSessionId] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const nextId = React.useRef(0);
  const lastSeenDbId = React.useRef(0);
  // Streaming reply accumulator. It is a ref, not state and not a captured `let`:
  // it is not render data (only the messages we mirror into are), and a mutated
  // closure variable is what the React compiler refuses.
  const streamAccRef = React.useRef("");

  // The theme is DERIVED, not synced. It used to be state written from an
  // effect, which cost a second render whenever the config arrived. A media
  // query is an external store — what useSyncExternalStore is for — and its
  // server snapshot is a fixed false, so the prerender stays deterministic.
  const systemDark = React.useSyncExternalStore(
    React.useCallback((onChange: () => void) => {
      const mq = window.matchMedia("(prefers-color-scheme: dark)");
      mq.addEventListener("change", onChange);
      return () => mq.removeEventListener("change", onChange);
    }, []),
    () => window.matchMedia("(prefers-color-scheme: dark)").matches,
    () => false,
  );
  const widgetTheme = cfg?.theme ?? "light";
  const dark = widgetTheme === "dark" || (widgetTheme === "auto" && systemDark);

  const accent = cfg?.primary_color || colorParam || "#4f46e5";
  const p = dark
    ? {
        bg: "#17181c", bubbleIn: "#26272b", bubbleInText: "#e8e8ea", bubbleOutText: "#fff",
        border: "#2e2f34", muted: "#9a9ba1", inputBg: "#1f2024", inputText: "#e8e8ea",
        sysBg: "#3a1d1d", sysText: "#fca5a5", chipBg: "#1f2024",
      }
    : {
        bg: "#ffffff", bubbleIn: "#f1f1f4", bubbleInText: "#222", bubbleOutText: "#fff",
        border: "#eee", muted: "#8a8a90", inputBg: "#fff", inputText: "#222",
        sysBg: "#fef2f2", sysText: "#b91c1c", chipBg: "#fff",
      };

  // Tenant branding (name/theme/color/greeting/suggestions) lives server-side
  // on the embed token; the page is useless-branded without it.
  React.useEffect(() => {
    if (!apiBase || !token) return;
    let dead = false;
    fetch(`${apiBase}/widget/config?token=${encodeURIComponent(token)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => { if (!dead && j) setCfg(j as WidgetCfg); })
      .catch(() => { /* fall back to query params + defaults */ });
    return () => { dead = true; };
  }, [apiBase, token]);


  React.useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, busy]);

  // Persist the session id per token so a reload continues the conversation.
  React.useEffect(() => {
    let saved: string | null = null;
    try {
      saved = localStorage.getItem(`khmer-widget-sid:${token}`);
    } catch { /* ignore */ }
    if (!saved) saved = readWidgetSidCookie(token);
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (saved && SID_RE.test(saved)) setSessionId(saved);
  }, [token]);

  // Fetch the persisted transcript (gives real dbIds + agent replies).
  //
  // 合并语义 (不能整体替换): 后端先发 session 事件 → INSERT 访客消息 →
  // 生成结束后才落库 AI 回复. 所以 sessionId 一变化触发的同步与 6 秒轮询
  // 常拿到 "只有访客消息" 的快照; 早期实现直接 setMessages(mapped) 会把
  // 正在流式输出的 pending 气泡整体删掉, 之后到达的 token 因找不到 pendingId
  // 被丢弃, 直到答完才一次性出现.
  //
  // 现在的规则:
  //   * 服务端已有同 dbId 的本地气泡 → 以服务端为准刷新 (保留本地 id 以免
  //     键变化导致重挂载闪烁; rating 沿用 r.feedback_rating ?? null 原语义);
  //   * 本地无 dbId 的消息 → 先按 (role, content) 与尚未被认领的服务端行配对
  //     (流结束后本地气泡可能只带 dbId 而 content 仍是流式文本);
  //   * 仍未配对的本地消息 (pending 气泡、错误提示) 保留原位;
  //   * 服务端有、本地还没有的其余消息 (agent 回复等) 追加到末尾。
  const syncMessages = React.useCallback(async (sid: string) => {
    if (!apiBase) return;
    try {
      const res = await fetch(`${apiBase}/widget/messages?token=${encodeURIComponent(token)}&session=${encodeURIComponent(sid)}&limit=200`);
      if (!res.ok) return;
      const rows = (await res.json()) as { message_id: number; role: string; content: string; feedback_rating?: number | null }[];
      const mapped: Msg[] = rows
        .filter((r) => r.role !== "system")
        .map((r) => ({
          id: r.message_id, dbId: r.message_id,
          role: (r.role === "user" ? "user" : "model") as Msg["role"],
          content: r.content, rating: r.feedback_rating ?? null,
        }));
      // Unread ping for the host-page launcher badge: an inbound (model/
      // agent) message arrived that the visitor hasn't seen rendered yet.
      const fresh = mapped.some((m) => m.role === "model" && (m.dbId ?? 0) > lastSeenDbId.current && lastSeenDbId.current > 0);
      const maxDb = mapped.reduce((mx, m) => Math.max(mx, m.dbId ?? 0), 0);
      if (fresh) {
        // Only to the direct parent: ancestorOrigins[0] is the nearest
        // ancestor's origin (Chrome/Safari). Directly-opened widgets and old
        // browsers get no list → fall back to "*" (the receiver in
        // widget-embed.js still checks ev.source).
        const targetOrigin = window.location.ancestorOrigins?.[0] || "*";
        try { window.parent.postMessage({ khmerWidgetUnread: true }, targetOrigin); } catch { /* ignore */ }
      }
      lastSeenDbId.current = Math.max(lastSeenDbId.current, maxDb);
      setMessages((cur) => {
        const byDbId = new Map<number, Msg>();
        for (const row of mapped) byDbId.set(row.dbId as number, row);
        // 1) 服务端已有同 dbId 的本地气泡 → 用服务端行刷新.
        const merged: Msg[] = cur.map((m) => {
          if (m.dbId == null) return m;
          const row = byDbId.get(m.dbId);
          if (!row) return m;
          byDbId.delete(m.dbId);
          return { ...m, role: row.role, content: row.content, rating: row.rating };
        });
        // 2) 本地无 dbId 的访客消息与未被认领的服务端行按 (role, content) 配对.
        for (let i = 0; i < merged.length; i += 1) {
          const m = merged[i];
          if (m.dbId != null || m.role === "system") continue;
          for (const row of byDbId.values()) {
            if (row.role !== m.role || row.content !== m.content) continue;
            byDbId.delete(row.dbId as number);
            merged[i] = { ...m, dbId: row.dbId, content: row.content, rating: row.rating, pending: false };
            break;
          }
        }
        // 3) 服务端有、本地还没有的其余消息追加到末尾 (保持服务端顺序).
        return byDbId.size > 0 ? [...merged, ...byDbId.values()] : merged;
      });
    } catch { /* ignore */ }
  }, [apiBase, token]);

  // Initial restore + ongoing agent-reply poll. The synchronous fetch is
  // kicked off async (setInterval fires the first tick after 6s), so the
  // effect body only subscribes to an external system.
  React.useEffect(() => {
    if (!sessionId) return;
    const timer = setInterval(() => void syncMessages(sessionId), 6000);
    return () => clearInterval(timer);
  }, [sessionId, syncMessages]);

  React.useEffect(() => {
    // External sync (fetch persisted transcript); setState happens after await.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (sessionId) void syncMessages(sessionId);
  }, [sessionId, syncMessages]);

  const send = async (text?: string) => {
    const content = (text ?? draft).trim();
    if (!content || busy || !apiBase) return;
    setDraft("");
    const pendingId = nextId.current++;
    setMessages((cur) => [...cur, { id: nextId.current++, role: "user", content }, { id: pendingId, role: "model", content: "", pending: true }]);
    setBusy(true);

    streamAccRef.current = "";
    let sid = sessionId;
    try {
      const res = await fetch(`${apiBase}/widget/chat`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, session_id: sid || undefined, message: content, language: lang }),
      });
      if (!res.ok || !res.body) {
        let msg = `HTTP ${res.status}`;
        try { msg = (await res.json()).error || msg; } catch { /* ignore */ }
        setMessages((cur) => cur.filter((m) => m.id !== pendingId).concat({ id: nextId.current++, role: "system", content: msg }));
        return;
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const raw = buffer.slice(0, sep);
          buffer = buffer.slice(2 + sep);
          const evt = parseLine(raw);
          if (!evt) continue;
          if (evt.event === "session") {
            sid = (evt.data as { session_id: string }).session_id;
            setSessionId(sid);
            try { localStorage.setItem(`khmer-widget-sid:${token}`, sid); } catch { /* ignore */ }
            writeWidgetSidCookie(token, sid);
          } else if (evt.event === "token") {
            streamAccRef.current += (evt.data as { text: string }).text;
            setMessages((cur) => cur.map((m) => (m.id === pendingId ? { ...m, content: streamAccRef.current } : m)));
          } else if (evt.event === "error") {
            streamAccRef.current = (evt.data as { message: string }).message;
            setMessages((cur) => cur.map((m) => (m.id === pendingId ? { ...m, content: streamAccRef.current, pending: false } : m)));
          }
        }
      }
      // Pull dbIds (and agent replies) for the bubbles we just rendered.
      await syncMessages(sid || "");
    } catch {
      setMessages((cur) => cur.filter((m) => m.id !== pendingId));
    } finally {
      setBusy(false);
    }
  };

  const rate = async (m: Msg, rating: 1 | -1) => {
    if (!m.dbId || !apiBase) return;
    // 乐观更新: 失败必须回滚, 否则访客看到 👍/👎 已选中、运营侧却收不到反馈.
    const prevRating = m.rating ?? null;
    setMessages((cur) => cur.map((x) => (x.id === m.id ? { ...x, rating } : x)));
    try {
      const res = await fetch(`${apiBase}/widget/feedback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, message_id: m.dbId, rating }),
      });
      // 服务端反馈端点没有撤销语义, 所以回滚只还原本地显示: 访客可以再点一次,
      // 重新提交同一条反馈. (widget 保持自包含, 不引入 app 的 toast 主题. )
      if (!res.ok) setMessages((cur) => cur.map((x) => (x.id === m.id ? { ...x, rating: prevRating } : x)));
    } catch {
      setMessages((cur) => cur.map((x) => (x.id === m.id ? { ...x, rating: prevRating } : x)));
    }
  };

  if (!token || !apiBase) {
    return <div style={{ padding: 16, fontFamily: "system-ui", color: "#666", fontSize: 13 }}>Widget not configured.</div>;
  }

  const title = cfg?.name || s.title;
  const greeting = (lang === "en" ? cfg?.greeting_en : lang === "zh" ? (cfg?.greeting_en || cfg?.greeting_km) : cfg?.greeting_km) || s.intro;
  const chips = cfg?.suggested_questions ?? [];
  const showOpeners = messages.length === 0;
  // Only the newest AI answer offers 好评/差评. Leaving the controls on every
  // older answer clutters the history and lets a visitor rate the wrong
  // message; a rating that was already given still shows on its own bubble.
  // 注意: 这里之前是 React.useMemo, 但它位于上面 `!token` 提前 return 之后 —
  // 若同一实例先以空 token 渲染一帧、随后 token 变为非空, hook 数量就会变化,
  // React 会抛 "Rendered more hooks than during the previous render" 并卸载整棵
  // 子树 (访客 iframe 白屏)。这是一次 O(n) 的反向遍历, 直接内联计算即可,
  // 组件内所有 hook 因此都恒定保持在提前 return 之前。
  let lastModelKey: number | null = null;
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    if (messages[i].role === "model" && messages[i].dbId != null) { lastModelKey = messages[i].id; break; }
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh", background: p.bg, color: p.bubbleInText, fontFamily: "'Noto Sans Khmer','Inter',system-ui,sans-serif" }}>
      <style>{WIDGET_CSS}</style>

      {/* Header — tenant-branded, Crisp-style presence */}
      <div style={{ padding: "12px 16px", background: accent, color: "#fff", display: "flex", alignItems: "center", gap: 10 }}>
        <div style={{ width: 34, height: 34, borderRadius: "50%", background: "rgba(255,255,255,0.22)", display: "flex", alignItems: "center", justifyContent: "center", fontSize: 15, fontWeight: 700 }}>
          {title.trim().charAt(0).toUpperCase()}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 14, fontWeight: 600, lineHeight: 1.25, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{title}</div>
          <div style={{ fontSize: 11, opacity: 0.92, display: "flex", alignItems: "center", gap: 5 }}>
            <span className="kw-dot" /> {s.online}
          </div>
        </div>
      </div>

      <div ref={scrollRef} style={{ flex: 1, overflowY: "auto", padding: "14px 14px 8px", display: "flex", flexDirection: "column", gap: 8 }}>
        {showOpeners && (
          <div className="kw-msg" style={{ alignSelf: "flex-start", background: p.bubbleIn, color: p.bubbleInText, padding: "10px 12px", borderRadius: "4px 12px 12px 12px", fontSize: 14, maxWidth: "85%", lineHeight: 1.5 }}>
            {greeting}
          </div>
        )}
        {showOpeners && chips.length > 0 && (
          <div style={{ display: "flex", flexDirection: "column", gap: 6, alignItems: "flex-start" }}>
            {chips.map((q) => (
              <button
                key={q}
                type="button"
                className="kw-chip"
                onClick={() => void send(q)}
                style={{ border: `1px solid ${p.border}`, background: p.chipBg, color: accent, borderRadius: 16, padding: "7px 12px", fontSize: 13, cursor: "pointer", textAlign: "left", maxWidth: "90%" }}
              >
                {q}
              </button>
            ))}
          </div>
        )}
        {messages.map((m) => (
          <div key={m.id} className="kw-msg" style={{ display: "flex", flexDirection: "column", alignItems: m.role === "user" ? "flex-end" : "flex-start" }}>
            <div style={{
              background: m.role === "user" ? accent : m.role === "system" ? p.sysBg : p.bubbleIn,
              color: m.role === "user" ? p.bubbleOutText : m.role === "system" ? p.sysText : p.bubbleInText,
              padding: "9px 12px", borderRadius: m.role === "user" ? "12px 4px 12px 12px" : "4px 12px 12px 12px",
              fontSize: 14, maxWidth: "85%", whiteSpace: "pre-wrap", wordBreak: "break-word", lineHeight: 1.5,
              boxShadow: "0 1px 2px rgba(0,0,0,0.06)",
            }}>
              {m.pending && !m.content
                ? <span className="kw-typing" style={{ display: "inline-flex", alignItems: "center", height: 14 }}><span /><span /><span /></span>
                : m.content}
            </div>
            {m.role === "model" && !m.pending && (
              <div style={{ marginTop: 3, display: "flex", alignItems: "center", gap: 8, fontSize: 13 }}>
                <span style={{ fontSize: 10, fontWeight: 600, letterSpacing: 0.4, color: p.muted, display: "inline-flex", alignItems: "center", gap: 3 }}>
                  <svg width="10" height="10" viewBox="0 0 24 24" fill={accent} aria-hidden="true"><path d="M12 2l2.1 6.2L20 10l-5.9 1.8L12 18l-2.1-6.2L4 10l5.9-1.8z" /></svg>
                  {s.ai}
                </span>
                {m.dbId != null && (
                  m.rating ? (
                    <span style={{ color: m.rating === 1 ? "#16a34a" : "#dc2626" }}>{m.rating === 1 ? "👍" : "👎"}</span>
                  ) : m.id === lastModelKey ? (
                    <>
                      <button type="button" aria-label="good" style={{ border: "none", background: "transparent", cursor: "pointer", padding: 0 }} onClick={() => void rate(m, 1)}>👍</button>
                      <button type="button" aria-label="bad" style={{ border: "none", background: "transparent", cursor: "pointer", padding: 0 }} onClick={() => void rate(m, -1)}>👎</button>
                    </>
                  ) : null
                )}
              </div>
            )}
          </div>
        ))}
      </div>

      <div style={{ borderTop: `1px solid ${p.border}`, padding: 10, display: "flex", gap: 8, background: p.bg }}>
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && !e.nativeEvent.isComposing) void send(); }}
          placeholder={s.placeholder}
          disabled={busy}
          style={{ flex: 1, border: `1px solid ${p.border}`, background: p.inputBg, color: p.inputText, borderRadius: 20, padding: "8px 14px", fontSize: 14, outline: "none" }}
        />
        <button type="button" onClick={() => void send()} disabled={busy || !draft.trim()}
          style={{ border: "none", background: accent, color: "#fff", borderRadius: 20, padding: "0 16px", fontSize: 14, cursor: "pointer", opacity: busy || !draft.trim() ? 0.6 : 1 }}>
          {s.send}
        </button>
      </div>
      <div style={{ textAlign: "center", fontSize: 10, color: p.muted, paddingBottom: 4 }}>{s.powered}</div>
    </div>
  );
}

export default function WidgetPage() {
  return (
    <Suspense fallback={<div style={{ padding: 16 }}>…</div>}>
      <WidgetInner />
    </Suspense>
  );
}
