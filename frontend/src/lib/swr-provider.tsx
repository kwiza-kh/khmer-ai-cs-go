"use client";

import * as React from "react";
import { SWRConfig } from "swr";
import { ApiError, apiFetch } from "@/lib/api";

/**
 * Global SWR config:
 *  - `fetcher`: typed wrapper around apiFetch (auto-adds token, throws ApiError)
 *  - `onError`: surface 401 (expired token) by reloading to /login via the
 *    AuthGuard; keep other errors per-hook.
 */
export function SWRProvider({ children }: { children: React.ReactNode }) {
  return (
    <SWRConfig
      value={{
        fetcher: (path: string) => apiFetch(path),
        revalidateOnFocus: false,
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
