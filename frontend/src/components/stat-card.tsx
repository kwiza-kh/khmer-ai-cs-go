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
  "flex size-8 items-center justify-center rounded-md",
  {
    variants: {
      tone: {
        default: "bg-primary/10 text-primary",
        success: "bg-success/15 text-success",
        warning: "bg-warning/15 text-warning",
        info: "bg-info/15 text-info",
        danger: "bg-danger/15 text-danger",
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
  className,
}: StatCardProps) {
  return (
    <Card className={className}>
      <CardContent className="flex items-center gap-3 p-3.5">
        <div className={cn(toneVariants({ tone }))}>
          <Icon className="size-4" />
        </div>
        <div className="min-w-0">
          <p className="text-[10px] font-medium text-muted-foreground uppercase tracking-[0.1em] truncate leading-none">
            {label}
          </p>
          <p className="mt-1.5 text-[17px] font-semibold tracking-[-0.01em] leading-none tabular-nums truncate">
            {value ?? "—"}
          </p>
          {hint != null && (
            <p className="text-[11px] text-muted-foreground mt-1 truncate leading-none">{hint}</p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
