import * as React from "react";

/**
 * The RelayChat mark — the four-point sparkle the sidebar has always drawn, lifted
 * into one component so the sidebar lockup, the AI message avatars and the AI panel
 * chip cannot drift apart.
 *
 * Two render modes because it is used at two very different scales:
 *   - default: a single-colour mark in `currentColor`, so it stays legible at the
 *     14-16px avatars and follows the surrounding text colour (DESIGN.md's --brand
 *     token, which is documented as the logo accent).
 *   - `gradient`: the sidebar's two-stop violet, which it pairs with a blurred glow.
 */
export function RelayChatLogo({
  className,
  gradient = false,
}: {
  className?: string;
  gradient?: boolean;
}) {
  // One id per instance: the inbox renders a mark for every AI message on screen,
  // and duplicate SVG ids make every `url(#…)` resolve to whichever one came first
  // (the sidebar's), which would leave a differently-coloured mark wrong.
  const rawId = React.useId();
  const gradientId = `relaychat-mark-${rawId.replace(/[^a-zA-Z0-9_-]/g, "")}`;

  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" aria-hidden focusable="false">
      <path
        d="M12 2l2.4 7.6L22 12l-7.6 2.4L12 22l-2.4-7.6L2 12l7.6-2.4L12 2z"
        fill={gradient ? `url(#${gradientId})` : "currentColor"}
      />
      {gradient && (
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="24" y2="24">
            <stop offset="0" stopColor="#8b5cf6" />
            <stop offset="1" stopColor="#4f46e5" />
          </linearGradient>
        </defs>
      )}
    </svg>
  );
}
