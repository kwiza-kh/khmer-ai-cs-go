"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAuth } from "@/lib/auth-client";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import {
  BookOpen, Globe, Users, Settings, LogOut, Menu, Search,
  Gauge, HelpCircle, Inbox, UserCheck, ShieldCheck, Coins, Cpu, FlaskConical,
  ChevronsUpDown, ChevronsDown, ChevronsLeft, type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import { ThemeToggle } from "@/components/theme-toggle";
import { NotificationBell } from "@/components/notification-bell";

// NavItem: Qwen Cloud style — active = filled light-gray pill, no rail.
function NavItem({
  href,
  icon: Icon,
  label,
  active,
  onNavigate,
  badge,
}: {
  href: string;
  icon: LucideIcon;
  label: string;
  active: boolean;
  onNavigate?: () => void;
  badge?: string;
}) {
  return (
    <Link
      href={href}
      onClick={onNavigate}
      className={cn(
        "group flex items-center gap-2.5 rounded-lg px-3 py-2 text-[13px] font-medium transition-colors",
        active
          ? "bg-muted text-foreground"
          : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
      )}
    >
      <Icon className={cn("size-4 shrink-0", active ? "text-foreground" : "text-muted-foreground/70 group-hover:text-foreground")} />
      <span className="flex-1 truncate">{label}</span>
      {badge && (
        <span className="rounded-full bg-primary px-1.5 py-0.5 text-[10px] font-semibold text-primary-foreground">{badge}</span>
      )}
    </Link>
  );
}

// Collapsible section label with chevron.
function NavSection({ label, children, defaultOpen = true }: { label: string; children: React.ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className="pt-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between rounded-md px-3 py-1.5 text-[13px] font-medium text-foreground hover:bg-muted/60"
      >
        {label}
        {open ? <ChevronsDown className="size-3.5 text-muted-foreground/60" /> : <ChevronsLeft className="size-3.5 text-muted-foreground/60" />}
      </button>
      {open && <div className="mt-0.5 space-y-0.5 pl-3">{children}</div>}
    </div>
  );
}

const ROLE_LABELS: Record<string, string> = {
  platform_admin: "Platform admin",
  admin: "Administrator",
  user: "Member",
};

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const pathname = usePathname();
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const isAdmin = user?.role === "admin" || user?.role === "platform_admin";
  const isPlatformAdmin = user?.role === "platform_admin";

  const isActive = (href: string) => pathname === href.split("?")[0];
  const close = () => setSidebarOpen(false);

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background">
      {/* ===== Top bar (Qwen Cloud style) ===== */}
      <header className="flex h-14 shrink-0 items-center gap-4 border-b border-border bg-background px-4">
        <button type="button" className="lg:hidden" onClick={() => setSidebarOpen(true)}>
          <Menu className="size-4 text-muted-foreground" />
        </button>
        {/* Brand */}
        <Link href="/inbox" onClick={close} className="flex items-center gap-2">
          <svg viewBox="0 0 24 24" className="size-6" fill="none" aria-hidden>
            <path d="M12 2l2.4 7.6L22 12l-7.6 2.4L12 22l-2.4-7.6L2 12l7.6-2.4L12 2z" fill="url(#qg)" />
            <defs>
              <linearGradient id="qg" x1="0" y1="0" x2="24" y2="24">
                <stop offset="0" stopColor="#8b5cf6" /><stop offset="1" stopColor="#6d28d9" />
              </linearGradient>
            </defs>
          </svg>
          <span className="text-[15px] font-semibold tracking-tight text-foreground">Khmer AI</span>
        </Link>
        {/* Search (decorative) */}
        <div className="relative hidden md:block w-72">
          <Search className="absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground/60" />
          <input
            readOnly
            placeholder="Search"
            className="h-9 w-full rounded-full border border-border bg-card pl-9 pr-12 text-[13px] text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-ring/40"
          />
          <span className="absolute right-3 top-1/2 -translate-y-1/2 rounded border border-border bg-muted px-1 text-[10px] text-muted-foreground">⌘K</span>
        </div>
        <div className="ml-auto flex items-center gap-4">
          <Link href="/knowledge" className="hidden sm:block text-[13px] font-medium text-muted-foreground hover:text-foreground">Knowledge</Link>
          <Link href="/help" className="hidden sm:block text-[13px] font-medium text-muted-foreground hover:text-foreground">Help</Link>
          <button type="button" className="rounded-full bg-ink px-4 py-1.5 text-[13px] font-semibold text-ink-foreground hover:opacity-90">
            Console
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger>
              <Avatar className="size-8 cursor-pointer rounded-full ring-2 ring-primary/30">
                <AvatarFallback className="rounded-full bg-gradient-to-br from-primary to-violet-700 text-[12px] font-semibold text-white">
                  {user?.username?.charAt(0).toUpperCase()}
                </AvatarFallback>
              </Avatar>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <div className="px-2 py-1.5">
                <p className="text-xs font-medium truncate">{user?.username}</p>
                <p className="text-[10px] text-muted-foreground">{ROLE_LABELS[user?.role ?? "user"] ?? "Member"}</p>
              </div>
              <DropdownMenuItem onClick={logout} className="text-destructive cursor-pointer text-xs">
                <LogOut className="size-3.5 mr-2" />Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>

      <div className="flex flex-1 overflow-hidden">
        {sidebarOpen && <div className="fixed inset-0 z-40 bg-background/80 backdrop-blur-sm lg:hidden" onClick={close} />}

        {/* ===== Sidebar ===== */}
        <aside className={cn(
          "flex flex-col bg-sidebar border-r border-sidebar-border",
          sidebarOpen ? "fixed inset-y-0 left-0 z-50 w-[220px]" : "hidden lg:flex lg:w-[220px]"
        )}>
          <nav className="flex-1 overflow-auto px-3 py-3">
            <div className="space-y-0.5">
              <NavItem href="/inbox" icon={Inbox} label="Inbox" active={isActive("/inbox")} onNavigate={close} />
              <NavItem href="/handoff-requests" icon={UserCheck} label="Human requests" active={isActive("/handoff-requests")} onNavigate={close} />
              <NavItem href="/knowledge" icon={BookOpen} label="Knowledge" active={isActive("/knowledge")} onNavigate={close} />
              <NavItem href="/ai-test" icon={FlaskConical} label="AI 测试" active={isActive("/ai-test")} onNavigate={close} badge="Hot" />
              <NavItem href="/platforms" icon={Globe} label="Platforms" active={isActive("/platforms")} onNavigate={close} />
            </div>

            {isAdmin && (
              <NavSection label="Administration">
                <NavItem href="/admin" icon={Gauge} label="Dashboard" active={isActive("/admin")} onNavigate={close} />
                <NavItem href="/admin/users" icon={Users} label="Users" active={isActive("/admin/users")} onNavigate={close} />
                <NavItem href="/admin/models" icon={Cpu} label="Models" active={isActive("/admin/models")} onNavigate={close} />
                <NavItem href="/admin/tokens" icon={Coins} label="Tokens" active={isActive("/admin/tokens")} onNavigate={close} />
              </NavSection>
            )}
            {isPlatformAdmin && (
              <NavSection label="Super admin">
                <NavItem href="/platform-admin" icon={ShieldCheck} label="Platform Admin" active={isActive("/platform-admin")} onNavigate={close} />
              </NavSection>
            )}
            <NavSection label="Preferences">
              <NavItem href="/settings" icon={Settings} label="Settings" active={isActive("/settings")} onNavigate={close} />
              <NavItem href="/help" icon={HelpCircle} label="Help" active={isActive("/help")} onNavigate={close} />
            </NavSection>
          </nav>
          <div className="flex items-center justify-between border-t border-sidebar-border px-3 py-2">
            <ThemeToggle />
            <NotificationBell />
          </div>
        </aside>

        {/* ===== Main ===== */}
        <main className="flex-1 min-h-0 overflow-hidden bg-background">{children}</main>
      </div>
    </div>
  );
}
