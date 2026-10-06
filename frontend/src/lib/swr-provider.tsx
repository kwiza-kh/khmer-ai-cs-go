"use client";

import * as React from "react";
import { SWRConfig } from "swr";
import { ApiError, apiFetch } from "@/lib/api";

/**
 * Global SWR config:
 *  - `fetcher`: typed wrapper around apiFetch (auto-adds token, throws ApiError)
 *  - `onError`: surface 401 (expired token) by reloading to /login via the
 *    AuthGuard; keep other errors per-hook.
 *
 * Refresh policy (measured 2026-10-06): the console's own endpoints answer in
 * 1–8 ms, so the refresh speed is decided here, not on the server. Two settings
 * carry it:
 *  - revalidateOnFocus was OFF, which only bought staleness — switching back to
 *    the tab showed whatever was fetched when it was first opened. It is on now,
 *    throttled so flipping between tabs costs at most one round trip every 5 s.
 *  - keepPreviousData keeps the current rows on screen while a NEW key loads
 *    (filter/page/tab), instead of blanking to a loading state: at 3 ms per
 *    request the blank flash was the slow part, not the fetch.
 */
export function SWRProvider({ children }: { children: React.ReactNode }) {
  return (
    <SWRConfig
      value={{
        fetcher: (path: string) => apiFetch(path),
        revalidateOnFocus: true,
        focusThrottleInterval: 5000,
        keepPreviousData: true,
        shouldRetryOnError: (err) => {
          // Don't retry on auth / client errors — they won't fix themselves.
          if (err instanceof ApiError) return err.status >= 500;
          return false;
        },
        errorRetryCount: 2,
        dedupingInterval: 2000,
      }}
    >
      {children}
    </SWRConfig>
  );
}
