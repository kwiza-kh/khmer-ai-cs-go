"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// 原仪表盘（页内 Tab 版）已拆分为六个独立入口；/admin 保留为重定向，
// 旧书签与旧链接落到第一个入口（分析）。
export default function AdminIndexRedirect() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/admin/analytics");
  }, [router]);
  return null;
}
