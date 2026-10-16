"use client";

import { useAuth } from "@/lib/auth-client";
import AppLayout from "@/components/layout";
import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Loader2 } from "lucide-react";

export function AuthGuard({ children }: { children: React.ReactNode }) {
  const { token, isLoading } = useAuth();
  const pathname = usePathname();
  const router = useRouter();

  // Public routes that never require a session (/widget = the chat widget
  // iframe, /privacy = the Meta app review privacy policy,
  // /privacy/deletion-status = the status page Meta's Data Deletion callback
  // hands to a data subject, /join = an invite landing page whose visitor
  // usually has no session yet and must keep the ?code= through the login
  // round trip). Exact match only — /widget-admin is an
  // authenticated admin page that must keep the app shell (and the auth
  // redirect), and the deletion-status path is listed explicitly rather than
  // matched by prefix so this stays true.
  const isPublic =
    pathname === "/login" ||
    pathname === "/join" ||
    pathname === "/widget" ||
    pathname === "/privacy" ||
    pathname === "/privacy/deletion-status";

  useEffect(() => {
    if (isLoading) return;
    // Navigation can fail outside our control: Safari throws
    // SecurityError("The operation is insecure.") from history.replaceState
    // when it throttles the History API, and before 2026-10-16 that throw
    // escaped this effect and replaced the whole app with the global error
    // screen (the owner's login report). A failed redirect is a degradation,
    // not a crash — the user simply stays where they are.
    try {
      if (!token && !isPublic) {
        router.replace("/login");
      }
      if (token && pathname === "/login") {
        router.replace("/ai-test");
      }
    } catch {
      /* navigation unavailable — stay on the current page */
    }
  }, [isLoading, token, pathname, isPublic, router]);

  if (isLoading && !isPublic) {
    return (
      <div className="h-screen bg-background flex items-center justify-center">
        <Loader2 className="size-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (isPublic) return <>{children}</>;
  if (!token) return null;
  if (pathname === "/login") return null;

  return <AppLayout>{children}</AppLayout>;
}
