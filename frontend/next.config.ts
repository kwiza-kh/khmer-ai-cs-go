import type { NextConfig } from "next";
import path from "node:path";
// `next/constants.js` (not the extensionless `next/constants`): Next 16 may
// load next.config.ts through Node's native TS loader, and Node's ESM
// resolution refuses extensionless subpaths — writing the extension avoids a
// fallback to the SWC require-hook (which logs a failure warning).
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";

// NEXT_PUBLIC_API_URL is inlined into the CLIENT bundle at build time (see
// API_BASE in src/lib/auth-client.tsx, consumed by the widget page too), so a
// missing value is not recoverable at runtime: the Dockerfile's ARG default
// bakes http://localhost:8080/api/v1 into the bundle, and a widget embedded on
// a third-party site then makes every visitor call their OWN localhost. The
// failure is total and silent, so production builds must fail loudly instead.
// Local docker-compose runs are the one legitimate exception — they opt in with
// NEXT_PUBLIC_ALLOW_LOCALHOST_API_URL=1 and get a loud warning instead.
function assertPublicApiUrl(value: string | undefined) {
  const raw = (value ?? "").trim();
  const allowLocalhost = process.env.NEXT_PUBLIC_ALLOW_LOCALHOST_API_URL === "1";
  if (!raw) {
    throw new Error(
      "NEXT_PUBLIC_API_URL is not set. It is baked into the browser bundle at build time, " +
        "so `next build` refuses to fall back to the Dockerfile default " +
        "(http://localhost:8080/api/v1). Build with e.g. " +
        "`NEXT_PUBLIC_API_URL=https://<domain>/api/v1 next build` " +
        "(docker: `--build-arg NEXT_PUBLIC_API_URL=https://<domain>/api/v1`).",
    );
  }
  if (/^(https?:\/\/)?(localhost|127\.\d+\.\d+\.\d+|0\.0\.0\.0|\[::1\])(?=[:/]|$)/i.test(raw)) {
    const detail =
      `NEXT_PUBLIC_API_URL is a localhost address ("${raw}"). In a production build that URL is ` +
      "baked into the bundle and every visitor (especially inside the embedded widget on a " +
      "third-party site) calls their own machine. Use the deployed origin, e.g. " +
      "`NEXT_PUBLIC_API_URL=https://<domain>/api/v1`.";
    if (!allowLocalhost) throw new Error(detail);
    console.warn(`[next.config] ${detail} Building anyway because ` +
      "NEXT_PUBLIC_ALLOW_LOCALHOST_API_URL=1 (local docker-compose only — an image built this way " +
      "must never be published).");
  }
}

export default (phase: string): NextConfig => {
  // Only the real production build is checked: `next dev` and the lint/type
  // tasks legitimately run without a deployed URL.
  if (phase === PHASE_PRODUCTION_BUILD) {
    assertPublicApiUrl(process.env.NEXT_PUBLIC_API_URL);
    const raw = (process.env.NEXT_PUBLIC_API_URL ?? "").trim();
    try {
      const parsed = new URL(raw);
      if (parsed.pathname.replace(/\/$/, "") !== "/api/v1") {
        console.warn(
          `[next.config] NEXT_PUBLIC_API_URL ("${raw}") does not end in /api/v1 — the bundle ` +
            "prepends no path of its own, so API calls will 404 unless the backend is mounted there.",
        );
      }
    } catch {
      // Not an absolute URL: the value is passed through to fetch as-is.
      console.warn(`[next.config] NEXT_PUBLIC_API_URL ("${raw}") is not an absolute URL.`);
    }
  }

  return {
    // Produce a self-contained server bundle at .next/standalone — required for
    // the multi-stage Docker build (no node_modules in the runtime image).
    output: "standalone",
    // Audit: hide the X-Powered-By: Next.js fingerprint header.
    poweredByHeader: false,
    // Allow dev-server access via 127.0.0.1 (and LAN IP). Without this, Next.js
    // 16/Turbopack blocks HMR + font/dev resources as cross-origin when the host
    // is anything other than "localhost", so the app NEVER hydrates — pages show
    // frozen SSR markup (e.g. all admin tabs rendered at once, no interactivity).
    allowedDevOrigins: ["127.0.0.1", "0.0.0.0"],
    turbopack: {
      root: path.resolve(__dirname),
    },
  };
};
