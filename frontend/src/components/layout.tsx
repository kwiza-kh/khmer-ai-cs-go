"use client";

import Link from "next/link";
import * as React from "react";
import { usePathname, useRouter } from "next/navigation";
import useSWR from "swr";
import { useAuth } from "@/lib/auth-client";
import { listHumanHandoffRequests } from "@/lib/api";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import {
  BookOpen, Globe, Users, Settings, LogOut, Menu, Search,
  Gauge, HelpCircle, Inbox, UserCheck, ShieldCheck, Coins, Cpu, FlaskConical,
  ChevronsDown, ChevronsLeft, MessageCircle, type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import { ThemeToggle } from "@/components/theme-toggle";
import { NotificationBell } from "@/components/notification-bell";
import { LanguageSwitcher } from "@/components/language-switcher";
import { useI18n } from "@/lib/i18n";
import { useInboxRealtime } from "@/lib/realtime";

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
    <Link
      href={href}
      onClick={onNavigate}
      className={cn(
        "group relative flex items-center gap-2.5 rounded-lg px-3 py-2 text-[13px] transition-all",
        active
          ? "bg-primary/[0.09] font-semibold text-primary ring-1 ring-inset ring-primary/20 shadow-[inset_0_1px_0_rgb(255_255_255/0.4)] dark:bg-primary/[0.13] dark:shadow-none"
          : "font-medium text-sidebar-foreground/85 hover:bg-foreground/[0.045] hover:text-sidebar-foreground"
      )}
    >
      <Icon className={cn("size-4 shrink-0", active ? "text-primary" : "text-sidebar-foreground/60 group-hover:text-sidebar-foreground")} />
      <span className="flex-1 truncate">{label}</span>
      {count != null && count > 0 && (
        <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-bold tabular-nums text-white shadow-[0_2px_8px_-2px_color-mix(in_oklch,var(--color-destructive)_60%,transparent)]">
          {count > 99 ? "99+" : count}
        </span>
      )}
      {badge && (
        <span className="rounded-full bg-[linear-gradient(115deg,var(--color-primary),hsl(285_85%_58%))] px-1.5 py-0.5 text-[10px] font-semibold text-white shadow-[0_2px_8px_-2px_color-mix(in_oklch,var(--color-primary)_60%,transparent)]">
          {badge}
        </span>
      )}
    </Link>
  );
}

function NavSection({ label, children, defaultOpen = true }: { label: string; children: React.ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className="pt-4">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="group flex w-full items-center justify-between rounded-md px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/80 transition-colors hover:text-foreground"
      >
        {label}
        {open ? <ChevronsDown className="size-3 text-muted-foreground/50 group-hover:text-muted-foreground" /> : <ChevronsLeft className="size-3 text-muted-foreground/50 group-hover:text-muted-foreground" />}
      </button>
      {open && <div className="mt-1 space-y-0.5 pl-3">{children}</div>}
    </div>
  );
}

const ROLE_KEYS: Record<string, string> = {
  platform_admin: "role.platformAdmin",
  admin: "role.admin",
  user: "role.member",
};

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
    (window as unknown as { __refreshHandoffBadge?: () => void }).__refreshHandoffBadge = () => { void mutateHandoffPending(); };
    return () => { delete (window as unknown as { __refreshHandoffBadge?: () => void }).__refreshHandoffBadge; };
  }, [mutateHandoffPending]);
  useInboxRealtime(token, React.useCallback((event) => {
    if (event.type === "inbox.notification") {
      (window as unknown as { __refreshNotifs?: () => void }).__refreshNotifs?.();
    }
    if (event.type === "inbox.session") void mutateHandoffPending();
  }, [mutateHandoffPending]));

  const isActive = (href: string) => pathname === href.split("?")[0];
  const close = () => setSidebarOpen(false);

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background">
      {/* ===== Top bar — glass + hairline ===== */}
      <header className="glass hairline-b relative z-30 flex h-14 shrink-0 items-center gap-4 px-4">
        <button type="button" className="rounded-md p-1.5 hover:bg-foreground/[0.06] lg:hidden" onClick={() => setSidebarOpen(true)}>
          <Menu className="size-4 text-muted-foreground" />
        </button>
        {/* Brand */}
        <Link href="/inbox" onClick={close} className="group flex items-center gap-2">
          <span className="relative flex size-7 items-center justify-center">
            <span aria-hidden className="absolute inset-0 rounded-lg bg-primary/25 blur-md opacity-60 transition-opacity group-hover:opacity-100" />
            <svg viewBox="0 0 24 24" className="relative size-6 transition-transform duration-300 group-hover:scale-105 group-hover:rotate-3" fill="none" aria-hidden>
              <path d="M12 2l2.4 7.6L22 12l-7.6 2.4L12 22l-2.4-7.6L2 12l7.6-2.4L12 2z" fill="url(#qg)" />
              <defs>
                <linearGradient id="qg" x1="0" y1="0" x2="24" y2="24">
                  <stop offset="0" stopColor="#8b5cf6" /><stop offset="1" stopColor="#4f46e5" />
                </linearGradient>
              </defs>
            </svg>
          </span>
          <span className="text-[15px] font-semibold tracking-tight text-foreground">RelayChat</span>
        </Link>
        {/* Search — jumps to the inbox with the query pre-applied */}
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
            className="h-9 w-full rounded-full border border-border/80 bg-card/70 pl-9 pr-12 text-[13px] text-foreground shadow-[0_1px_2px_rgb(0_0_0/0.03)] placeholder:text-muted-foreground/50 transition-all focus:outline-none focus:border-ring/50 focus:ring-2 focus:ring-ring/25 dark:bg-white/[0.05]"
          />
          <span className="absolute right-3 top-1/2 -translate-y-1/2 rounded-md border border-border/70 bg-muted/80 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">⌘K</span>
        </div>
        <div className="ml-auto flex items-center gap-4">
          <Link href="/knowledge" className="hidden sm:block text-[13px] font-medium text-muted-foreground transition-colors hover:text-foreground">{t("nav.knowledge")}</Link>
          <Link href="/help" className="hidden sm:block text-[13px] font-medium text-muted-foreground transition-colors hover:text-foreground">{t("nav.help")}</Link>
          <Link href="/inbox" className="rounded-full bg-ink px-4 py-1.5 text-[13px] font-semibold text-ink-foreground shadow-[0_1px_2px_rgb(0_0_0/0.25),inset_0_1px_0_rgb(255_255_255/0.18)] transition-all hover:brightness-110 active:scale-[0.97]">
            {t("nav.console")}
          </Link>
          <DropdownMenu>
            <DropdownMenuTrigger>
              <Avatar className="size-8 cursor-pointer rounded-full ring-2 ring-primary/30 transition-all hover:ring-primary/55 hover:shadow-[0_0_0_4px_color-mix(in_oklch,var(--color-primary)_14%,transparent)]">
                <AvatarFallback className="rounded-full bg-gradient-to-br from-primary to-indigo-700 text-[12px] font-semibold text-white">
                  {user?.username?.charAt(0).toUpperCase()}
                </AvatarFallback>
              </Avatar>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <div className="px-2 py-1.5">
                <p className="text-xs font-medium truncate">{user?.username}</p>
                <p className="text-[10px] text-muted-foreground">{t(ROLE_KEYS[user?.role ?? "user"] ?? "role.member")}</p>
              </div>
              <DropdownMenuItem onClick={logout} className="text-destructive cursor-pointer text-xs">
                <LogOut className="size-3.5 mr-2" />{t("nav.signOut")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>

      <div className="flex flex-1 overflow-hidden">
        {sidebarOpen && <div className="fixed inset-0 z-40 bg-background/70 backdrop-blur-sm lg:hidden" onClick={close} />}

        {/* ===== Sidebar ===== */}
        <aside className={cn(
          "flex flex-col bg-sidebar border-r border-sidebar-border",
          sidebarOpen ? "fixed inset-y-0 left-0 z-50 w-[232px] shadow-2xl" : "hidden lg:flex lg:w-[224px]"
        )}>
          <nav className="flex-1 overflow-auto px-3 py-3">
            <div className="space-y-0.5">
              <NavItem href="/inbox" icon={Inbox} label={t("nav.inbox")} active={isActive("/inbox")} onNavigate={close} />
              <NavItem href="/handoff-requests" icon={UserCheck} label={t("nav.humanRequests")} active={isActive("/handoff-requests")} onNavigate={close} count={handoffPending?.total ?? 0} />
              <NavItem href="/knowledge" icon={BookOpen} label={t("nav.knowledge")} active={isActive("/knowledge")} onNavigate={close} />
              <NavItem href="/ai-test" icon={FlaskConical} label={t("nav.aiTest")} active={isActive("/ai-test")} onNavigate={close} badge={t("nav.hot")} />
              <NavItem href="/platforms" icon={Globe} label={t("nav.platforms")} active={isActive("/platforms")} onNavigate={close} />
              <NavItem href="/widget-admin" icon={MessageCircle} label={t("nav.widget")} active={isActive("/widget-admin")} onNavigate={close} />
            </div>

            {isAdmin && (
              <NavSection label={t("nav.administration")}>
                <NavItem href="/admin" icon={Gauge} label={t("nav.dashboard")} active={isActive("/admin")} onNavigate={close} />
                <NavItem href="/admin/users" icon={Users} label={t("nav.users")} active={isActive("/admin/users")} onNavigate={close} />
                <NavItem href="/admin/models" icon={Cpu} label={t("nav.models")} active={isActive("/admin/models")} onNavigate={close} />
                <NavItem href="/admin/tokens" icon={Coins} label={t("nav.tokens")} active={isActive("/admin/tokens")} onNavigate={close} />
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
          <div className="flex items-center justify-between border-t border-sidebar-border/80 px-3 py-2">
            <ThemeToggle />
            <div className="flex items-center gap-1">
              <LanguageSwitcher />
              <NotificationBell />
            </div>
          </div>
        </aside>

        {/* ===== Main — 路由切换时淡入上浮 ===== */}
        <main className="flex-1 min-h-0 overflow-hidden bg-background">
          <div key={pathname} className="h-full animate-fade-up">{children}</div>
        </main>
      </div>
    </div>
  );
}
