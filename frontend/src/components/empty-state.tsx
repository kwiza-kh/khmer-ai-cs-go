import * as React from "react";
import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  description?: React.ReactNode;
  /** Vertical padding; matches the previous inline "py-12" pattern. */
  className?: string;
  action?: React.ReactNode;
}

/**
 * Standardized "no data" placeholder.
 * Replaces ~5 inline copies scattered across admin/knowledge pages,
 * each of which used its own muted-foreground opacity and spacing.
 */
export function EmptyState({
  icon: Icon,
  title,
  description,
  className,
  action,
}: EmptyStateProps) {
  return (
    <div className={cn("flex flex-col items-center justify-center text-center py-14 animate-fade-in", className)}>
      <div className="mb-4 flex size-14 items-center justify-center rounded-2xl bg-muted/70 ring-1 ring-inset ring-border/60 shadow-[inset_0_1px_0_rgb(255_255_255/0.5)] dark:bg-white/[0.04] dark:ring-white/[0.06] dark:shadow-none">
        <Icon className="size-6 text-muted-foreground/60" />
      </div>
      <p className="text-sm font-semibold text-foreground/80">{title}</p>
      {description != null && (
        <p className="text-xs text-muted-foreground mt-1.5 max-w-xs leading-relaxed">{description}</p>
      )}
      {action != null && <div className="mt-5">{action}</div>}
    </div>
  );
}
