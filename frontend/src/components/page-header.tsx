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
        "border-b flex items-center justify-between gap-4 px-5 sm:px-6 py-4",
        className
      )}
    >
      <div className="flex min-w-0 items-center gap-3">
        {Icon && (
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
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
            "text-[17px] font-semibold tracking-[-0.015em] leading-tight text-foreground truncate",
            kicker && "mt-1.5"
          )}>
            {title}
          </h1>
          {description != null && (
            <p className="mt-0.5 text-[12.5px] text-muted-foreground truncate">{description}</p>
          )}
        </div>
      </div>
      {actions && <div className="flex items-center gap-2 flex-shrink-0">{actions}</div>}
    </header>
  );
}
