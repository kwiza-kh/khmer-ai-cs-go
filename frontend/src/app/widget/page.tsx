"use client";

// Public website-chat widget UI. Rendered inside the iframe opened by
// widget-embed.js. Authenticated solely by the ?t= embed token; no JWT.
import * as React from "react";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";

type Msg = {
  id: number;
  dbId?: number;      // chat_messages.message_id (present once synced)
  role: "user" | "model" | "system";
  content: string;
  rating?: number | null;
  pending?: boolean;
};

const STR = {
  km: {
    title: "客服助手", intro: "您好！有什么可以帮您？", placeholder: "输入消息…",
    send: "发送", powered: "由 Khmer AI 提供", typing: "在线",
  },
  en: {
    title: "Support Assistant", intro: "Hi! How can we help you today?", placeholder: "Type a message…",
    send: "Send", powered: "Powered by Khmer AI", typing: "online",
  },
};

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

function WidgetInner() {
  const params = useSearchParams();
  const token = params.get("t") || "";
  const apiBase = (params.get("api") || "").replace(/\/$/, "");
  const lang = (params.get("lang") === "en" ? "en" : "km") as "km" | "en";
  const accent = params.get("color") || "#4f46e5";
  const s = STR[lang];

  const [messages, setMessages] = React.useState<Msg[]>([]);
  const [draft, setDraft] = React.useState("");
  const [sessionId, setSessionId] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const nextId = React.useRef(0);

  React.useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, busy]);

  // Persist the session id per token so a reload continues the conversation.
  // localStorage is an external store, so reading it in an effect is the
  // sanctioned pattern here (same rationale as auth-client.tsx).
  React.useEffect(() => {
    try {
      const saved = localStorage.getItem(`khmer-widget-sid:${token}`);
      // eslint-disable-next-line react-hooks/set-state-in-effect
      if (saved) setSessionId(saved);
    } catch { /* ignore */ }
  }, [token]);

  // Fetch the persisted transcript (gives real dbIds + agent replies). Skips
  // the optimistic pending bubble and preserves the local id for model turns
  // already shown, matching by order so ratings survive refresh.
  const syncMessages = React.useCallback(async (sid: string) => {
    if (!apiBase) return;
    try {
      const res = await fetch(`${apiBase}/widget/messages?token=${encodeURIComponent(token)}&session=${sid}&limit=200`);
      if (!res.ok) return;
      const rows = (await res.json()) as { message_id: number; role: string; content: string; feedback_rating?: number | null }[];
      const mapped: Msg[] = rows
        .filter((r) => r.role !== "system")
        .map((r) => ({
          id: r.message_id, dbId: r.message_id,
          role: (r.role === "user" ? "user" : "model") as Msg["role"],
          content: r.content, rating: r.feedback_rating ?? null,
        }));
      if (mapped.length) setMessages(mapped);
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

  const send = async () => {
    const content = draft.trim();
    if (!content || busy || !apiBase) return;
    setDraft("");
    const pendingId = nextId.current++;
    setMessages((cur) => [...cur, { id: nextId.current++, role: "user", content }, { id: pendingId, role: "model", content: "", pending: true }]);
    setBusy(true);

    let acc = "";
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
          buffer = buffer.slice(sep + 2);
          const evt = parseLine(raw);
          if (!evt) continue;
          if (evt.event === "session") {
            sid = (evt.data as { session_id: string }).session_id;
            setSessionId(sid);
            try { localStorage.setItem(`khmer-widget-sid:${token}`, sid); } catch { /* ignore */ }
          } else if (evt.event === "token") {
            acc += (evt.data as { text: string }).text;
            setMessages((cur) => cur.map((m) => (m.id === pendingId ? { ...m, content: acc } : m)));
          } else if (evt.event === "error") {
            acc = (evt.data as { message: string }).message;
            setMessages((cur) => cur.map((m) => (m.id === pendingId ? { ...m, content: acc, pending: false } : m)));
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
    setMessages((cur) => cur.map((x) => (x.id === m.id ? { ...x, rating } : x)));
    try {
      await fetch(`${apiBase}/widget/feedback`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, message_id: m.dbId, rating }),
      });
    } catch { /* ignore */ }
  };

  if (!token || !apiBase) {
    return <div style={{ padding: 16, fontFamily: "system-ui", color: "#666", fontSize: 13 }}>Widget not configured.</div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh", background: "#fff", fontFamily: "'Noto Sans Khmer','Inter',system-ui,sans-serif" }}>
      <div style={{ padding: "12px 16px", background: accent, color: "#fff", display: "flex", alignItems: "center", gap: 8 }}>
        <div style={{ width: 28, height: 28, borderRadius: 8, background: "rgba(255,255,255,0.2)", display: "flex", alignItems: "center", justifyContent: "center", fontSize: 14 }}>💬</div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 14, fontWeight: 600, lineHeight: 1.2 }}>{s.title}</div>
          <div style={{ fontSize: 11, opacity: 0.85 }}>● {s.typing}</div>
        </div>
      </div>

      <div ref={scrollRef} style={{ flex: 1, overflowY: "auto", padding: "12px 14px", display: "flex", flexDirection: "column", gap: 8 }}>
        {messages.length === 0 && (
          <div style={{ alignSelf: "flex-start", background: "#f1f1f4", color: "#333", padding: "10px 12px", borderRadius: "4px 12px 12px 12px", fontSize: 14, maxWidth: "85%" }}>{s.intro}</div>
        )}
        {messages.map((m) => (
          <div key={m.id} style={{ display: "flex", flexDirection: "column", alignItems: m.role === "user" ? "flex-end" : "flex-start" }}>
            <div style={{
              background: m.role === "user" ? accent : m.role === "system" ? "#fef2f2" : "#f1f1f4",
              color: m.role === "user" ? "#fff" : m.role === "system" ? "#b91c1c" : "#222",
              padding: "9px 12px", borderRadius: m.role === "user" ? "12px 4px 12px 12px" : "4px 12px 12px 12px",
              fontSize: 14, maxWidth: "85%", whiteSpace: "pre-wrap", wordBreak: "break-word", lineHeight: 1.5,
            }}>
              {m.pending && !m.content ? <span style={{ opacity: 0.6 }}>…</span> : m.content}
            </div>
            {m.role === "model" && !m.pending && m.dbId != null && (
              <div style={{ marginTop: 3, display: "flex", gap: 6, fontSize: 13 }}>
                {m.rating ? (
                  <span style={{ color: m.rating === 1 ? "#16a34a" : "#dc2626" }}>{m.rating === 1 ? "👍" : "👎"}</span>
                ) : (
                  <>
                    <button type="button" aria-label="good" style={{ border: "none", background: "transparent", cursor: "pointer", padding: 0 }} onClick={() => void rate(m, 1)}>👍</button>
                    <button type="button" aria-label="bad" style={{ border: "none", background: "transparent", cursor: "pointer", padding: 0 }} onClick={() => void rate(m, -1)}>👎</button>
                  </>
                )}
              </div>
            )}
          </div>
        ))}
      </div>

      <div style={{ borderTop: "1px solid #eee", padding: 10, display: "flex", gap: 8 }}>
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && !e.nativeEvent.isComposing) void send(); }}
          placeholder={s.placeholder}
          disabled={busy}
          style={{ flex: 1, border: "1px solid #ddd", borderRadius: 20, padding: "8px 14px", fontSize: 14, outline: "none" }}
        />
        <button type="button" onClick={() => void send()} disabled={busy || !draft.trim()}
          style={{ border: "none", background: accent, color: "#fff", borderRadius: 20, padding: "0 16px", fontSize: 14, cursor: "pointer", opacity: busy || !draft.trim() ? 0.6 : 1 }}>
          {s.send}
        </button>
      </div>
      <div style={{ textAlign: "center", fontSize: 10, color: "#aaa", paddingBottom: 4 }}>{s.powered}</div>
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
