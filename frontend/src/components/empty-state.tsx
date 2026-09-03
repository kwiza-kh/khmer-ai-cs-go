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
    <div className={cn("flex flex-col items-center justify-center text-center py-12", className)}>
      <Icon className="size-8 text-muted-foreground/40 mb-3" />
      <p className="text-sm font-medium text-muted-foreground">{title}</p>
      {description != null && (
        <p className="text-xs text-muted-foreground mt-1 max-w-xs">{description}</p>
      )}
      {action != null && <div className="mt-4">{action}</div>}
    </div>
  );
}
