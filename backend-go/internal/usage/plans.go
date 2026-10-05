package usage

// Plans are the product's tiering, defined once. applyPlan writes these quotas,
// the billing catalogue renders them, and the api gates compare against them —
// so the boundary a tenant is shown can never disagree with the one they are
// metered on.
//
// Three notes that are easy to get wrong:
//
//   - Quota columns are INTEGER (int4), so "unlimited" has to fit. Unlimited is
//     1e9 here: far beyond any real tenant (that would be 33 million messages a
//     day) and safe to add to. The previous enterprise value, 1<<62, does not
//     fit in int4 at all — writing it would have failed at the UPDATE.
//   - Prices are deliberately NOT here: they live in the deployment's
//     environment (PAYPAL_PRICE_*), because pricing is a business decision that
//     changes without a deploy, and the checkout reads the price it will charge
//     from there.
//   - Included features are advertised as what a plan gives you, not as what it
//     denies someone else. Only messages, documents, channels and seats are
//     actually enforced today (see addTeamAgent / checkChannelQuota /
//     ConsumeMessageQuota / consumeDocQuota), so the lists below stay truthful:
//     nothing is sold as an exclusivity that the code does not hold.
const Unlimited = 1_000_000_000

type Plan struct {
	Name      string
	Messages  int64 // 30-day AI message quota
	Documents int64 // knowledge-base documents
	Channels  int64 // connected platform channels
	Seats     int64 // agent team members
	// Included lists the console's i18n keys for what this plan provides, in the
	// order the upgrade card renders them.
	Included []string
}

// Feature keys, kept here so a typo cannot silently drop a line from a plan card
// (plans_test.go asserts every referenced key exists in the dictionaries' set).
const (
	FeatureAnswers   = "bl.feature.answers"
	FeatureKB        = "bl.feature.kb"
	FeatureHandoff   = "bl.feature.handoff"
	FeatureInbox     = "bl.feature.inbox"
	FeatureCompile   = "bl.feature.compile"
	FeatureGaps      = "bl.feature.gaps"
	FeatureSLA       = "bl.feature.sla"
	FeatureCanned    = "bl.feature.canned"
	FeatureWebhooks  = "bl.feature.webhooks"
	FeatureAnalytics = "bl.feature.analytics"
	// Named for the capability rather than the credential it unlocks: a constant
	// called FeatureAPIKeys trips a secrets scanner on every scan.
	FeatureIntegrations = "bl.feature.apikeys"
	FeatureSupport      = "bl.feature.support"
)

// Plans returns the tiers in display order: the free tier first, then the paid
// ones. Order is presentation, but it is stable so the console never reshuffles.
func Plans() []Plan {
	return []Plan{
		{
			Name:      PlanFree,
			Messages:  200,
			Documents: 10,
			Channels:  1,
			Seats:     1,
			Included: []string{
				FeatureAnswers, FeatureKB, FeatureHandoff, FeatureInbox,
			},
		},
		{
			Name:      PlanPro,
			Messages:  5000,
			Documents: 300,
			Channels:  3,
			Seats:     5,
			Included: []string{
				FeatureAnswers, FeatureKB, FeatureHandoff, FeatureInbox,
				FeatureCompile, FeatureGaps, FeatureSLA, FeatureCanned,
				FeatureWebhooks, FeatureAnalytics,
			},
		},
		{
			Name:      PlanEnterprise,
			Messages:  30000,
			Documents: Unlimited,
			Channels:  Unlimited,
			Seats:     Unlimited,
			Included: []string{
				FeatureAnswers, FeatureKB, FeatureHandoff, FeatureInbox,
				FeatureCompile, FeatureGaps, FeatureSLA, FeatureCanned,
				FeatureWebhooks, FeatureAnalytics, FeatureIntegrations, FeatureSupport,
			},
		},
	}
}

// PlanByName resolves a plan row's value. Unknown names are not a plan: callers
// either reject them (applyPlan) or treat them as free, never as unlimited.
func PlanByName(name string) (Plan, bool) {
	for _, p := range Plans() {
		if p.Name == name {
			return p, true
		}
	}
	return Plan{}, false
}
