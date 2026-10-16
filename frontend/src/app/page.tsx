"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function Home() {
  const router = useRouter();

  useEffect(() => {
    // Same guard as AuthGuard: a throttled/failed History call (Safari
    // SecurityError, 2026-10-16) must not take the root page down. When the
    // SPA router cannot navigate, fall back to a real page load.
    try {
      router.push("/ai-test");
    } catch {
      window.location.replace("/ai-test");
    }
  }, [router]);

  return null;
}
