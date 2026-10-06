// RelayChat — website chat widget embed.
//
// Usage (paste before </body> on your site):
//   <script src="https://<your-host>/widget-embed.js"
//           data-token="wt_xxx"
//           data-lang="km" defer></script>
//
// The API base is baked into the /widget page at build time; there is no
// data-api attribute. (One used to exist and was passed through as ?api=,
// which the page stopped reading when trusting it would have let any page
// frame the widget with someone else's token.) Keeping data-api here would
// only promise a knob that does nothing.
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
  var lang = script.getAttribute("data-lang") || "km";
  var color = script.getAttribute("data-color") || "#4f46e5";
  // script.src is a resolved absolute URL, but a malformed data-app or a
  // sandboxed document can still make URL() throw — fall back to no origin
  // rather than aborting the whole embed.
  var origin = script.getAttribute("data-app") || "";
  if (!origin && script.src) {
    try { origin = new URL(script.src).origin; } catch { origin = ""; }
  }
  if (!token || !origin) {
    console.warn("[khmer-widget] missing data-token / script origin");
    return;
  }

  // Panel open/close transition, launcher attention pulse, unread badge.
  var STYLE =
    "@keyframes kw-launch{0%{box-shadow:0 0 0 0 rgba(17,20,45,.35)}70%{box-shadow:0 0 0 14px rgba(17,20,45,0)}100%{box-shadow:0 0 0 0 rgba(17,20,45,0)}}" +
    "#khmer-widget-frame{opacity:0;transform:translateY(10px) scale(.96);transform-origin:bottom right;" +
    "transition:opacity .18s ease,transform .18s ease,visibility 0s linear .18s;visibility:hidden;pointer-events:none;}" +
    "#khmer-widget-frame.kw-open{opacity:1;transform:translateY(0) scale(1);visibility:visible;pointer-events:auto;transition:opacity .18s ease,transform .18s ease;}" +
    "#khmer-widget-btn{animation:kw-launch 1.6s ease-out .8s 2;}" +
    "#khmer-widget-badge{position:absolute;top:1px;right:1px;width:13px;height:13px;border-radius:50%;" +
    "background:#ef4444;border:2px solid #fff;display:none;}";

  function boot() {
    if (document.getElementById("khmer-widget-root")) return;

    var style = document.createElement("style");
    style.textContent = STYLE;
    document.head.appendChild(style);

    var host = document.createElement("div");
    host.id = "khmer-widget-root";
    host.style.cssText = "position:fixed;right:20px;bottom:20px;z-index:2147483000;font-family:'Inter',system-ui,sans-serif;";

    var frame = document.createElement("iframe");
    frame.id = "khmer-widget-frame";
    frame.setAttribute("title", "Support chat");
    frame.src = origin + "/widget?t=" + encodeURIComponent(token) +
      "&lang=" + encodeURIComponent(lang) +
      "&color=" + encodeURIComponent(color);
    frame.style.cssText = "width:360px;max-width:calc(100vw - 32px);height:520px;max-height:calc(100vh - 110px);" +
      "border:0;border-radius:14px;box-shadow:0 18px 48px -12px rgba(17,20,45,0.35);background:#fff;";

    var CHAT_SVG =
      '<svg xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M21 11.5a8.38 8.38 0 0 1-8.5 8.5 8.5 8.5 0 0 1-3.8-.9L3 21l1.9-5.7a8.5 8.5 0 1 1 16.1-3.8z"/></svg>';
    var CLOSE_SVG =
      '<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2.4" stroke-linecap="round">' +
      '<path d="M6 6l12 12M18 6L6 18"/></svg>';

    // The two icons are constants, so innerHTML would be safe today — but this
    // script is pasted into other people's pages, and neither a scanner nor a
    // reviewer should have to re-derive that nothing variable reaches a markup
    // sink. Parse the static markup into nodes instead. The xmlns is what makes
    // this work: XML parsing (unlike HTML's) does NOT put a bare <svg> in the
    // SVG namespace, and a namespace-less svg silently renders nothing.
    var svgNode = function (markup) {
      var parsed = new DOMParser().parseFromString(markup, "image/svg+xml");
      return document.importNode(parsed.documentElement, true);
    };

    var btn = document.createElement("button");
    btn.type = "button";
    btn.id = "khmer-widget-btn";
    btn.setAttribute("aria-label", "Open support chat");
    btn.style.cssText = "position:relative;display:flex;align-items:center;justify-content:center;width:56px;height:56px;border-radius:50%;" +
      "border:none;background:" + color + ";cursor:pointer;box-shadow:0 10px 28px -8px rgba(17,20,45,0.45);" +
      "transition:transform .15s ease;margin-left:auto;";
    btn.onmouseenter = function () { btn.style.transform = "scale(1.06)"; };
    btn.onmouseleave = function () { btn.style.transform = "scale(1)"; };

    var unread = false;
    var badge = null;
    var showLauncher = function () {
      while (btn.firstChild) btn.removeChild(btn.firstChild);
      btn.appendChild(svgNode(CHAT_SVG));
      badge = document.createElement("span");
      badge.id = "khmer-widget-badge";
      badge.style.display = unread ? "block" : "none";
      btn.appendChild(badge);
    };
    showLauncher();

    var setOpen = function (open) {
      if (open) {
        frame.classList.add("kw-open");
        while (btn.firstChild) btn.removeChild(btn.firstChild);
        btn.appendChild(svgNode(CLOSE_SVG));
        unread = false;
        if (badge) badge.style.display = "none";
        try { frame.contentWindow.postMessage({ khmerWidgetFocus: true }, origin); } catch { /* noop */ }
      } else {
        frame.classList.remove("kw-open");
        showLauncher();
      }
    };
    var isOpen = function () { return frame.classList.contains("kw-open"); };

    // Unread ping from the widget iframe (new agent/AI message while closed).
    window.addEventListener("message", function (ev) {
      if (ev.source !== frame.contentWindow) return;
      var d = ev.data;
      if (d && d.khmerWidgetUnread) {
        unread = true;
        // badge stays live: showLauncher() rebuilds it on close, and while the
        // panel is open the launcher children (badge included) are detached.
        if (badge && !isOpen()) badge.style.display = "block";
      }
    });

    btn.onclick = function () { setOpen(!isOpen()); };

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
