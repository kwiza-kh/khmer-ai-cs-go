"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// 原仪表盘（页内 Tab 版）已拆分为六个独立入口；/admin 保留为重定向，
// 旧书签与旧链接落到第一个入口（分析）。
export default function AdminIndexRedirect() {
  const router = useRouter();
  useEffect(() => {
    // Guarded like the other boot-path redirects: a failed History call
    // (Safari SecurityError, 2026-10-16) falls back to a real page load
    // instead of crashing the segment.
    try {
      router.replace("/admin/analytics");
    } catch {
      window.location.replace("/admin/analytics");
    }
  }, [router]);
  return null;
}
