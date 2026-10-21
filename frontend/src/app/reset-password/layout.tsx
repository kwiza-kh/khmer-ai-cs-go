import type { Metadata } from "next";

// The reset page's URL carries a one-time token in the query string. Two
// browser-level guards keep it from leaking beyond the tab that already has it:
// search engines must not index the page, and a navigation must not send the
// token in a Referer header (the site-wide policy is strict-origin-when-cross-
// origin, which still includes the full URL on same-origin navigations).
export const metadata: Metadata = {
  title: "Reset password | RelayChat",
  robots: { index: false, follow: false },
  referrer: "no-referrer",
};

export default function ResetPasswordLayout({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
