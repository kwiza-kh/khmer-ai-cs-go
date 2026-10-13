"use client";

import Link from "next/link";

// Route-level error boundary. Without this file every client-side render error
// falls through to Next's built-in global error screen ("This page couldn't
// load", with no way back into the app) — a dead end for a customer-service
// console. This one keeps the error text visible so a user can send us the
// actual message, and offers a retry that re-renders the segment.

export default function RouteError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <div className="flex min-h-[60vh] items-center justify-center p-6">
      <div className="w-full max-w-md space-y-4 rounded-lg border border-border bg-card p-6 text-center shadow-sm">
        <h1 className="text-base font-semibold text-foreground">
          页面加载失败 · Something went wrong
        </h1>
        <p className="text-sm text-muted-foreground">
          请点击重试；若反复出现，请把下面的信息截图发给我们。
        </p>
        <pre className="max-h-32 overflow-auto rounded border border-border bg-muted p-2 text-left text-xs text-muted-foreground">
          {error?.message || String(error)}
          {error?.digest ? `\n(digest: ${error.digest})` : ""}
        </pre>
        <div className="flex justify-center gap-2">
          <button
            type="button"
            onClick={() => reset()}
            className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:opacity-90"
          >
            重试 · Reload
          </button>
          <Link
            href="/"
            className="rounded-md border border-border px-4 py-2 text-sm font-medium text-foreground hover:bg-muted"
          >
            返回首页 · Home
          </Link>
        </div>
      </div>
    </div>
  );
}
