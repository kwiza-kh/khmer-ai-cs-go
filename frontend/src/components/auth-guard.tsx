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

  useEffect(() => {
    if (isLoading) return;
    if (!token && pathname !== "/login") {
      router.replace("/login");
    }
    if (token && pathname === "/login") {
      router.replace("/ai-test");
    }
  }, [isLoading, token, pathname, router]);

  if (isLoading) {
    return (
      <div className="h-screen bg-background flex items-center justify-center">
        <Loader2 className="size-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (!token && pathname !== "/login") return null;
  if (token && pathname === "/login") return null;

  if (pathname === "/login") return <>{children}</>;

  return <AppLayout>{children}</AppLayout>;
}
