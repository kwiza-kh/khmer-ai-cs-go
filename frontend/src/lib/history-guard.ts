/**
 * A History API that cannot crash the app.
 *
 * Safari (and Firefox) throw SecurityError("The operation is insecure.") from
 * history.replaceState/pushState when they throttle the History API. On
 * 2026-10-16 that hit the owner's Safari and replaced the whole console with
 * the global error screen while opening the app. Our own boot-path calls are
 * guarded where they happen (auth-guard, the redirect pages), but Next's
 * internal HistoryUpdater performs its own replaceState inside a passive
 * effect that no app-level error boundary can reach — the shim below is the
 * only way to keep that one from killing the page.
 *
 * A URL bar that quietly misses a sync is a far smaller failure than a dead
 * console, so a blocked call is swallowed AND logged (the condition stays
 * visible in the console instead of disappearing). The originals remain
 * reachable as `window.__rawHistory` for debugging. Installed once, before
 * hydration, from auth-client.tsx — every module in the client bundle
 * evaluates before Next's first effect runs.
 */
export function installHistoryGuard(): void {
  if (typeof window === "undefined") return;
  const hist = window.history as History & { __guarded?: boolean };
  if (hist.__guarded) return;

  const rawReplace = History.prototype.replaceState;
  const rawPush = History.prototype.pushState;
  (window as unknown as { __rawHistory?: unknown }).__rawHistory = {
    replaceState: rawReplace,
    pushState: rawPush,
  };

  hist.replaceState = function (...args: Parameters<History["replaceState"]>) {
    try {
      rawReplace.apply(hist, args);
    } catch (err) {
      console.warn("history.replaceState was blocked by the browser; URL sync skipped", err);
    }
  };
  hist.pushState = function (...args: Parameters<History["pushState"]>) {
    try {
      rawPush.apply(hist, args);
    } catch (err) {
      console.warn("history.pushState was blocked by the browser; URL sync skipped", err);
    }
  };
  hist.__guarded = true;
}
