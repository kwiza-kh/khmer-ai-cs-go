import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

/**
 * Tone → icon container color mapping.
 * Replaces the inline `colors: { emerald, blue, amber, purple }` map
 * that used raw Tailwind palette classes. All values come from tokens
 * defined in globals.css so they follow the active theme.
 */
const toneVariants = cva(
  "flex size-9 shrink-0 items-center justify-center rounded-lg",
  {
    variants: {
      tone: {
        default: "bg-muted text-foreground",
        success: "bg-success/10 text-success",
        warning: "bg-warning/10 text-warning",
        info: "bg-info/10 text-info",
        danger: "bg-danger/10 text-danger",
      },
    },
    defaultVariants: {
      tone: "default",
    },
  }
);

interface StatCardProps extends VariantProps<typeof toneVariants> {
  icon: LucideIcon;
  label: string;
  value?: React.ReactNode;
  /** Optional hint shown below the value, e.g. "+12% vs last week". */
  hint?: React.ReactNode;
  /** Show skeleton placeholder instead of content. */
  loading?: boolean;
  className?: string;
}

/**
 * Compact KPI card with an icon chip + label + value.
 * Used on the admin dashboard (total tokens, cache hit, cost, users, etc).
 */
export function StatCard({
  icon: Icon,
  label,
  value,
  hint,
  tone,
  loading,
  className,
}: StatCardProps) {
  if (loading) {
    return (
      <Card className={cn(className)}>
        <CardContent className="flex items-center gap-3.5 p-4">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted animate-pulse-subtle" />
          <div className="min-w-0 space-y-2 flex-1">
            <div className="h-2.5 w-16 rounded bg-muted animate-pulse-subtle" />
            <div className="h-5 w-20 rounded bg-muted animate-pulse-subtle" />
          </div>
        </CardContent>
      </Card>
    );
  }
  return (
    <Card className={cn(className)}>
      <CardContent className="flex items-center gap-3.5 p-4">
        <div className={cn(toneVariants({ tone }))}>
          <Icon className="size-[18px]" />
        </div>
        <div className="min-w-0">
          <p className="truncate text-[11px] leading-none font-medium text-muted-foreground uppercase tracking-[0.12em]">
            {label}
          </p>
          <p className="mt-1.5 text-[19px] font-semibold tracking-[-0.015em] leading-none tabular-nums truncate">
            {value ?? "—"}
          </p>
          {hint != null && (
            <p className="text-[11px] text-muted-foreground/90 mt-1.5 truncate leading-none">{hint}</p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
