import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

const emptyStateVariants = cva(
  "flex flex-col items-center justify-center text-center py-14 animate-fade-in",
  {
    variants: {
      variant: {
        default: "",
        error: "",
        onboarding: "",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

const iconContainerVariants = cva(
  "mb-4 flex size-14 items-center justify-center rounded-2xl ring-1 ring-inset shadow-[inset_0_1px_0_rgb(255_255_255/0.5)] dark:shadow-none",
  {
    variants: {
      variant: {
        default: "bg-muted/70 ring-border/60 dark:bg-white/[0.04] dark:ring-white/[0.06]",
        error: "bg-danger/10 ring-danger/20 dark:bg-danger/10 dark:ring-danger/20",
        onboarding: "bg-brand-muted ring-brand/20 dark:bg-brand-muted dark:ring-brand/20",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

const iconVariants = cva("size-6", {
  variants: {
    variant: {
      default: "text-muted-foreground/60",
      error: "text-danger/70",
      onboarding: "text-brand",
    },
  },
  defaultVariants: {
    variant: "default",
  },
});

interface EmptyStateProps extends VariantProps<typeof emptyStateVariants> {
  icon: LucideIcon;
  title: string;
  description?: React.ReactNode;
  className?: string;
  action?: React.ReactNode;
}

/**
 * Standardized "no data" placeholder with visual variants.
 * - default: neutral grey for empty lists / no results
 * - error: red-tinted for failures / permission denied
 * - onboarding: brand-tinted for first-use guidance
 */
export function EmptyState({
  icon: Icon,
  title,
  description,
  variant,
  className,
  action,
}: EmptyStateProps) {
  return (
    <div className={cn(emptyStateVariants({ variant }), className)}>
      <div className={cn(iconContainerVariants({ variant }))}>
        <Icon className={cn(iconVariants({ variant }))} />
      </div>
      <p className="text-sm font-semibold text-foreground/80">{title}</p>
      {description != null && (
        <p className="text-xs text-muted-foreground mt-1.5 max-w-xs leading-relaxed">{description}</p>
      )}
      {action != null && <div className="mt-5">{action}</div>}
    </div>
  );
}
