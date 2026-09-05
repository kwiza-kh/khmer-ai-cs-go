// Khmer AI — website chat widget embed.
//
// Usage (paste before </body> on your site):
//   <script src="https://<your-host>/widget-embed.js"
//           data-token="wt_xxx"
//           data-api="https://api.example.com/api/v1"
//           data-lang="km" defer></script>
//
// It renders a floating bubble that opens the chat panel in an iframe served
// by the Next.js app (/widget). Nothing here requires the host page to have
// any framework; plain ES5-safe DOM APIs only.
(function () {
  "use strict";
  var script = document.currentScript || (function () {
    var all = document.getElementsByTagName("script");
    for (var i = all.length - 1; i >= 0; i--) {
      if (all[i].src && all[i].src.indexOf("widget-embed.js") >= 0) return all[i];
    }
    return null;
  })();
  if (!script) return;

  var token = script.getAttribute("data-token") || "";
  var api = script.getAttribute("data-api") || "";
  var lang = script.getAttribute("data-lang") || "km";
  var color = script.getAttribute("data-color") || "#4f46e5";
  var origin = script.getAttribute("data-app") || (script.src ? new URL(script.src).origin : "");
  if (!token || !api || !origin) {
    console.warn("[khmer-widget] missing data-token / data-api / script origin");
    return;
  }

  function boot() {
    if (document.getElementById("khmer-widget-root")) return;

    var host = document.createElement("div");
    host.id = "khmer-widget-root";
    host.style.cssText = "position:fixed;right:20px;bottom:20px;z-index:2147483000;font-family:'Inter',system-ui,sans-serif;";

    var frame = document.createElement("iframe");
    frame.id = "khmer-widget-frame";
    frame.setAttribute("title", "Support chat");
    frame.src = origin + "/widget?t=" + encodeURIComponent(token) +
      "&api=" + encodeURIComponent(api) + "&lang=" + encodeURIComponent(lang) +
      "&color=" + encodeURIComponent(color);
    frame.style.cssText = "display:none;width:360px;max-width:calc(100vw - 32px);height:520px;max-height:calc(100vh - 110px);" +
      "border:0;border-radius:14px;box-shadow:0 18px 48px -12px rgba(17,20,45,0.35);background:#fff;";

    var btn = document.createElement("button");
    btn.type = "button";
    btn.setAttribute("aria-label", "Open support chat");
    btn.innerHTML =
      '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M21 11.5a8.38 8.38 0 0 1-8.5 8.5 8.5 8.5 0 0 1-3.8-.9L3 21l1.9-5.7a8.5 8.5 0 1 1 16.1-3.8z"/></svg>';
    btn.style.cssText = "display:flex;align-items:center;justify-content:center;width:56px;height:56px;border-radius:50%;" +
      "border:none;background:" + color + ";cursor:pointer;box-shadow:0 10px 28px -8px rgba(17,20,45,0.45);" +
      "transition:transform .15s ease;margin-left:auto;";
    btn.onmouseenter = function () { btn.style.transform = "scale(1.06)"; };
    btn.onmouseleave = function () { btn.style.transform = "scale(1)"; };
    btn.onclick = function () {
      var open = frame.style.display !== "none";
      frame.style.display = open ? "none" : "block";
      if (!open) {
        try { frame.contentWindow.postMessage({ khmerWidgetFocus: true }, origin); } catch { /* noop */ }
      }
    };

    var column = document.createElement("div");
    column.style.cssText = "display:flex;flex-direction:column;align-items:flex-end;gap:12px;";
    host.appendChild(column);
    column.appendChild(frame);
    column.appendChild(btn);
    document.body.appendChild(host);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
