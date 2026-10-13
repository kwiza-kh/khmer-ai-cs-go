import { storageGet, storageRemove, storageSet } from "./storage";

/**
 * The session's storage: the API token and the cached user blob, both under one
 * guarded module.
 *
 * The token lives in localStorage rather than an HttpOnly cookie, deliberately:
 *   * the realtime socket authenticates by passing the token as a WebSocket
 *     sub-protocol (see realtime.tsx) and a browser cannot attach a header or an
 *     HttpOnly cookie to that handshake, so a cookie alone would not remove the
 *     JS-readable copy anyway;
 *   * local development points the frontend (localhost:3001) at the API on a
 *     different origin, where a Secure cookie is refused outright.
 * The cost is that any script running on this origin can read the token, and
 * the deployed CSP does not close that door: nginx serves
 * `script-src 'self' 'unsafe-inline' https://connect.facebook.net` (see
 * deploy-khmer-ai-cs/references/deploy-commands.md §8), where 'unsafe-inline' is
 * what the Next bootstrap needs today. An earlier version of this comment
 * claimed script-src was 'self' only; that was wrong, and tightening it (a nonce
 * for the bootstrap, dropping 'unsafe-inline') is the real follow-up, not a
 * comment edit. Until then the app's own defences are: no raw HTML
 * (no dangerouslySetInnerHTML, no rehype-raw), https-only links (safe-href.ts),
 * and a server that validates stored payloads (inbox_handlers.go agentReply).
 *
 * Moving to a cookie is a deliberate change, not a cleanup: the socket must
 * authenticate from the cookie, the same-site POSTs need a CSRF story, and dev
 * needs a fallback. The reasoning is recorded here so the next reader finds it
 * with the code instead of re-deciding it. Every call site goes through this
 * module, for reads and for writes, so there is exactly one place to change.
 */
const TOKEN_KEY = "token";
const USER_KEY = "user";

/** The API token, or null when signed out / storage is unavailable. */
export function sessionToken(): string | null {
  return storageGet(TOKEN_KEY);
}

/**
 * Store (or clear) the session token. The only writer for this key, so the
 * "one place to change" claim above is true of writes too — it was not while
 * five call sites in auth-client.tsx wrote the key directly.
 */
export function setSessionToken(token: string | null): void {
  storageSet(TOKEN_KEY, token);
}

/** Drop the stored session token (logout, expired token, corrupted cache). */
export function clearSessionToken(): void {
  storageRemove(TOKEN_KEY);
}

/**
 * The cached user blob written next to the token. Guarded here because the same
 * browsers that refuse storage also refuse this key, and an unguarded read in
 * the AuthProvider mount effect left the whole app in its loading state.
 */
export function sessionUser(): string | null {
  return storageGet(USER_KEY);
}

export function setSessionUser(user: string | null): void {
  storageSet(USER_KEY, user);
}

export function clearSessionUser(): void {
  storageRemove(USER_KEY);
}
