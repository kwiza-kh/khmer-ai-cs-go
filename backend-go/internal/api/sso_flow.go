package api

import (
	"khmer-ai-cs-go/internal/platform"
	"net/http"
	"time"
)

// SSO flow binding: the login-CSRF fix for the Google and Telegram flows.
//
// The state and one-time login code used to be unbound Redis keys, so an
// attacker who completed their own consent round-trip could hand the victim's
// browser a callback URL with the attacker's state and code — the SPA would
// silently exchange it and the victim would be logged into the attacker's
// account. The fix binds every flow to a cookie (sso_flow) that only the
// browser which called /start can present:
//
//	/start    mints flowID, sets the cookie, stores flowID -> state in Redis
//	/callback requires cookie(flowID) -> state == presented state
//	          and embeds the state in the login-code payload
//	/exchange requires cookie(flowID) -> state == payload.state again, then
//	          deletes the flow and the code (single use)
//
// An attacker cannot set this cookie in the victim's browser (HttpOnly, set
// only by this server), so a callback or exchange without the initiating
// browser's cookie — or with a state that does not match the cookie's flow —
// is rejected.
const ssoFlowCookie = "sso_flow"

const ssoFlowTTL = 10 * time.Minute

// ssoFlowStart stores the flow binding and sets the cookie. Call it from the
// /start handlers after minting the state.
func (a *App) ssoFlowStart(w http.ResponseWriter, r *http.Request, provider, state string) {
	flowID := platform.NewUUID()
	_ = a.Redis.SetString(r.Context(), provider+"-flow:"+flowID, state, ssoFlowTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     ssoFlowCookie,
		Value:    flowID,
		Path:     "/api/v1/auth/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ssoFlowTTL.Seconds()),
	})
}

// ssoFlowMatch reports whether the request carries the flow cookie bound to
// the presented state. Call it from /callback and /exchange.
func (a *App) ssoFlowMatch(r *http.Request, provider, state string) bool {
	c, err := r.Cookie(ssoFlowCookie)
	if err != nil || c.Value == "" {
		return false
	}
	v, err := a.Redis.GetString(r.Context(), provider+"-flow:"+c.Value)
	return err == nil && v != "" && v == state
}

// ssoFlowConsume deletes the flow binding after a successful exchange so the
// whole flow is single use.
func (a *App) ssoFlowConsume(r *http.Request, provider string) {
	if c, err := r.Cookie(ssoFlowCookie); err == nil && c.Value != "" {
		_ = a.Redis.Del(r.Context(), provider+"-flow:"+c.Value)
	}
}
