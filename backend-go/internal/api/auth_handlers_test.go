package api

import "testing"

func TestEmailValidation(t *testing.T) {
	cases := map[string]bool{
		"a@b.co":                 true,
		"khmer.user@example.com": true,
		"nope":                   false,
		"a@b":                    false,
		"@b.co":                  false,
	}
	for email, want := range cases {
		if got := isValidEmail(email); got != want {
			t.Errorf("isValidEmail(%q) = %v, want %v", email, got, want)
		}
	}
}
