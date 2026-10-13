/**
 * localStorage access that cannot throw.
 *
 * Every other read of localStorage in this app assumes the browser will hand
 * the value over. A browser can refuse (Safari private mode, storage blocked by
 * policy, a sandboxed iframe) and then the accessor itself throws — which used
 * to break the AuthProvider mount effect (the app never left its loading state),
 * the logout button (the throw landed before router.push) and the language
 * detector (which runs on first render). The guard policy lives here so that
 * there is one place to fix it, and the token/user keys go through
 * session-token.ts on top of these primitives.
 */
export function storageGet(key: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function storageSet(key: string, value: string | null): void {
  if (typeof window === "undefined") return;
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    // Storage unavailable: the in-memory session still works until a reload, and
    // throwing here would break login on a browser that refuses storage.
  }
}

export function storageRemove(key: string): void {
  storageSet(key, null);
}
