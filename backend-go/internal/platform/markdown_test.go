package platform

import "testing"

// TestPlainTextForChat pins the delivery-side flattening: no messenger
// transport sets parse_mode, so the customer's app renders "**bold**"
// literally. The prompt asks for plain text; this is the guard that makes it
// true even when the model ignores the instruction.
func TestPlainTextForChat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the reported price reply",
			in:   "您好！我们主要供应以下产品：\n\n* **EPS PANEL**：单价 **$2.80 / KG**\n* **EPS SHEET (10 kg/m³)**：单价 **$32.00 / m³**\n\n如果您需要订购，欢迎联系我！",
			want: "您好！我们主要供应以下产品：\n\n* EPS PANEL：单价 $2.80 / KG\n* EPS SHEET (10 kg/m³)：单价 $32.00 / m³\n\n如果您需要订购，欢迎联系我！",
		},
		{
			name: "markdown list and headings",
			in:   "## 报价\n- **EPS PANEL**: $2.80/KG\n- EPS SHEET: $32/m³",
			want: "报价\n- EPS PANEL: $2.80/KG\n- EPS SHEET: $32/m³",
		},
		{
			name: "underscore emphasis unwrapped",
			in:   "__Total__ is __$64.00__",
			want: "Total is $64.00",
		},
		{
			name: "code span unwrapped",
			in:   "model `KWF-RO-100` in stock",
			want: "model KWF-RO-100 in stock",
		},
		{
			name: "single underscores in identifiers survive",
			in:   "សម្រាប់ KWF_RO_100 និង 18_handbook_full",
			want: "សម្រាប់ KWF_RO_100 និង 18_handbook_full",
		},
		{
			name: "plain text untouched",
			in:   "សូមអរគុណ! EPS PANEL តម្លៃ $2.80 ក្នុងមួយគីឡូក្រាម។",
			want: "សូមអរគុណ! EPS PANEL តម្លៃ $2.80 ក្នុងមួយគីឡូក្រាម។",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plainTextForChat(tc.in); got != tc.want {
				t.Errorf("plainTextForChat mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
