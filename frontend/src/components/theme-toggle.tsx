"use client";

import * as React from "react";
import { useTheme } from "next-themes";
import { Button } from "@/components/ui/button";
import { SunIcon, MoonIcon, MonitorIcon } from "lucide-react";

type ResolvedTheme = "light" | "dark" | "system";

const ORDER: ResolvedTheme[] = ["light", "dark", "system"];

const LABEL: Record<ResolvedTheme, string> = {
  light: "Light",
  dark: "Dark",
  system: "System",
};

/**
 * Cycles light → dark → system on click. Renders nothing on the server
 * to avoid a hydration mismatch (theme is only known client-side).
 */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = React.useState(false);

  // next-themes only knows the resolved theme after hydration. Rendering the
  // real icon before that causes a hydration mismatch, so we mount on first
  // effect. This is the documented next-themes pattern.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  React.useEffect(() => setMounted(true), []);

  if (!mounted) {
    // Placeholder keeps layout stable during SSR + first paint.
    return <div className="size-7" aria-hidden />;
  }

  const current = (theme as ResolvedTheme) || "system";
  const next = ORDER[(ORDER.indexOf(current) + 1) % ORDER.length];

  const Icon = current === "light" ? SunIcon : current === "dark" ? MoonIcon : MonitorIcon;

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setTheme(next)}
      className="w-full justify-start gap-2 h-9 text-sm"
      title={`Theme: ${LABEL[current]} (click for ${LABEL[next]})`}
      aria-label={`Switch theme. Current: ${LABEL[current]}`}
    >
      <Icon className="size-4 flex-shrink-0" />
      <span className="flex-1 text-left">{LABEL[current]}</span>
    </Button>
  );
}
