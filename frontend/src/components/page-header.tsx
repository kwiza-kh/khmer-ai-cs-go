import * as React from "react";
import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

interface PageHeaderProps {
  icon: LucideIcon;
  /** Small uppercase eyebrow label, e.g. "Administration". */
  kicker?: string;
  /** Main title, e.g. "Operations dashboard". */
  title: string;
  /** Optional sub-line under the title. */
  description?: React.ReactNode;
  /** Right-aligned actions (buttons, badges, search). */
  actions?: React.ReactNode;
  className?: string;
}

/**
 * Unified top-of-page header.
 * Replaces the ad-hoc `.workspace-header` / `.page-kicker` / `.page-title`
 * markup that was duplicated across admin / knowledge / platforms / chat.
 */
export function PageHeader({
  icon: Icon,
  kicker,
  title,
  description,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <header
      className={cn(
        "flex items-end justify-between gap-4 px-5 sm:px-6 pt-5 pb-4",
        className
      )}
    >
      <div className="flex min-w-0 items-center gap-3">
        {Icon && (
          <div className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-muted text-foreground ring-1 ring-foreground/[0.06]">
            <Icon className="size-[18px]" />
          </div>
        )}
        <div className="min-w-0">
          {kicker && (
            <p className="text-muted-foreground text-[11px] leading-none font-semibold uppercase tracking-[0.16em]">
              {kicker}
            </p>
          )}
          <h1 className={cn(
            "text-[26px] sm:text-[30px] font-semibold tracking-[-0.025em] leading-[1.1] text-foreground truncate",
            kicker && "mt-1.5"
          )}>
            {title}
          </h1>
          {description != null && (
            <p className="mt-1.5 text-[13px] text-muted-foreground truncate">{description}</p>
          )}
        </div>
      </div>
      {actions && <div className="flex items-center gap-2 flex-shrink-0 pb-1">{actions}</div>}
    </header>
  );
}
