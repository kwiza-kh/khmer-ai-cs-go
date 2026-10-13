package platform

import "testing"

// The business-hours comparison decides two customer-visible things: whether the
// after-hours preamble answers a message (isOpenNow), and whether a
// business-hours-only SLA policy holds its clock (the API's withinBusinessHours
// calls the same function). It is pinned here rather than only through a
// database test, because an off-by-one at the boundary is the difference between
// "closed" and "a breach recorded at 17:00".
func TestWithinWindow(t *testing.T) {
	cases := []struct {
		name, open, close, hm string
		want                  bool
	}{
		{"inside", "09:00", "17:00", "12:30", true},
		{"at open", "09:00", "17:00", "09:00", true},
		{"one minute before close", "09:00", "17:00", "16:59", true},
		{"at close", "09:00", "17:00", "17:00", false},
		{"before open", "09:00", "17:00", "08:59", false},
		{"row exists but has no times (closed all day)", "", "", "12:00", false},
		{"half-filled row is treated as closed", "09:00", "", "12:00", false},
		// An overnight shift is the case the plain >= && < comparison got wrong:
		// it answered "closed" for the whole night a night-shift tenant staffed.
		{"overnight, before midnight", "22:00", "02:00", "23:00", true},
		{"overnight, at open", "22:00", "02:00", "22:00", true},
		{"overnight, after midnight", "22:00", "02:00", "01:59", true},
		{"overnight, at close", "22:00", "02:00", "02:00", false},
		{"overnight, daytime is outside", "22:00", "02:00", "12:00", false},
		{"open equals close is treated as always open", "09:00", "09:00", "03:00", true},
	}
	for _, tc := range cases {
		if got := WithinWindow(tc.open, tc.close, tc.hm); got != tc.want {
			t.Errorf("%s: WithinWindow(%q, %q, %q) = %v, want %v", tc.name, tc.open, tc.close, tc.hm, got, tc.want)
		}
	}
}
