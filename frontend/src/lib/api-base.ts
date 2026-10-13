/**
 * The API base URL baked into the bundle at build time.
 *
 * There is deliberately no fallback. Every former call site read
 * `process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1"`, and 8080 is
 * the port of the Rust backend this service replaced: a build that forgot the
 * variable sent every request to a dead port, and the only symptom was the same
 * Chinese "网络错误" toast an offline laptop shows — which is how a whole release
 * shipped pointing at nothing. Failing here instead names the missing variable at
 * the moment the bundle loads. The deploy runbook already requires it (AGENTS.md:
 * "NEXT_PUBLIC_API_URL must be set: it is baked into the bundle"), so a build
 * without it is a mistake that should never reach a customer's screen.
 */
export function apiBaseURL(): string {
  const base = process.env.NEXT_PUBLIC_API_URL;
  if (!base) {
    throw new Error(
      "NEXT_PUBLIC_API_URL is not set. It is baked into the bundle at build time — " +
        "set it before `npm run build` (see AGENTS.md and deploy-khmer-ai-cs/SKILL.md).",
    );
  }
  return base.replace(/\/+$/, "");
}
