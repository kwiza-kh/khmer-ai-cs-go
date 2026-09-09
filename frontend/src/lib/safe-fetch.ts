// Network-aware response handling: raw fetch rejects with a bare TypeError
// ("Failed to fetch") on offline/DNS/unreachable, which is useless to a shop
// owner. This helper converts that rejection into an actionable, localized
// message.
//
// It deliberately takes the in-flight promise rather than a URL, so no request
// target ever passes through this module — callers keep full control of what
// they fetch, and there is no URL-rewriting surface here.
import { localizeCurrentLang } from "./api-errors";

export class NetworkError extends Error {
  status = 0;
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

/** settle — await a fetch promise, mapping transport failures to a clear message. */
export async function settle(pending: Promise<Response>): Promise<Response> {
  try {
    return await pending;
  } catch {
    const offline = typeof navigator !== "undefined" && !navigator.onLine;
    throw new NetworkError(
      offline
        ? localizeCurrentLang("网络已断开，请检查网络连接后重试")
        : localizeCurrentLang("无法连接服务器，请稍后重试"),
    );
  }
}
