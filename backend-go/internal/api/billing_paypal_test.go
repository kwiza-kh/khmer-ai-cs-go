package api

import (
	"testing"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/paypal"
	"khmer-ai-cs-go/internal/usage"
)

// The money path's decisions that do not need a database: what a plan costs, and
// whether what PayPal captured is what the order asked for. Both are pure, and
// both are the kind of check that silently grants a paid plan if it is wrong.

func TestNormalizeAmount(t *testing.T) {
	cases := map[string]string{
		"29":       "29",
		"29.00":    "29",
		"29.0":     "29",
		"29.50":    "29.5",
		"29.99":    "29.99",
		" 12.30  ": "12.3",
		"":         "",
		"0":        "0",
	}
	for in, want := range cases {
		if got := normalizeAmount(in); got != want {
			t.Errorf("normalizeAmount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAmountMatches(t *testing.T) {
	cases := []struct {
		name                                  string
		captured, expected, cur, wantCurrency string
		want                                  bool
	}{
		{"identical", "29.00", "29.00", "USD", "USD", true},
		{"paypal strips trailing zeros", "29.0", "29.00", "USD", "USD", true},
		{"integer form", "29", "29.00", "USD", "USD", true},
		{"currency case", "29.00", "29.00", "usd", "USD", true},
		{"underpaid", "28.99", "29.00", "USD", "USD", false},
		{"overpaid is still a mismatch", "290.00", "29.00", "USD", "USD", false},
		{"wrong currency", "29.00", "29.00", "EUR", "USD", false},
		{"empty capture", "", "29.00", "USD", "USD", false},
		{"empty expected", "29.00", "", "USD", "USD", false},
		{"zero against a real price", "0.00", "29.00", "USD", "USD", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := amountMatches(tc.captured, tc.expected, tc.cur, tc.wantCurrency); got != tc.want {
				t.Errorf("amountMatches(%q, %q, %q, %q) = %v, want %v",
					tc.captured, tc.expected, tc.cur, tc.wantCurrency, got, tc.want)
			}
		})
	}
}

// A plan is sellable only when a price is configured; free is never for sale, and
// an unconfigured price must not read as "free".
func TestBillingPlanPrice(t *testing.T) {
	app := &App{Cfg: &config.Config{PayPal: config.PayPalConfig{PricePro: " 29.00 ", PriceEnterprise: ""}}}
	if price, ok := app.billingPlanPrice(usage.PlanPro); !ok || price != "29.00" {
		t.Errorf("pro price = %q, %v; want 29.00, true", price, ok)
	}
	if _, ok := app.billingPlanPrice(usage.PlanEnterprise); ok {
		t.Error("enterprise reported as purchasable with no configured price")
	}
	if _, ok := app.billingPlanPrice(usage.PlanFree); ok {
		t.Error("free reported as purchasable")
	}
	if _, ok := app.billingPlanPrice("some-unknown-plan"); ok {
		t.Error("unknown plan reported as purchasable")
	}
}

// paypalEnabled gates every checkout endpoint, so it has to survive the two
// states a deployment can be in: no client at all (tests, not selling yet) and a
// client without credentials.
func TestPaypalEnabledIsNilSafe(t *testing.T) {
	if (&App{}).paypalEnabled() {
		t.Error("nil client reported as enabled")
	}
	blank := &App{PayPal: paypal.New(paypal.Config{})}
	if blank.paypalEnabled() {
		t.Error("client without credentials reported as enabled")
	}
	ready := &App{PayPal: paypal.New(paypal.Config{ClientID: "id", ClientSecret: "secret"})}
	if !ready.paypalEnabled() {
		t.Error("configured client reported as disabled")
	}
}

// The return URL is where PayPal sends the buyer back; deriving it from the API
// URL keeps a deployment from returning customers to the wrong host.
func TestBillingReturnURLDerivesFromPublicAPIURL(t *testing.T) {
	app := &App{Cfg: &config.Config{Server: config.ServerConfig{PublicAPIURL: "https://cs.example/api/v1"}}}
	if got := app.billingReturnURL("return"); got != "https://cs.example/billing?paypal=return" {
		t.Errorf("return URL = %q", got)
	}
	if got := app.billingReturnURL("cancel"); got != "https://cs.example/billing?paypal=cancel" {
		t.Errorf("cancel URL = %q", got)
	}
	empty := &App{Cfg: &config.Config{}}
	if got := empty.billingReturnURL("return"); got != "" {
		t.Errorf("empty public API URL produced %q, want empty", got)
	}
}
