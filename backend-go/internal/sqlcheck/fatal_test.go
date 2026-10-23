package sqlcheck

import "testing"

// The gate used to treat 42P08 ("inconsistent types deduced for parameter $N")
// as an unparseable fragment and pass. That is exactly how a real INSERT …
// SELECT defect reached production on 2026-10-23; it is now fatal, while the
// genuinely ambiguous "could not determine data type" stays a skip.
func TestIsFatalCode(t *testing.T) {
	fatal := []string{codeUndefinedColumn, codeUndefinedTable, codeUndefinedObject, codeUndefinedFunction, codeParamsInconsistent}
	for _, c := range fatal {
		if !isFatalCode(c) {
			t.Errorf("%s must fail the gate", c)
		}
	}
	for _, c := range []string{"", "42601", "42P10", "?"} {
		if isFatalCode(c) {
			t.Errorf("%s: syntax/extraction artifacts must stay non-fatal", c)
		}
	}
}
