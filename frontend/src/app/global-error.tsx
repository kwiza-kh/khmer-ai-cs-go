"use client";

// Root-level error boundary. Next renders this file when an error escapes the
// root layout itself (the route-level src/app/error.tsx cannot catch those), so
// it has to supply its own <html>/<body> and must not rely on globals.css —
// hence inline styles. Without it the user gets Next's built-in screen, which
// shows no error text at all.

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const card: React.CSSProperties = {
    maxWidth: 460,
    width: "100%",
    border: "1px solid #e5e7eb",
    borderRadius: 12,
    background: "#fff",
    padding: 24,
    textAlign: "center",
    boxShadow: "0 1px 3px rgba(0,0,0,0.08)",
  };

  return (
    <html lang="zh">
      <body
        style={{
          margin: 0,
          minHeight: "100vh",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          padding: 24,
          background: "#f8fafc",
          color: "#0f172a",
          fontFamily:
            "system-ui, -apple-system, 'Segoe UI', Roboto, 'Noto Sans Khmer', sans-serif",
        }}
      >
        <div style={card}>
          <h1 style={{ margin: "0 0 8px", fontSize: 17, fontWeight: 600 }}>
            页面加载失败 · Something went wrong
          </h1>
          <p style={{ margin: "0 0 12px", fontSize: 14, color: "#475569" }}>
            请点击重试；若反复出现，请把下面的信息截图发给我们。
          </p>
          <pre
            style={{
              margin: "0 0 16px",
              maxHeight: 120,
              overflow: "auto",
              textAlign: "left",
              fontSize: 12,
              color: "#475569",
              background: "#f1f5f9",
              border: "1px solid #e2e8f0",
              borderRadius: 8,
              padding: 8,
            }}
          >
            {error?.message || String(error)}
            {error?.digest ? `\n(digest: ${error.digest})` : ""}
          </pre>
          <div style={{ display: "flex", gap: 8, justifyContent: "center" }}>
            <button
              type="button"
              onClick={() => reset()}
              style={{
                border: 0,
                borderRadius: 8,
                background: "#0f172a",
                color: "#fff",
                padding: "9px 16px",
                fontSize: 14,
                fontWeight: 500,
                cursor: "pointer",
              }}
            >
              重试 · Retry
            </button>
            <button
              type="button"
              onClick={() => window.location.reload()}
              style={{
                borderRadius: 8,
                background: "#fff",
                color: "#0f172a",
                border: "1px solid #cbd5e1",
                padding: "9px 16px",
                fontSize: 14,
                fontWeight: 500,
                cursor: "pointer",
              }}
            >
              刷新页面 · Reload
            </button>
          </div>
        </div>
      </body>
    </html>
  );
}
