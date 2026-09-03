import type { NextConfig } from "next";
import path from "node:path";

const nextConfig: NextConfig = {
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

export default nextConfig;
