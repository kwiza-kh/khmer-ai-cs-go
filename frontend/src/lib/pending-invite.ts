// The invite code has to survive a login round trip (including the Google /
// Telegram redirect), so it is parked in sessionStorage and the login page
// sends the visitor back to /join. sessionStorage rather than localStorage:
// the code is a credential, and it should die with the tab.
//
// Every access is guarded — sessionStorage throws in some privacy modes, and a
// blocked invite must not take the login page down with it.
const PENDING_INVITE_KEY = "pending_invite";

export function stashPendingInvite(code: string): void {
  try {
    sessionStorage.setItem(PENDING_INVITE_KEY, code);
  } catch {
    /* storage blocked — the visitor can open the link again after signing in */
  }
}

export function peekPendingInvite(): string | null {
  try {
    return sessionStorage.getItem(PENDING_INVITE_KEY);
  } catch {
    return null;
  }
}

export function clearPendingInvite(): void {
  try {
    sessionStorage.removeItem(PENDING_INVITE_KEY);
  } catch {
    /* nothing to clean up */
  }
}
