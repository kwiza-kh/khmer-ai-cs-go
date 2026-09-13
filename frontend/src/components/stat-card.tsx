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
  "flex size-9 shrink-0 items-center justify-center rounded-[10px] shadow-[inset_0_1px_0_rgb(255_255_255/0.35)] dark:shadow-none",
  {
    variants: {
      tone: {
        default: "bg-gradient-to-br from-primary/18 to-primary/6 text-primary ring-1 ring-inset ring-primary/15",
        success: "bg-gradient-to-br from-success/20 to-success/8 text-success ring-1 ring-inset ring-success/15",
        warning: "bg-gradient-to-br from-warning/20 to-warning/8 text-warning ring-1 ring-inset ring-warning/15",
        info: "bg-gradient-to-br from-info/20 to-info/8 text-info ring-1 ring-inset ring-info/15",
        danger: "bg-gradient-to-br from-danger/20 to-danger/8 text-danger ring-1 ring-inset ring-danger/15",
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
    <Card className={cn("card-interactive", className)}>
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
