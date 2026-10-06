"use client";

import Link from "next/link";
import * as React from "react";
import { usePathname, useRouter } from "next/navigation";
import useSWR from "swr";
import { motion } from "motion/react";
import { useAuth } from "@/lib/auth-client";
import { listHumanHandoffRequests, listInbox, listKnowledge, getProfile } from "@/lib/api";
import type { InboxItem, KnowledgeDocument } from "@/lib/api";
import { getBillingCatalog } from "@/lib/billing-api";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { RelayChatLogo } from "@/components/relaychat-logo";
import { cn } from "@/lib/utils";
import {
  BookOpen, Globe, Users, Settings, LogOut, Menu, Search, Loader2,
  Gauge, HelpCircle, Inbox, UserCheck, ShieldCheck, Coins, Cpu, FlaskConical,
  MessageCircle, ChevronRight, Bot, type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import { ThemeToggle } from "@/components/theme-toggle";
import { NotificationBell } from "@/components/notification-bell";
import { LanguageSwitcher } from "@/components/language-switcher";
import { useI18n } from "@/lib/i18n";
import { useInboxRealtime } from "@/lib/realtime";

/**
 * Sidebar nav row. Borrows nav-list-card's spring micro-interaction from the
 * Spectrum UI registry (whileHover x / whileTap scale) but keeps this app's
 * token-driven active state — the registry component hardcodes neutral colors,
 * which would break the light/dark contract documented in docs/DEVELOPMENT.md.
 */
function NavItem({
  href,
  icon: Icon,
  label,
  active,
  onNavigate,
  badge,
  count,
}: {
  href: string;
  icon: LucideIcon;
  label: string;
  active: boolean;
  onNavigate?: () => void;
  badge?: string;
  /** Unread-style numeric pill (e.g. open human-handoff requests). */
  count?: number;
}) {
  return (
    <motion.div
      whileHover={{ x: 2 }}
      whileTap={{ scale: 0.985 }}
      transition={{ type: "spring", bounce: 0.35, duration: 0.3 }}
    >
      <Link
        href={href}
        onClick={onNavigate}
        className={cn(
          "group flex items-center gap-2.5 rounded-[10px] px-3 py-2 text-[13px] transition-colors",
          active
            ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground ring-1 ring-foreground/[0.07] shadow-[0_1px_2px_rgb(0_0_0/0.04)]"
            : "font-medium text-sidebar-foreground/70 hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground"
        )}
      >
        <Icon className={cn("size-4 shrink-0 transition-colors", active ? "text-sidebar-primary" : "text-sidebar-foreground/60 group-hover:text-sidebar-accent-foreground")} />
        <span className="flex-1 truncate">{label}</span>
        {count != null && count > 0 && (
          <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[11px] font-bold tabular-nums text-white">
            {count > 99 ? "99+" : count}
          </span>
        )}
        {badge && (
          <span className="rounded-full bg-primary px-1.5 py-0.5 text-[10px] font-semibold text-primary-foreground">
            {badge}
          </span>
        )}
      </Link>
    </motion.div>
  );
}

/** Uppercase group label — the reference dashboard's rail has plain section
 *  captions rather than collapsible trees. */
function NavSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="pt-5">
      <p className="px-3 pb-1.5 text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/70">
        {label}
      </p>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

const ROLE_KEYS: Record<string, string> = {
  platform_admin: "role.platformAdmin",
  admin: "role.admin",
  user: "role.member",
};

/** Pathname → breadcrumb labels. Longest prefix wins. */
const BREADCRUMBS: { match: string; section: string; page: string }[] = [
  { match: "/handoff-requests", section: "nav.groupWorkspace", page: "nav.humanRequests" },
  { match: "/ai-test", section: "nav.groupWorkspace", page: "nav.aiTest" },
  { match: "/inbox", section: "nav.groupWorkspace", page: "nav.inbox" },
  { match: "/widget-admin", section: "nav.groupContent", page: "nav.widget" },
  { match: "/knowledge", section: "nav.groupContent", page: "nav.knowledge" },
  { match: "/platforms", section: "nav.groupContent", page: "nav.platforms" },
  { match: "/admin/users", section: "nav.administration", page: "nav.users" },
  { match: "/admin/models", section: "nav.administration", page: "nav.models" },
  { match: "/admin/tokens", section: "nav.administration", page: "nav.tokens" },
  { match: "/admin", section: "nav.administration", page: "nav.dashboard" },
  { match: "/platform-admin", section: "nav.superAdmin", page: "nav.platformAdmin" },
  { match: "/billing", section: "nav.preferences", page: "bl.title" },
  { match: "/settings", section: "nav.preferences", page: "nav.settings" },
  { match: "/help", section: "nav.preferences", page: "nav.help" },
];

/** Brand mark + wordmark. The mark renders twice (sidebar + mobile header); RelayChatLogo scopes its gradient id per instance. */
function Brand({ iconOnly = false }: { iconOnly?: boolean }) {
  return (
    <Link href="/inbox" className="group flex items-center gap-2">
      <span className="relative flex size-7 shrink-0 items-center justify-center">
        <span aria-hidden className="absolute inset-0 rounded-lg bg-brand/30 blur-md opacity-60 transition-opacity group-hover:opacity-100" />
        <RelayChatLogo gradient className="relative size-6 transition-transform duration-300 group-hover:scale-105 group-hover:rotate-3" />
      </span>
      {!iconOnly && <span className="text-[15px] font-semibold tracking-tight text-foreground">RelayChat</span>}
    </Link>
  );
}

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { user, token, logout } = useAuth();
  const { t } = useI18n();
  const pathname = usePathname();
  const router = useRouter();
  const searchRef = React.useRef<HTMLInputElement>(null);
  const [globalSearch, setGlobalSearch] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  // Who administers this tenant. The role string cannot answer it: self-service
  // and SSO signups own their tenant with role "user" (see isTenantOwner on the
  // backend), and the tenant-scoped admin routes now accept exactly that owner.
  // login/register already carry the flag; this fetch covers a session created
  // before it existed. The narrow inline type is deliberate — the central
  // UserProfile type is not ours to change in this pass.
  const { data: selfProfile } = useSWR(token ? "profile-self" : null, () => getProfile());
  // The field exists on the server (profileFields.IsTenantOwner); the shared
  // UserProfile type has not caught up yet, so narrow it here rather than editing
  // that type in this pass.
  const isTenantOwner =
    (selfProfile as { is_tenant_owner?: boolean } | undefined)?.is_tenant_owner ??
    user?.is_tenant_owner ??
    false;
  const isAdmin = isTenantOwner || user?.role === "admin" || user?.role === "platform_admin";
  const isPlatformAdmin = user?.role === "platform_admin";
  // Which plan the tenant is on, shown on the "plan & billing" label so a
  // merchant sees their tier without opening the page. The same SWR key as
  // /billing ("billing-catalog") on purpose: one fetch feeds both, and buying a
  // plan there revalidates the label here. Read is open to any member, so a
  // member who cannot buy still sees the plan rather than a silently empty label.
  const { data: billing } = useSWR(token ? "billing-catalog" : null, getBillingCatalog);
  const planName = billing?.current.plan;

  // ⌘K / Ctrl+K focuses the top-bar search.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Top-bar search: conversations (through the same server-side filter the inbox
  // uses) plus knowledge-base documents. There is no document-search endpoint, so
  // the library is fetched once and filtered here — a tenant's library is tens of
  // rows, and typing must not re-query it on every keystroke.
  //
  // State is only written from the debounce callback, never from the effect body:
  // a synchronous write there re-renders on every keystroke.
  const searchWrapRef = React.useRef<HTMLDivElement>(null);
  const libraryRef = React.useRef<KnowledgeDocument[] | null>(null);
  const [searchOpen, setSearchOpen] = React.useState(false);
  const [searchBusy, setSearchBusy] = React.useState(false);
  const [searchResults, setSearchResults] = React.useState<{ sessions: InboxItem[]; docs: KnowledgeDocument[] }>({
    sessions: [],
    docs: [],
  });
  const searchTerm = globalSearch.trim();
  const searchLive = searchTerm.length >= 2;
  const closeSearch = React.useCallback(() => {
    setSearchOpen(false);
    setGlobalSearch("");
  }, []);

  // Fetched once per layout mount, then filtered in memory (see above).
  const loadLibrary = React.useCallback(async () => {
    if (!libraryRef.current) {
      const res = await listKnowledge(1, 200);
      libraryRef.current = res.data ?? [];
    }
    return libraryRef.current;
  }, []);

  React.useEffect(() => {
    if (!searchLive) return;
    let cancelled = false;
    const timer = window.setTimeout(() => {
      setSearchBusy(true);
      void (async () => {
        try {
          const [inbox, library] = await Promise.all([
            listInbox({ q: searchTerm, pageSize: 5 }),
            loadLibrary(),
          ]);
          if (cancelled) return;
          const needle = searchTerm.toLowerCase();
          setSearchResults({
            sessions: inbox.data ?? [],
            docs: library
              .filter((doc) => doc.title.toLowerCase().includes(needle) || (doc.category ?? "").toLowerCase().includes(needle))
              .slice(0, 5),
          });
        } catch {
          if (!cancelled) setSearchResults({ sessions: [], docs: [] });
        } finally {
          if (!cancelled) setSearchBusy(false);
        }
      })();
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [searchTerm, searchLive, loadLibrary]);

  // The panel is transient: Escape, and a click anywhere outside, close it.
  React.useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (!searchWrapRef.current?.contains(e.target as Node)) setSearchOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, []);

  // inbox.notification realtime events refresh the bell immediately.
  // Every session mutation (new handoff, takeover, resolve) fans out as an
  // inbox.session event too — reuse the same socket to keep the sidebar's
  // pending-human-requests badge live without extra polling bursts.
  const { data: handoffPending, mutate: mutateHandoffPending } = useSWR(
    token ? "sidebar-handoff-pending" : null,
    () => listHumanHandoffRequests({ status: "pending", pageSize: 1 }),
    { refreshInterval: 20_000 },
  );
  // Expose a global refresh (the handoff-requests page calls this after
  // takeover/resolve/create so the badge updates without waiting for the WS).
  React.useEffect(() => {
    // SAFETY: `window` carries no declaration for this global, so the cast is the
    // only way to name it. The invariant is the optional marker itself — a caller
    // that loads before this effect runs sees `undefined` and does nothing.
    const badgeGlobal = window as unknown as { __refreshHandoffBadge?: () => void };
    badgeGlobal.__refreshHandoffBadge = () => { void mutateHandoffPending(); };
    return () => { delete badgeGlobal.__refreshHandoffBadge; };
  }, [mutateHandoffPending]);
  useInboxRealtime(token, React.useCallback((event) => {
    if (event.type === "inbox.notification") {
      // SAFETY: the same undeclared global; the optional call turns "the
      // notification bell is not mounted" into a no-op instead of a TypeError.
      (window as unknown as { __refreshNotifs?: () => void }).__refreshNotifs?.();
    }
    if (event.type === "inbox.session") void mutateHandoffPending();
  }, [mutateHandoffPending]));

  const isActive = (href: string) => pathname === href.split("?")[0];
  const close = () => setSidebarOpen(false);
  // Longest matching prefix wins so /admin/users beats /admin.
  const crumb = BREADCRUMBS
    .filter((c) => pathname === c.match || pathname.startsWith(c.match + "/"))
    .sort((a, b) => b.match.length - a.match.length)[0];

  return (
    /* Canvas: the whole app is a white rounded frame floating on a faint gray
       page — the reference layout's defining gesture. Edge-to-edge on small
       screens where the frame would only cost usable width. */
    <div className="flex h-screen flex-col overflow-hidden bg-background lg:p-3">
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-card lg:rounded-2xl lg:ring-1 lg:ring-foreground/[0.08] lg:shadow-[0_1px_3px_rgb(0_0_0/0.04)]">
        {/* ===== Top bar ===== */}
        <header className="relative z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border/70 px-4">
          <button type="button" className="rounded-md p-1.5 hover:bg-foreground/[0.06] lg:hidden" onClick={() => setSidebarOpen(true)}>
            <Menu className="size-4 text-muted-foreground" />
          </button>
          {/* Icon-only brand on small screens — the full brand lives in the sidebar. */}
          <div className="lg:hidden">
            <Brand iconOnly />
          </div>
          {/* Breadcrumb (desktop) — section › page, mirroring the reference. */}
          {crumb && (
            <nav aria-label="Breadcrumb" className="hidden items-center gap-2 lg:flex">
              <span className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/70">
                {t(crumb.section)}
              </span>
              <ChevronRight className="size-3.5 text-muted-foreground/40" />
              <span className="text-[13px] font-medium text-foreground">{t(crumb.page)}</span>
            </nav>
          )}
          {/* Search + actions */}
          <div className="ml-auto flex items-center gap-3">
            <div ref={searchWrapRef} className="relative hidden md:block w-72">
              <Search className="absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground/50" />
              <input
                ref={searchRef}
                value={globalSearch}
                onChange={(e) => { setGlobalSearch(e.target.value); setSearchOpen(true); }}
                onFocus={() => setSearchOpen(true)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") { setSearchOpen(false); return; }
                  if (e.key === "Enter" && searchTerm) {
                    setSearchOpen(false);
                    router.push(`/inbox?q=${encodeURIComponent(searchTerm)}`);
                  }
                }}
                placeholder={t("nav.search")}
                role="combobox"
                aria-label={t("nav.search")}
                aria-autocomplete="list"
                aria-expanded={searchOpen && searchLive}
                aria-controls="global-search-results"
                aria-keyshortcuts="Meta+k Control+k"
                className="h-9 w-full rounded-[10px] border border-border/80 bg-background/60 pl-9 pr-12 text-[13px] text-foreground placeholder:text-muted-foreground/50 transition-all focus:outline-none focus:border-ring/50 focus:ring-2 focus:ring-ring/25"
              />
              <span className="absolute right-2.5 top-1/2 -translate-y-1/2 rounded-md border border-border/70 bg-muted/80 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">⌘K</span>

              {searchOpen && searchLive && (
                <div id="global-search-results" className="absolute right-0 top-full z-50 mt-2 w-96 overflow-hidden rounded-xl border border-border bg-popover text-popover-foreground shadow-lg">
                  {searchBusy && searchResults.sessions.length === 0 && searchResults.docs.length === 0 && (
                    <p className="flex items-center gap-2 px-3.5 py-3 text-xs text-muted-foreground">
                      <Loader2 className="size-3.5 animate-spin" />
                    </p>
                  )}
                  {!searchBusy && searchResults.sessions.length === 0 && searchResults.docs.length === 0 && (
                    <p className="px-3.5 py-3 text-xs text-muted-foreground">{t("nav.searchNoResults")}</p>
                  )}
                  {searchResults.sessions.length > 0 && (
                    <div>
                      <p className="border-b border-border/70 bg-muted/30 px-3.5 py-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        {t("nav.inbox")}
                      </p>
                      {searchResults.sessions.map((session) => (
                        <button
                          key={session.session_id}
                          type="button"
                          onClick={() => { closeSearch(); router.push(`/inbox?session=${encodeURIComponent(session.session_id)}`); }}
                          className="flex w-full items-center gap-2.5 px-3.5 py-2 text-left transition-colors hover:bg-muted/50"
                        >
                          <Inbox className="size-3.5 shrink-0 text-muted-foreground" />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-xs font-medium">
                              {session.user_display_name || session.title || session.platform_user_id || t("inbox.anonymous")}
                            </span>
                            {session.last_message && (
                              <span className="block truncate text-[10px] text-muted-foreground">{session.last_message}</span>
                            )}
                          </span>
                        </button>
                      ))}
                    </div>
                  )}
                  {searchResults.docs.length > 0 && (
                    <div className="border-t border-border/70">
                      <p className="border-b border-border/70 bg-muted/30 px-3.5 py-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                        {t("nav.knowledge")}
                      </p>
                      {searchResults.docs.map((doc) => (
                        <button
                          key={doc.doc_id}
                          type="button"
                          onClick={() => { closeSearch(); router.push(`/knowledge?doc=${doc.doc_id}`); }}
                          className="flex w-full items-center gap-2.5 px-3.5 py-2 text-left transition-colors hover:bg-muted/50"
                        >
                          <BookOpen className="size-3.5 shrink-0 text-muted-foreground" />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-xs font-medium">{doc.title}</span>
                            {doc.category && (
                              <span className="block truncate text-[10px] text-muted-foreground">{doc.category}</span>
                            )}
                          </span>
                        </button>
                      ))}
                      <button
                        type="button"
                        onClick={() => { closeSearch(); router.push(`/knowledge?q=${encodeURIComponent(searchTerm)}`); }}
                        className="flex w-full items-center justify-between gap-2 border-t border-border/70 px-3.5 py-2 text-[11px] font-medium text-primary transition-colors hover:bg-muted/50"
                      >
                        {t("nav.searchAllInKb")}
                        <ChevronRight className="size-3" />
                      </button>
                    </div>
                  )}
                </div>
              )}
            </div>
            <NotificationBell />
          </div>
        </header>

        <div className="flex min-h-0 flex-1 overflow-hidden">
          {sidebarOpen && <div className="fixed inset-0 z-40 bg-background/70 backdrop-blur-sm lg:hidden" onClick={close} />}

          {/* ===== Sidebar ===== */}
          <aside className={cn(
            "flex flex-col overflow-hidden border-sidebar-border bg-sidebar",
            sidebarOpen ? "fixed inset-y-0 left-0 z-50 w-[236px] border-r shadow-2xl" : "hidden lg:flex lg:w-[228px] lg:border-r"
          )}>
            {/* Brand zone — same height as the top bar for a continuous header line */}
            <div className="flex h-14 shrink-0 items-center border-b border-sidebar-border/60 px-4">
              <Brand />
            </div>
            <nav className="flex-1 overflow-auto px-3 py-3">
              <NavSection label={t("nav.groupWorkspace")}>
                <NavItem href="/inbox" icon={Inbox} label={t("nav.inbox")} active={isActive("/inbox")} onNavigate={close} />
                <NavItem href="/handoff-requests" icon={UserCheck} label={t("nav.humanRequests")} active={isActive("/handoff-requests")} onNavigate={close} count={handoffPending?.total ?? 0} />
                <NavItem href="/ai-test" icon={FlaskConical} label={t("nav.aiTest")} active={isActive("/ai-test")} onNavigate={close} badge={t("nav.hot")} />
              </NavSection>

              <NavSection label={t("nav.groupContent")}>
                <NavItem href="/knowledge" icon={BookOpen} label={t("nav.knowledge")} active={isActive("/knowledge")} onNavigate={close} />
                <NavItem href="/platforms" icon={Globe} label={t("nav.platforms")} active={isActive("/platforms")} onNavigate={close} />
                <NavItem href="/widget-admin" icon={MessageCircle} label={t("nav.widget")} active={isActive("/widget-admin")} onNavigate={close} />
              </NavSection>

              {isAdmin && (
                <NavSection label={t("nav.administration")}>
                  <NavItem href="/admin" icon={Gauge} label={t("nav.dashboard")} active={isActive("/admin")} onNavigate={close} />
                  <NavItem href="/admin/users" icon={Users} label={t("nav.users")} active={isActive("/admin/users")} onNavigate={close} />
                  <NavItem href="/admin/models" icon={Cpu} label={t("nav.models")} active={isActive("/admin/models")} onNavigate={close} />
                  <NavItem href="/admin/tokens" icon={Coins} label={t("nav.tokens")} active={isActive("/admin/tokens")} onNavigate={close} />
                  <NavItem href="/admin/personas" icon={Bot} label={t("nav.personas")} active={isActive("/admin/personas")} onNavigate={close} />
                </NavSection>
              )}
              {isPlatformAdmin && (
                <NavSection label={t("nav.superAdmin")}>
                  <NavItem href="/platform-admin" icon={ShieldCheck} label={t("nav.platformAdmin")} active={isActive("/platform-admin")} onNavigate={close} />
                </NavSection>
              )}
              <NavSection label={t("nav.preferences")}>
                <NavItem href="/billing" icon={Coins} label={t("bl.title")} badge={planName?.toUpperCase()} active={isActive("/billing")} onNavigate={close} />
                <NavItem href="/settings" icon={Settings} label={t("nav.settings")} active={isActive("/settings")} onNavigate={close} />
                <NavItem href="/help" icon={HelpCircle} label={t("nav.help")} active={isActive("/help")} onNavigate={close} />
              </NavSection>
            </nav>

            {/* Account card + display controls, anchored to the rail's foot. */}
            <div className="shrink-0 border-t border-sidebar-border/80 p-2.5">
              <div className="flex items-center gap-2.5 rounded-xl bg-sidebar-accent/60 px-2 py-2 ring-1 ring-foreground/[0.05]">
                <Avatar className="size-8">
                  <AvatarFallback className="text-[12px] font-semibold">
                    {user?.username?.charAt(0).toUpperCase()}
                  </AvatarFallback>
                </Avatar>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[13px] font-medium leading-tight">{user?.username}</p>
                  <p className="truncate text-[11px] text-muted-foreground leading-tight">
                    {t(ROLE_KEYS[user?.role ?? "user"] ?? "role.member")}
                  </p>
                </div>
                <button
                  type="button"
                  onClick={logout}
                  title={t("nav.signOut")}
                  aria-label={t("nav.signOut")}
                  className="shrink-0 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-background hover:text-danger"
                >
                  <LogOut className="size-3.5" />
                </button>
              </div>
              <div className="mt-2 flex items-center justify-between gap-1">
                <ThemeToggle className="min-w-0 flex-1" />
                <LanguageSwitcher />
              </div>
            </div>
          </aside>

          {/* ===== Main — 路由切换时淡入上浮 ===== */}
          <main className="min-h-0 flex-1 overflow-hidden bg-card">
            <div key={pathname} className="h-full animate-fade-up">{children}</div>
          </main>
        </div>
      </div>
    </div>
  );
}
