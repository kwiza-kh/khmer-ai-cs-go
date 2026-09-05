"use client";

// Lightweight client-side i18n: React context + per-language dictionaries.
// The app is entirely client-rendered behind an auth gate, so URL-based
// locale routing buys nothing; language is a user preference (persisted in
// localStorage "lang" — shared with the login page and Settings, and kept in
// sync with the backend AI-language preference).

import {
  createContext, useCallback, useContext, useEffect, useMemo, useState,
  type ReactNode,
} from "react";

import type { Dict, Lang } from "./types";
import { en } from "./dict-en";
import { zh } from "./dict-zh";
import { km } from "./dict-km";

export type { Lang, Dict };

export const LANGS: { key: Lang; label: string; full: string }[] = [
  { key: "km", label: "ខ្មែរ", full: "ភាសាខ្មែរ" },
  { key: "en", label: "EN", full: "English" },
  { key: "zh", label: "中文", full: "简体中文" },
];

const STORAGE_KEY = "lang";

export function detectLang(): Lang {
  if (typeof window === "undefined") return "en";
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved === "km" || saved === "en" || saved === "zh") return saved;
  const nav = navigator.language?.toLowerCase() ?? "";
  if (nav.startsWith("zh")) return "zh";
  if (nav.startsWith("km")) return "km";
  return "en";
}

const DICTS: Record<Lang, Dict> = { en, zh, km };

interface I18nValue {
  lang: Lang;
  setLang: (lang: Lang) => void;
  /** Translate a key; falls back to English, then the key itself. */
  t: (key: string) => string;
  /** Translate with {param} interpolation: tf("inbox.messagesCount", { n: 3 }). */
  tf: (key: string, params: Record<string, string | number>) => string;
}

const I18nContext = createContext<I18nValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  // Start at "en" for SSR parity; resolve the real language after mount.
  // Pages behind the auth gate only render once mounted, so they never
  // flash; the login page switches once (same as before).
  const [lang, setLangState] = useState<Lang>("en");

  // localStorage/浏览器语言是外部存储, 只能在客户端挂载后读取 (SSR 期间没有
  // window). 与 auth-client.tsx 相同的 "从外部系统同步状态" 模式, 显式豁免.
  useEffect(() => {
    const resolved = detectLang();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLangState(resolved);
    document.documentElement.lang = resolved;
  }, []);

  const setLang = useCallback((next: Lang) => {
    localStorage.setItem(STORAGE_KEY, next);
    document.documentElement.lang = next;
    setLangState(next);
  }, []);

  const t = useCallback(
    (key: string) => DICTS[lang][key] ?? en[key] ?? key,
    [lang],
  );

  const tf = useCallback(
    (key: string, params: Record<string, string | number>) => {
      let text = DICTS[lang][key] ?? en[key] ?? key;
      for (const [name, value] of Object.entries(params)) {
        text = text.replaceAll(`{${name}}`, String(value));
      }
      return text;
    },
    [lang],
  );

  const value = useMemo(() => ({ lang, setLang, t, tf }), [lang, setLang, t, tf]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used within I18nProvider");
  return ctx;
}
