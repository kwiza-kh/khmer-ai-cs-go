import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { TrendingDown, TrendingUp, type LucideIcon } from "lucide-react";

/**
 * Tone → icon container color mapping.
 * Replaces the inline `colors: { emerald, blue, amber, purple }` map
 * that used raw Tailwind palette classes. All values come from tokens
 * defined in globals.css so they follow the active theme.
 */
const toneVariants = cva(
  "flex size-7 shrink-0 items-center justify-center rounded-lg",
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
  /** Percentage change vs the previous period; renders as a signed pill. */
  delta?: number | null;
  /** Which direction is good news — an increase is good unless this is "down". */
  deltaGood?: "up" | "down";
  /** Trailing slot in the label row (e.g. a "…" actions menu). */
  menu?: React.ReactNode;
  /** Show skeleton placeholder instead of content. */
  loading?: boolean;
  className?: string;
}

/**
 * KPI tile in the dashboard language used across the product: a small icon
 * chip and uppercase label on the top row, the value set large beneath it,
 * then a muted caption.
 * Used on the admin dashboard (total tokens, cache hit, cost, users, etc).
 */
export function StatCard({
  icon: Icon,
  label,
  value,
  hint,
  delta,
  deltaGood = "up",
  menu,
  tone,
  loading,
  className,
}: StatCardProps) {
  if (loading) {
    return (
      <Card className={cn(className)}>
        <CardContent className="flex flex-col gap-3 p-4">
          <div className="flex items-center gap-2.5">
            <div className="size-7 shrink-0 rounded-lg bg-muted animate-pulse-subtle" />
            <div className="h-2.5 w-20 rounded bg-muted animate-pulse-subtle" />
          </div>
          <div className="h-7 w-24 rounded bg-muted animate-pulse-subtle" />
        </CardContent>
      </Card>
    );
  }

  const hasDelta = delta != null && Number.isFinite(delta);
  const rising = (delta ?? 0) >= 0;
  // An increase is good news for most KPIs; for "lower is better" metrics
  // (cost, churn, response time) the caller passes deltaGood="down".
  const good = deltaGood === "up" ? rising : !rising;
  const DeltaIcon = rising ? TrendingUp : TrendingDown;

  return (
    <Card className={cn(className)}>
      <CardContent className="flex flex-col gap-3 p-4">
        <div className="flex items-center gap-2.5">
          <div className={cn(toneVariants({ tone }))}>
            <Icon className="size-[15px]" />
          </div>
          <p className="min-w-0 flex-1 truncate text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
            {label}
          </p>
          {menu && <div className="shrink-0 text-muted-foreground/60">{menu}</div>}
        </div>
        <div className="flex flex-wrap items-baseline gap-2">
          <p className="text-[27px] font-semibold leading-none tracking-[-0.02em] tabular-nums">
            {value ?? "—"}
          </p>
          {hasDelta && (
            <span
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[11px] font-semibold tabular-nums",
                good ? "bg-success/10 text-success" : "bg-danger/10 text-danger"
              )}
            >
              <DeltaIcon className="size-3" />
              {rising ? "+" : ""}
              {delta.toFixed(0)}%
            </span>
          )}
        </div>
        {hint != null && (
          <p className="truncate text-[12px] leading-none text-muted-foreground">{hint}</p>
        )}
      </CardContent>
    </Card>
  );
}
