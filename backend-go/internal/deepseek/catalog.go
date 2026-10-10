package deepseek

// Model is one DeepSeek model the console may offer. The catalog is the list of
// models the platform has verified, and a model outside it is refused at save
// time: sampling, thinking and pricing are all per model, so an unknown id would
// be served with defaults nobody measured.
type Model struct {
	ID          string
	DisplayName string
	LaunchStage string

	// Sampling reports whether the model takes a temperature. DeepSeek ignores
	// temperature in thinking mode and applies it in non-thinking mode, which is
	// the mode this client requests (see ThinkingOff).
	Sampling bool

	// ThinkingOff makes requests say thinking.type "disabled". Thinking is ON by
	// default on this platform and its tokens bill as output at the output rate,
	// on top of seconds of latency; a customer reply needs neither. The catalog
	// maps the flag so a model that ever requires thinking can be added without
	// touching the request builder.
	ThinkingOff bool
}

// catalog is ordered for display. New DeepSeek model ids must be verified
// against the API before they are added here: a wrong id is a 404 on every
// customer turn, and the save-time refusal below is the only thing that keeps
// one out of the database.
var catalog = []Model{
	{ID: "deepseek-flash", DisplayName: "DeepSeek V4.1 Flash", LaunchStage: "GA", Sampling: true, ThinkingOff: true},
	{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", LaunchStage: "GA", Sampling: true, ThinkingOff: true},
}

// Catalog returns the models the console may offer, in display order.
func Catalog() []Model {
	return append([]Model(nil), catalog...)
}

// Lookup returns the catalog entry for a model id. The id is compared exactly:
// the platform stores the id the API documents, and the legacy deepseek-v4-flash
// alias (billed at the Flash price) is not a model.
func Lookup(id string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
