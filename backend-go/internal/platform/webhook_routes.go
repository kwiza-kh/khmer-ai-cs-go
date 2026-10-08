// Provider callback routing.
//
// cmd/server used to enumerate every provider callback by hand:
//
//	whMux.HandleFunc("/api/v1/webhook/meta", webhooks.MetaWebhook)
//	whMux.HandleFunc("/api/v1/webhook/whatsapp", webhooks.WhatsAppWebhook)
//	... one line per channel ...
//
// Two things were wrong with that: adding a channel meant editing the server's
// wiring as well as the capability table, and nothing answered "which platforms
// can reach us at all".
//
// The table below is that single place. RegisterWebhookRoutes installs it and a
// unified dispatcher at /api/v1/webhook/{platform}, so a channel whose inbound
// path follows the convention needs one row. The provider-specific paths stay
// registered because they are the URLs merchants already configured in the Meta,
// Telegram, LINE and Zalo consoles - changing them would break live integrations.
package platform

import (
	"net/http"
	"strings"
)

// WebhookBasePath is the prefix every provider callback lives under.
const WebhookBasePath = "/api/v1/webhook/"

// WebhookPlatformAliases maps a platform segment to the route that actually
// serves it. Instagram messaging is delivered to the Meta app webhook (one app,
// one endpoint), so /api/v1/webhook/instagram must reach the Meta handler
// instead of answering 404.
var WebhookPlatformAliases = map[string]string{
	"instagram": "meta",
}

// webhookRouteSpec is one inbound callback. The handler is a method expression so
// the table stays a package-level value rather than allocating per request.
type webhookRouteSpec struct {
	path     string // suffix after WebhookBasePath
	platform string // unified-dispatch segment; "" for sub-resources
	handler  func(*Webhooks) http.HandlerFunc
}

var webhookRouteSpecs = []webhookRouteSpec{
	{path: "meta", platform: "meta", handler: func(wh *Webhooks) http.HandlerFunc { return wh.MetaWebhook }},
	// Meta Data Deletion Request Callback — mandatory under Platform Terms
	// §3(d)(i). Unauthenticated like every other webhook here: the signed_request
	// is the authentication. A sub-resource of meta, not a platform of its own,
	// which is why it carries no dispatch segment.
	{path: "meta/data-deletion", handler: func(wh *Webhooks) http.HandlerFunc { return wh.MetaDataDeletion }},
	{path: "whatsapp", platform: "whatsapp", handler: func(wh *Webhooks) http.HandlerFunc { return wh.WhatsAppWebhook }},
	{path: "telegram", platform: "telegram", handler: func(wh *Webhooks) http.HandlerFunc { return wh.TelegramWebhook }},
	{path: "line", platform: "line", handler: func(wh *Webhooks) http.HandlerFunc { return wh.LineWebhook }},
	{path: "zalo", platform: "zalo", handler: func(wh *Webhooks) http.HandlerFunc { return wh.ZaloWebhook }},
}

// WebhookRoute is a resolved inbound callback.
type WebhookRoute struct {
	Path     string
	Platform string
	Handler  http.HandlerFunc
}

// Routes returns every inbound callback this build serves.
func (wh *Webhooks) Routes() []WebhookRoute {
	out := make([]WebhookRoute, 0, len(webhookRouteSpecs))
	for _, spec := range webhookRouteSpecs {
		out = append(out, WebhookRoute{
			Path:     WebhookBasePath + spec.path,
			Platform: spec.platform,
			Handler:  spec.handler(wh),
		})
	}
	return out
}

// handlerForPlatform resolves a unified-dispatch segment to its callback.
// Unknown segments - and known channels with no inbound callback (the website
// widget) - resolve false so the dispatcher can 404 rather than accept an event
// it cannot attribute.
func (wh *Webhooks) handlerForPlatform(platform string) (http.HandlerFunc, bool) {
	if platform == "" {
		return nil, false
	}
	if alias, ok := WebhookPlatformAliases[platform]; ok {
		platform = alias
	}
	for _, spec := range webhookRouteSpecs {
		if spec.platform == platform {
			return spec.handler(wh), true
		}
	}
	return nil, false
}

// UnifiedWebhook serves /api/v1/webhook/{platform} with the same handler as the
// provider-specific path.
func (wh *Webhooks) UnifiedWebhook(w http.ResponseWriter, r *http.Request) {
	segment := strings.Trim(strings.TrimPrefix(r.URL.Path, WebhookBasePath), "/")
	if segment == "" || strings.Contains(segment, "/") {
		// Sub-resources (meta/data-deletion) have their own exact route; letting
		// them through here would serve the wrong handler.
		http.NotFound(w, r)
		return
	}
	h, ok := wh.handlerForPlatform(segment)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h(w, r)
}

// RegisterWebhookRoutes installs every provider callback plus the unified
// dispatcher on mux. cmd/server calls this once instead of enumerating channels.
func RegisterWebhookRoutes(mux *http.ServeMux, wh *Webhooks) {
	for _, rt := range wh.Routes() {
		mux.HandleFunc(rt.Path, rt.Handler)
	}
	mux.HandleFunc(WebhookBasePath, wh.UnifiedWebhook)
}
