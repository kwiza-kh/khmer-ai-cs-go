"use client";

import Link from "next/link";
import * as React from "react";
import { usePathname, useRouter } from "next/navigation";
import useSWR from "swr";
import { motion } from "motion/react";
import { useAuth } from "@/lib/auth-client";
import { listHumanHandoffRequests } from "@/lib/api";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { RelayChatLogo } from "@/components/relaychat-logo";
import { cn } from "@/lib/utils";
import {
  BookOpen, Globe, Users, Settings, LogOut, Menu, Search,
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
  const isAdmin = user?.role === "admin" || user?.role === "platform_admin";
  const isPlatformAdmin = user?.role === "platform_admin";

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
            <div className="relative hidden md:block w-72">
              <Search className="absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground/50" />
              <input
                ref={searchRef}
                value={globalSearch}
                onChange={(e) => setGlobalSearch(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && globalSearch.trim()) {
                    router.push(`/inbox?q=${encodeURIComponent(globalSearch.trim())}`);
                  }
                }}
                placeholder={t("nav.search")}
                role="searchbox"
                aria-label={t("nav.search")}
                aria-keyshortcuts="Meta+k Control+k"
                className="h-9 w-full rounded-[10px] border border-border/80 bg-background/60 pl-9 pr-12 text-[13px] text-foreground placeholder:text-muted-foreground/50 transition-all focus:outline-none focus:border-ring/50 focus:ring-2 focus:ring-ring/25"
              />
              <span className="absolute right-2.5 top-1/2 -translate-y-1/2 rounded-md border border-border/70 bg-muted/80 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">⌘K</span>
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
