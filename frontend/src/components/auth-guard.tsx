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

  // Public routes that never require a session (/widget = the chat widget iframe).
  const isPublic = pathname === "/login" || pathname.startsWith("/widget");

  useEffect(() => {
    if (isLoading) return;
    if (!token && !isPublic) {
      router.replace("/login");
    }
    if (token && pathname === "/login") {
      router.replace("/ai-test");
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
