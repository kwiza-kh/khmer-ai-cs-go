"use client";

import { createContext, useContext, useState, useEffect, ReactNode } from "react";
import { useRouter } from "next/navigation";

interface User {
  user_id: number;
  username: string;
  email: string;
  role: "user" | "admin" | "platform_admin";
  /**
   * Whether this account owns its tenant. The role above cannot answer it:
   * self-service and SSO signups own their tenant with role "user". Server-
   * provided on login/register; GET /profile re-reports it so a session that
   * predates this field still gets the answer without re-login.
   */
  is_tenant_owner?: boolean;
}

interface AuthContextType {
  user: User | null;
  token: string | null;
  /** Resolves { twoFactorRequired: true } (without throwing) when the account
   *  has 2FA enabled and no/blank code was sent — the login form then shows
   *  its TOTP field and retries with the code. */
  login: (username: string, password: string, totpCode?: string) => Promise<{ twoFactorRequired?: boolean }>;
  register: (username: string, email: string, password: string) => Promise<void>;
  /** Exchange a one-time Google sign-in code (from ?google_code=) for a session. */
  completeGoogleLogin: (code: string, state: string) => Promise<void>;
  /** Exchange a one-time Telegram sign-in code (from ?telegram_code=). */
  completeTelegramLogin: (code: string, state: string) => Promise<void>;
  logout: () => void;
  isLoading: boolean;
}

import { localizeCurrentLang } from "./api-errors";

const AuthContext = createContext<AuthContextType | null>(null);

import { settle } from "./safe-fetch";

export const API_BASE = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";

/**
 * Called by apiFetch (and any raw fetch 401) when the JWT turns out to be
 * expired/invalid. Signals the provider so it logs out the in-memory auth
 * state — AuthGuard then redirects to /login immediately. Previously only
 * localStorage was cleared, leaving the UI "logged in" until a reload.
 */
export function signalAuthExpired() {
  localStorage.removeItem("token");
  localStorage.removeItem("user");
  window.dispatchEvent(new Event("khmer:auth-expired"));
}

/**
 * Persist a token returned by an endpoint that changed a credential.
 *
 * The server bumps the account's token version on password and 2FA changes,
 * which retires every session issued with the old version — including the one
 * making the request. Those endpoints return a freshly signed token for the
 * current session so the user isn't signed out by their own action. Call this
 * with the response body; a missing/absent token is a no-op (the session will
 * simply end at the next request).
 *
 * localStorage alone is not enough: every in-memory consumer (notably the
 * realtime WebSocket in lib/realtime.ts, which authenticates with the
 * AuthProvider token) would keep using the retired JWT and fail to reconnect
 * forever. So — mirroring signalAuthExpired — broadcast the new token and let
 * AuthProvider sync its state.
 */
export function adoptRefreshedToken(res: { token?: string } | null | undefined) {
  if (!res?.token) return;
  localStorage.setItem("token", res.token);
  window.dispatchEvent(new CustomEvent<string>("khmer:token-refreshed", { detail: res.token }));
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const router = useRouter();

  // localStorage 是外部存储, 必须在客户端挂载后才能读取 (SSR 期间 window 不存在).
  // 这是 React 官方推荐的 "从外部系统同步状态" 用法, 因此显式关闭 set-state-in-effect 规则.
  useEffect(() => {
    const savedToken = localStorage.getItem("token");
    const savedUser = localStorage.getItem("user");
    if (savedToken && savedUser) {
      try {
        const parsed = JSON.parse(savedUser) as User;
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setToken(savedToken);
        setUser(parsed);
      } catch {
        // localStorage 中的 user 数据损坏 (例如旧版本格式), 清理掉避免后续崩溃.
        localStorage.removeItem("user");
        localStorage.removeItem("token");
      }
    }
    setIsLoading(false);
  }, []);

  const login = async (username: string, password: string, totpCode?: string): Promise<{ twoFactorRequired?: boolean }> => {
    const res = await settle(fetch(`${API_BASE}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password, ...(totpCode ? { totp_code: totpCode } : {}) }),
    }));
    const data = await res.json().catch(() => ({}));
    if (res.status === 401 && data?.action === "2fa_required") {
      return { twoFactorRequired: true };
    }
    if (!res.ok) throw new Error(localizeCurrentLang(data.error || "登录失败"));
    setToken(data.token);
    setUser(data.user);
    localStorage.setItem("token", data.token);
    localStorage.setItem("user", JSON.stringify(data.user));
    return {};
  };

  const register = async (username: string, email: string, password: string) => {
    const res = await settle(fetch(`${API_BASE}/auth/register`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, email, password }),
    }));
    const data = await res.json();
    if (!res.ok) throw new Error(localizeCurrentLang(data.error || "注册失败"));
    setToken(data.token);
    setUser(data.user);
    localStorage.setItem("token", data.token);
    localStorage.setItem("user", JSON.stringify(data.user));
  };

  // completeGoogleLogin — the OAuth callback redirects back with a one-time
  // code; swap it for the JWT payload (single use, 2-minute TTL server-side).
  const completeGoogleLogin = async (code: string, state: string) => {
    const res = await settle(fetch(`${API_BASE}/auth/google/exchange`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code, state }),
    }));
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(localizeCurrentLang(data.error || "Google 登录失败"));
    setToken(data.token);
    setUser(data.user);
    localStorage.setItem("token", data.token);
    localStorage.setItem("user", JSON.stringify(data.user));
  };

  // completeTelegramLogin — same one-time-code exchange as Google, against the
  // Telegram (OIDC) callback. Telegram supplies no e-mail address, so the user
  // payload carries an empty one.
  const completeTelegramLogin = async (code: string, state: string) => {
    const res = await settle(fetch(`${API_BASE}/auth/telegram/exchange`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code, state }),
    }));
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(localizeCurrentLang(data.error || "Telegram 登录失败"));
    setToken(data.token);
    setUser(data.user);
    localStorage.setItem("token", data.token);
    localStorage.setItem("user", JSON.stringify(data.user));
  };

  const logout = () => {
    setToken(null);
    setUser(null);
    localStorage.removeItem("token");
    localStorage.removeItem("user");
    router.push("/login");
  };

  // 任何页面收到 401 (JWT 过期/失效) 时联动登出: signalAuthExpired 广播事件,
  // 这里执行真正的状态清空 + 跳转登录页.
  useEffect(() => {
    const onAuthExpired = () => logout();
    window.addEventListener("khmer:auth-expired", onAuthExpired);
    return () => window.removeEventListener("khmer:auth-expired", onAuthExpired);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 改密码 / 改 2FA 后服务端会让旧 token_version 失效并下发新 JWT:
  // adoptRefreshedToken 广播新 token, 这里同步内存态, 否则 WebSocket 会一直用
  // 死 token 无限重连失败 (只能等手动刷新页面).
  useEffect(() => {
    const onTokenRefreshed = (e: Event) => {
      const next = (e as CustomEvent<string>).detail;
      if (typeof next === "string" && next) setToken(next);
    };
    window.addEventListener("khmer:token-refreshed", onTokenRefreshed);
    return () => window.removeEventListener("khmer:token-refreshed", onTokenRefreshed);
  }, []);

  return (
    <AuthContext.Provider value={{ user, token, login, register, completeGoogleLogin, completeTelegramLogin, logout, isLoading }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
