package platform

import "testing"

// Two tenants can save the same Telegram webhook secret — nothing indexes
// webhook_secret_hash (015/051 index the provider *identities*, not the
// secret). Routing such a hash to the lowest config_id would deliver one
// tenant's customers into the other's inbox with a valid signature, so the
// rule is: exactly one match routes, several refuse.
func TestTelegramWebhookRoutingRefusesDuplicateSecrets(t *testing.T) {
	cases := []struct {
		matches int
		want    telegramRoute
	}{
		{0, telegramNoMatch}, // pre-051 config without a hash: legacy lookup
		{1, telegramRouted},
		{2, telegramAmbiguous},
		{7, telegramAmbiguous},
	}
	for _, tc := range cases {
		if got := classifyTelegramMatches(tc.matches); got != tc.want {
			t.Errorf("classifyTelegramMatches(%d) = %d, want %d", tc.matches, got, tc.want)
		}
	}
}
