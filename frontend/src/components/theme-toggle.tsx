"use client";

import * as React from "react";
import { useTheme } from "next-themes";
import { Button } from "@/components/ui/button";
import { SunIcon, MoonIcon, MonitorIcon } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type ResolvedTheme = "light" | "dark" | "system";

const ORDER: ResolvedTheme[] = ["light", "dark", "system"];

const LABEL_KEY: Record<ResolvedTheme, string> = {
  light: "theme.light",
  dark: "theme.dark",
  system: "theme.system",
};

/**
 * Cycles light → dark → system on click. Renders nothing on the server
 * to avoid a hydration mismatch (theme is only known client-side).
 */
export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  const { t, tf } = useI18n();
  const [mounted, setMounted] = React.useState(false);

  // next-themes only knows the resolved theme after hydration. Rendering the
  // real icon before that causes a hydration mismatch, so we mount on first
  // effect. This is the documented next-themes pattern.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  React.useEffect(() => setMounted(true), []);

  if (!mounted) {
    // Placeholder keeps layout stable during SSR + first paint.
    return <div className={cn("size-7", className)} aria-hidden />;
  }

  const current = (theme as ResolvedTheme) || "system";
  const next = ORDER[(ORDER.indexOf(current) + 1) % ORDER.length];

  const Icon = current === "light" ? SunIcon : current === "dark" ? MoonIcon : MonitorIcon;

  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setTheme(next)}
      className={cn("h-8 justify-start gap-2 px-2 text-[13px]", className)}
      title={tf("theme.title", { cur: t(LABEL_KEY[current]), next: t(LABEL_KEY[next]) })}
      aria-label={tf("theme.aria", { cur: t(LABEL_KEY[current]) })}
    >
      <Icon className="size-4 flex-shrink-0" />
      <span className="flex-1 text-left">{t(LABEL_KEY[current])}</span>
    </Button>
  );
}
