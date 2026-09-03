"use client";

import { Toaster as SonnerToaster } from "@/components/ui/sonner";

export function Toaster() {
  // 不强制 theme, 让 sonner 通过 useTheme() 跟随当前主题.
  return <SonnerToaster position="bottom-right" />;
}
