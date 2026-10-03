package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The inbound pipeline is now a stage list. These tests pin the driver's
// semantics (order, short-circuit, error) and the per-turn isolation that the
// inboundTurn struct exists to provide — the property that used to depend on
// nobody adding a field to Pipeline.

func recordStages(seen *[]string) []inboundStage {
	return []inboundStage{
		{Name: "one", Run: func(context.Context, *inboundTurn) (bool, error) {
			*seen = append(*seen, "one")
			return true, nil
		}},
		{Name: "two", Run: func(context.Context, *inboundTurn) (bool, error) {
			*seen = append(*seen, "two")
			return false, nil
		}},
		{Name: "three", Run: func(context.Context, *inboundTurn) (bool, error) {
			*seen = append(*seen, "three")
			return true, nil
		}},
	}
}

func TestRunInboundStagesStopsAtTheFirstStageThatSaysSo(t *testing.T) {
	var seen []string
	if err := runInboundStages(context.Background(), recordStages(&seen), &inboundTurn{}); err != nil {
		t.Fatalf("a deliberate stop is not an error: %v", err)
	}
	if strings.Join(seen, ",") != "one,two" {
		t.Fatalf("stages run = %v, want the turn to end at 'two'", seen)
	}
}

func TestRunInboundStagesReturnsTheFirstError(t *testing.T) {
	boom := errors.New("db down")
	var seen []string
	stages := []inboundStage{
		{Name: "one", Run: func(context.Context, *inboundTurn) (bool, error) {
			seen = append(seen, "one")
			return true, nil
		}},
		{Name: "two", Run: func(context.Context, *inboundTurn) (bool, error) {
			seen = append(seen, "two")
			return false, boom
		}},
		{Name: "three", Run: func(context.Context, *inboundTurn) (bool, error) {
			seen = append(seen, "three")
			return true, nil
		}},
	}
	err := runInboundStages(context.Background(), stages, &inboundTurn{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stage's error so the queue can retry", err)
	}
	if strings.Join(seen, ",") != "one,two" {
		t.Fatalf("stages run = %v, want nothing after the failure", seen)
	}
}

func TestRunInboundStagesRunsEveryStageInOrder(t *testing.T) {
	var seen []string
	stages := []inboundStage{
		{Name: "a", Run: func(context.Context, *inboundTurn) (bool, error) { seen = append(seen, "a"); return true, nil }},
		{Name: "b", Run: func(context.Context, *inboundTurn) (bool, error) { seen = append(seen, "b"); return true, nil }},
		{Name: "c", Run: func(context.Context, *inboundTurn) (bool, error) { seen = append(seen, "c"); return true, nil }},
	}
	if err := runInboundStages(context.Background(), stages, &inboundTurn{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(seen, ",") != "a,b,c" {
		t.Fatalf("order = %v", seen)
	}
}

// The reason inboundTurn exists: two tenants served at the same time share the
// stage list but never share turn state. A field added to Pipeline for any of
// this would fail here.
func TestRunInboundStagesKeepsTurnStatePerTurn(t *testing.T) {
	stages := []inboundStage{
		{Name: "stamp", Run: func(_ context.Context, tr *inboundTurn) (bool, error) {
			tr.Reply = "reply-for-" + tr.Content
			tr.SessionID = "session-for-" + tr.Content
			time.Sleep(time.Millisecond) // widen any sharing window
			return true, nil
		}},
		{Name: "verify", Run: func(_ context.Context, tr *inboundTurn) (bool, error) {
			if tr.Reply != "reply-for-"+tr.Content || tr.SessionID != "session-for-"+tr.Content {
				return false, fmt.Errorf("turn state leaked: reply=%q session=%q content=%q", tr.Reply, tr.SessionID, tr.Content)
			}
			return true, nil
		}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := fmt.Sprintf("tenant-%d", i)
			turn := &inboundTurn{Event: &InboundEvent{ConfigID: int32(i)}, Content: content}
			if err := runInboundStages(context.Background(), stages, turn); err != nil {
				t.Errorf("turn %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
}

// The order is a deployment property, so it is pinned here: inserting a stage
// should be a deliberate edit to this list, not an accident.
func TestInboundStageListIsStable(t *testing.T) {
	want := []string{
		"load-config",
		"resolve-profile",
		"prepare-media",
		"ensure-session",
		"reopen-finished-session",
		"release-handoff",
		"escalation-gate",
		"keyword-handoff",
		"bill-message",
		"screen-inbound",
		"prepare-grounding",
		"route-inbound",
		"notify-owner",
		"cache-lookup",
		"generate",
		"guard-reply",
		"screen-reply",
		"after-hours-preamble",
		"persist-and-deliver",
		"post-delivery",
	}

	stages := (&Pipeline{}).inboundStages()
	got := make([]string, 0, len(stages))
	seen := map[string]bool{}
	for _, st := range stages {
		if st.Name == "" {
			t.Fatal("a stage has no name")
		}
		if st.Run == nil {
			t.Fatalf("stage %q has no function", st.Name)
		}
		if seen[st.Name] {
			t.Fatalf("duplicate stage name %q", st.Name)
		}
		seen[st.Name] = true
		got = append(got, st.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stage order changed:\n got %v\nwant %v", got, want)
	}
}

// Every stage must tolerate being handed a turn that earlier stages already
// rejected: the driver stops, but a future reordering must not turn a
// half-populated turn into a panic. This asserts the contract at the type level —
// stages take *inboundTurn and return (bool, error), never a partially applied
// pipeline.
func TestInboundStagesHaveTheDriverContract(t *testing.T) {
	var stages []inboundStage = (&Pipeline{}).inboundStages()
	if len(stages) < 10 {
		t.Fatalf("only %d stages; the pipeline looks truncated", len(stages))
	}
	// A turn with no config at all must not panic the driver when a stage is a
	// no-op for it (the first stage owns loading the config).
	noop := []inboundStage{{Name: "noop", Run: func(context.Context, *inboundTurn) (bool, error) { return true, nil }}}
	if err := runInboundStages(context.Background(), noop, &inboundTurn{}); err != nil {
		t.Fatalf("err = %v", err)
	}
}
