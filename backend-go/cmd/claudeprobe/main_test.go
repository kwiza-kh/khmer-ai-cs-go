package main

import "testing"

// TestVertexBase pins the host rule that the first vertexprobe got wrong: the
// `global` location has NO region prefix (the old probe interpolated
// `global-aiplatform.googleapis.com`, a host that answers a bare HTML 404, and
// reported a false red on a deployment that was serving traffic), while the
// multi-region families live on their own rep hosts and everything else is
// <region>-aiplatform. A probe that disagrees with the serving client is worse
// than no probe.
func TestVertexBase(t *testing.T) {
	cases := map[string]string{
		"global":       "https://aiplatform.googleapis.com",
		"us":           "https://aiplatform.us.rep.googleapis.com",
		"eu":           "https://aiplatform.eu.rep.googleapis.com",
		"us-east5":     "https://us-east5-aiplatform.googleapis.com",
		"europe-west1": "https://europe-west1-aiplatform.googleapis.com",
	}
	for region, want := range cases {
		if got := vertexBase(region); got != want {
			t.Errorf("vertexBase(%q) = %q, want %q", region, got, want)
		}
	}
}
