package usage

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The tier table is the product's pricing boundary: the console renders it, the
// gates compare against it, and applyPlan writes it. These tests pin the three
// ways it can go wrong silently — a limit that shrinks as the plan gets more
// expensive, an "unlimited" that does not fit the column it is written to, and a
// feature key the console cannot translate (which shows the raw key on the card).

func TestPlansAreOrderedAndMonotonic(t *testing.T) {
	plans := Plans()
	if len(plans) != 3 || plans[0].Name != PlanFree || plans[len(plans)-1].Name != PlanEnterprise {
		t.Fatalf("plans = %v, want free … enterprise", names(plans))
	}
	seen := map[string]bool{}
	for _, p := range plans {
		if seen[p.Name] {
			t.Fatalf("duplicate plan %q", p.Name)
		}
		seen[p.Name] = true
		if p.Messages <= 0 || p.Documents <= 0 || p.Channels <= 0 || p.Seats <= 0 {
			t.Errorf("plan %q has a non-positive limit: %+v", p.Name, p)
		}
		if len(p.Included) == 0 {
			t.Errorf("plan %q advertises nothing", p.Name)
		}
	}
	for i := 1; i < len(plans); i++ {
		prev, cur := plans[i-1], plans[i]
		if cur.Messages < prev.Messages || cur.Documents < prev.Documents || cur.Channels < prev.Channels || cur.Seats < prev.Seats {
			t.Errorf("plan %q is not at least as generous as %q", cur.Name, prev.Name)
		}
		if len(cur.Included) < len(prev.Included) {
			t.Errorf("plan %q advertises fewer features than %q", cur.Name, prev.Name)
		}
	}
}

// Unlimited is written into INTEGER columns, so it has to fit int4. The tier
// table previously said 1<<62 for enterprise, which does not — the UPDATE would
// have failed the first time anyone bought that plan.
func TestUnlimitedFitsTheIntegerColumn(t *testing.T) {
	if Unlimited > math.MaxInt32 {
		t.Fatalf("Unlimited = %d does not fit the INTEGER quota columns (max %d)", Unlimited, math.MaxInt32)
	}
	spec, ok := PlanByName(PlanEnterprise)
	if !ok {
		t.Fatal("enterprise plan missing")
	}
	for name, v := range map[string]int64{"messages": spec.Messages, "documents": spec.Documents, "channels": spec.Channels, "seats": spec.Seats} {
		if v > math.MaxInt32 {
			t.Errorf("enterprise %s = %d overflows int4", name, v)
		}
	}
}

func TestUnknownPlanIsNotUnlimited(t *testing.T) {
	if _, ok := PlanByName("nope"); ok {
		t.Fatal("PlanByName accepted an unknown plan")
	}
	if _, ok := PlanByName(""); ok {
		t.Fatal("PlanByName accepted an empty plan")
	}
}

var featureKeyRe = regexp.MustCompile(`"((?:bl\.feature\.[a-z]+))"\s*:`)

// A key the dictionaries do not define renders as the literal "bl.feature.x" on
// the upgrade card, which is how a missing translation ships unnoticed. Skips
// when the frontend is not next to the backend (a backend-only checkout).
func TestEveryAdvertisedFeatureKeyExistsInTheDictionaries(t *testing.T) {
	languages := []string{"dict-zh.ts", "dict-en.ts", "dict-km.ts"}
	defined := map[string]map[string]bool{}
	for _, lang := range languages {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "lib", "i18n", lang))
		if err != nil {
			t.Skipf("frontend dictionaries unavailable (%v); skipping", err)
		}
		keys := map[string]bool{}
		for _, m := range featureKeyRe.FindAllStringSubmatch(string(raw), -1) {
			keys[m[1]] = true
		}
		defined[lang] = keys
	}
	for _, plan := range Plans() {
		for _, key := range plan.Included {
			if !strings.HasPrefix(key, "bl.feature.") {
				t.Errorf("plan %q advertises %q, which is not a bl.feature.* key", plan.Name, key)
			}
			for lang, keys := range defined {
				if !keys[key] {
					t.Errorf("plan %q advertises %q, missing from %s", plan.Name, key, lang)
				}
			}
		}
	}
}

func names(plans []Plan) []string {
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		out = append(out, p.Name)
	}
	return out
}
