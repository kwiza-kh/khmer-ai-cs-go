package anthropic

// Model is one Claude model the console may offer. The catalog is the list of
// models the platform has verified, and a model outside it is refused at save
// time: sampling, thinking and pricing are all per model, so an unknown id would
// be served with defaults nobody measured.
type Model struct {
	ID          string
	DisplayName string
	LaunchStage string

	// Sampling reports whether the model accepts temperature, top_p and top_k.
	// A non-default value is a 400 on every call for models that do not, so the
	// request omits the field entirely for those models.
	Sampling bool

	// ThinkingOff makes requests say thinking.type "disabled" explicitly. Haiku 5.5
	// thinks by default (adaptive, effort medium); its tokens count toward
	// max_tokens and are billed as output. A customer reply needs the whole
	// max_tokens budget for text and a predictable latency, so thinking is off.
	ThinkingOff bool
}

// catalog is ordered for display. claude-haiku-5-5 is the only model the console
// offers today.
var catalog = []Model{
	{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", LaunchStage: "GA", Sampling: false, ThinkingOff: true},
}

// Catalog returns the models the console may offer, in display order.
func Catalog() []Model {
	return append([]Model(nil), catalog...)
}

// Lookup returns the catalog entry for a model id. The id is compared exactly:
// the platform stores the id the API documents, and an alias is not a model.
func Lookup(id string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
