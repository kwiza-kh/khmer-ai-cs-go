"use client";

import { useEffect, useRef, useState } from "react";
import { API_BASE } from "./auth-client";

export interface InboxRealtimeEvent {
  type: "inbox.message" | "inbox.session" | "inbox.notification";
  session_id: string;
  message_id?: number;
  role?: string;
  occurred_at: string;
}

function inboxWebSocketURL(): string {
  const base = /^https?:\/\//.test(API_BASE) ? API_BASE : `${window.location.origin}${API_BASE}`;
  const url = new URL(base);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = `${url.pathname.replace(/\/api\/v1\/?$/, "")}/api/v1/realtime/inbox`;
  url.search = "";
  url.hash = "";
  return url.toString();
}

export function useInboxRealtime(token: string | null, onEvent: (event: InboxRealtimeEvent) => void) {
  const onEventRef = useRef(onEvent);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    if (!token) return;

    let socket: WebSocket | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectAttempts = 0;
    let stopped = false;

    const connect = () => {
      // Bind handlers to THIS socket instance — a late error from a stale
      // socket must not close the freshly reconnected one.
      const s = new WebSocket(inboxWebSocketURL(), ["khmer-ai-cs", token]);
      socket = s;
      s.onopen = () => {
        reconnectAttempts = 0;
        setConnected(true);
        // Catch-up: events missed while the socket was down are gone forever —
        // nudge the page to refetch immediately instead of waiting for the
        // next fallback poll.
        onEventRef.current({
          type: "inbox.session",
          session_id: "",
          occurred_at: new Date().toISOString(),
        });
      };
      s.onmessage = (message) => {
        try {
          const event = JSON.parse(message.data) as InboxRealtimeEvent;
          if (event.type === "inbox.message" || event.type === "inbox.session" || event.type === "inbox.notification") {
            onEventRef.current(event);
          }
        } catch {
          return;
        }
      };
      s.onerror = () => s.close();
      s.onclose = () => {
        setConnected(false);
        if (stopped) return;
        const delay = Math.min(1000 * 2 ** reconnectAttempts, 30000);
        reconnectAttempts += 1;
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

  return connected;
}
