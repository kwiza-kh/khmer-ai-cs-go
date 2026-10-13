import { isSafeHTTPSURL } from "./safe-href";

/**
 * Open a URL in a new tab, but only when it is an https URL.
 * Every caller passes a URL that came back from our own API (a signed object-store
 * link, a Telegram deep-link) or from a static table in the bundle — never from
 * customer input. The scheme check lives here anyway so that `javascript:` and
 * `data:` can never execute even if a source changes later, and one place owns the
 * rule (lib/safe-href.ts) instead of four copies of it.
 *
 * The boolean means "the URL was safe and the tab was requested", NOT "a tab is
 * definitely on screen": the features string below carries `noreferrer` (which
 * implies `noopener`), and with it window.open returns null whether or not the
 * browser honoured the call — a popup blocker is indistinguishable from a
 * success. An earlier version documented the boolean as "opened" and derived it
 * from that always-null return, so it could never be true.
 *
 * Callers that need a decision must therefore use onRefused (the refusal path,
 * which is exact); the boolean is for the courtesy toast on the success path.
 */
export function openExternalURL(url: string, onRefused?: () => void): boolean {
  if (!isSafeHTTPSURL(url)) {
    onRefused?.();
    return false;
  }
  // pi-lens-ignore: no-open-redirect-js, no-open-redirect -- url was validated as https by the guard above; the rule matches the call shape, not the check.
  window.open(url, "_blank", "noreferrer");
  return true;
}
