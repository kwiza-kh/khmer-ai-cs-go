"use client";

import * as React from "react";
import { API_BASE } from "./auth-client";

export interface InboxRealtimeEvent {
  type: "inbox.message" | "inbox.session" | "inbox.notification";
  session_id: string;
  message_id?: number;
  role?: string;
  occurred_at: string;
}

type RealtimeListener = (event: InboxRealtimeEvent) => void;

interface RealtimeContextValue {
  /** True while the socket is open. Callers poll slower when it is. */
  connected: boolean;
  subscribe: (listener: RealtimeListener) => () => void;
}

const RealtimeContext = React.createContext<RealtimeContextValue | null>(null);

// inboxWebSocketURL returns the socket URL, or "" when API_BASE cannot be
// parsed. A malformed NEXT_PUBLIC_API_URL must not break the page: realtime is an
// auxiliary path, so it degrades to "no socket" and the callers keep polling.
// (next.config.ts accepts a non-absolute value with a warning, so a relative
// base — which `new URL` cannot parse without a second argument — is reachable.)
function inboxWebSocketURL(): string {
  const base = /^https?:\/\//.test(API_BASE) ? API_BASE : `${window.location.origin}${API_BASE}`;
  try {
    const url = new URL(base);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.pathname = `${url.pathname.replace(/\/api\/v1\/?$/, "")}/api/v1/realtime/inbox`;
    url.search = "";
    url.hash = "";
    return url.toString();
  } catch {
    return "";
  }
}

const KNOWN_EVENTS = new Set(["inbox.message", "inbox.session", "inbox.notification"]);

/**
 * One socket per tab, shared by everything that needs realtime events.
 *
 * The sidebar needs the connection on every page, and the inbox and the handoff
 * queue each used to open a second one — two connections per tab on those
 * screens, with two independent reconnect loops and two file-wide `window.__`
 * globals for crossing the gap. A provider removes both: the connection lives
 * here, subscribers receive events, and the polling intervals (which stay as a
 * safety net) can read `connected` to decide how slow they are allowed to be.
 *
 * Events missed while the socket is down are gone — the hub has no replay — so
 * every successful (re)connect dispatches a synthetic `inbox.session` to make
 * subscribers refetch once, immediately, instead of waiting out the fallback
 * poll.
 *
 * Reconnects are BOUNDED. The browser tells a page nothing about why a WebSocket
 * handshake failed, so a rejected token looks exactly like a network blip — and
 * production showed what unlimited retries cost: one client reconnecting every
 * ~30s for hours, 214 of 240 handshakes answered 401. After quickRetryLimit tries
 * the provider gives up and stays disconnected, which tightens the fallback polls
 * to their fast cadence; a reload or a fresh session starts a new budget.
 */
const quickRetryLimit = 6
export function RealtimeProvider({ token, children }: { token: string | null; children: React.ReactNode }) {
  const [connected, setConnected] = React.useState(false);
  const listeners = React.useRef(new Set<RealtimeListener>());

  const subscribe = React.useCallback((listener: RealtimeListener) => {
    listeners.current.add(listener);
    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  React.useEffect(() => {
    if (!token) return;

    let socket: WebSocket | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectAttempts = 0;
    let stopped = false;

    const dispatch = (event: InboxRealtimeEvent) => {
      for (const listener of listeners.current) listener(event);
    };

    const connect = () => {
      const url = inboxWebSocketURL();
      if (!url) return; // unparseable base: no socket, callers poll
      // Bind handlers to THIS socket instance — a late error from a stale
      // socket must not close the freshly reconnected one.
      const s = new WebSocket(url, ["relaychat", token]);
      socket = s;
      s.onopen = () => {
        reconnectAttempts = 0;
        setConnected(true);
        dispatch({ type: "inbox.session", session_id: "", occurred_at: new Date().toISOString() });
      };
      s.onmessage = (message) => {
        try {
          const event = JSON.parse(message.data) as InboxRealtimeEvent;
          if (KNOWN_EVENTS.has(event.type)) dispatch(event);
        } catch {
          return;
        }
      };
      s.onerror = () => s.close();
      s.onclose = () => {
        setConnected(false);
        if (stopped) return;
        reconnectAttempts += 1;
        if (reconnectAttempts > quickRetryLimit) {
          // Given up: the polls carry the UI from here (they tighten to their
          // fast cadence while disconnected), and a reload or a new session
          // starts a fresh budget.
          return;
        }
        const delay = Math.min(1000 * 2 ** (reconnectAttempts - 1), 60_000);
        reconnectTimer = setTimeout(connect, delay);
      };
    };

    connect();
    return () => {
      stopped = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      socket?.close();
    };
  }, [token]);

  const value = React.useMemo<RealtimeContextValue>(() => ({ connected, subscribe }), [connected, subscribe]);
  return <RealtimeContext.Provider value={value}>{children}</RealtimeContext.Provider>;
}

/**
 * Whether the shared socket is up. Outside a provider this is false, which makes
 * subscribers fall back to the fast poll — the safe direction when realtime is
 * unavailable.
 */
export function useRealtimeConnected(): boolean {
  return React.useContext(RealtimeContext)?.connected ?? false;
}

/** Subscribe to realtime events for as long as the component is mounted. */
export function useRealtimeEvent(onEvent: RealtimeListener): void {
  const ctx = React.useContext(RealtimeContext);
  const onEventRef = React.useRef(onEvent);
  React.useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);
  React.useEffect(() => {
    if (!ctx) return;
    return ctx.subscribe((event) => onEventRef.current(event));
  }, [ctx]);
}
