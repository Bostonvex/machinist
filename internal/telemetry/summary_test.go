package telemetry

import (
	"context"
	"testing"
)

func summaryOf(t *testing.T, store *Store, filter Filter) FleetSummary {
	t.Helper()
	summary, err := store.Summary(context.Background(), filter)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	return summary
}

func TestASummaryDescribesOnlyTheWindowItWasAskedFor(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "early", "agent-a", "turn-early", "2026-09-05T10:00:00.000Z", nil)
	turnAt(t, store, "late", "agent-a", "turn-late", "2026-09-05T14:00:00.000Z", nil)

	summary := summaryOf(t, store, Filter{Since: "2026-09-05T13:00:00Z"})
	if summary.Fleet.TurnCount != 1 {
		t.Fatalf("turn count = %d, want 1", summary.Fleet.TurnCount)
	}
	if summary.Limited {
		t.Fatal("a window of one turn was reported as truncated")
	}
}

func TestTheGroupsInASummaryAddUpToTheFleet(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "one", "agent-a", "turn-1", "2026-09-05T12:00:00.000Z", nil)
	turnAt(t, store, "two", "agent-b", "turn-2", "2026-09-05T12:00:01.000Z", nil)

	summary := summaryOf(t, store, Filter{})
	for _, dimension := range dimensionsOfATurn {
		groups, present := summary.Groups[dimension.key]
		if !present {
			t.Fatalf("dimension %q is grouped by but absent from the summary", dimension.key)
		}
		var total int
		for _, group := range groups {
			total += group.TurnCount
		}
		if total != summary.Fleet.TurnCount {
			t.Fatalf("dimension %q holds %d turns, fleet holds %d",
				dimension.key, total, summary.Fleet.TurnCount)
		}
	}
}

func TestSelectingAHarnessDoesNotHideTheHardware(t *testing.T) {
	// Infrastructure is shared. Narrowing it by an agent dimension would answer
	// a question the samples cannot support, and would show an operator an idle
	// machine whenever they picked a harness.
	store := openTestStore(t)
	sampleAt(t, store, "gpu", "2026-09-05T12:00:00.000Z", "gpu_utilization", 77,
		map[string]any{"unit": "percent"})
	turnAt(t, store, "one", "agent-a", "turn-1", "2026-09-05T12:00:00.000Z",
		map[string]any{"harness": "codex"})

	summary := summaryOf(t, store, Filter{Harness: "codex"})
	if summary.Fleet.InfrastructureMetrics.SampleCount != 1 {
		t.Fatalf("samples = %d, want 1: shared hardware must survive an agent filter",
			summary.Fleet.InfrastructureMetrics.SampleCount)
	}
}

func TestAnAgentHoldingATurnIsCountedAsActive(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "open", EventTurnStarted, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z", "turn_id": "turn-open",
		"agent": map[string]any{"id": "agent-a", "display_name": "agent-a"},
	}))
	summary := summaryOf(t, store, Filter{})
	if summary.Fleet.ActiveAgents != 1 {
		t.Fatalf("active agents = %d, want 1", summary.Fleet.ActiveAgents)
	}
	if summary.Fleet.ActiveTurns != 1 {
		t.Fatalf("active turns = %d, want 1", summary.Fleet.ActiveTurns)
	}
}

func TestASummaryReadsTheModelCallsInsideItsWindow(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "call", EventModelCompleted, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z", "turn_id": "turn-1",
		"attributes": map[string]any{
			"duration_ms": 1000.0, "decode_ms": 1000.0, "output_tokens": 500,
			"input_tokens": 100, "correlation": "exact", "measurement_quality": "exact",
		},
	}))
	summary := summaryOf(t, store, Filter{})
	metrics := summary.Fleet.ModelMetrics
	if metrics.ExactCallCount != 1 {
		t.Fatalf("exact calls = %d, want 1", metrics.ExactCallCount)
	}
	if metrics.OutputTokensPerSecond == nil || *metrics.OutputTokensPerSecond != 500 {
		t.Fatalf("throughput = %v, want 500", metrics.OutputTokensPerSecond)
	}
	if metrics.Limited {
		t.Fatal("one call was reported as a truncated read")
	}
}

func TestAnUnreadableFilterFailsTheSummaryRatherThanWideningIt(t *testing.T) {
	if _, err := openTestStore(t).Summary(context.Background(), Filter{Until: "soon"}); err == nil {
		t.Fatal("an unreadable window was silently ignored")
	}
}

func TestATurnDetailPlacesItsEventsRelativeToItsStart(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "one", "agent-a", "turn-1", "2026-09-05T12:00:00.000Z", nil)
	insert(t, store, event(t, "tool", EventToolStarted, map[string]any{
		"observed_at": "2026-09-05T12:00:02.500Z", "turn_id": "turn-1",
		"agent":      map[string]any{"id": "agent-a", "display_name": "agent-a"},
		"attributes": map[string]any{"tool_kind": "read", "status": "started"},
	}))

	detail, found, err := store.TurnDetail(context.Background(), "turn-1")
	if err != nil || !found {
		t.Fatalf("turn detail: found=%v err=%v", found, err)
	}
	var seen bool
	for _, item := range detail.Timeline {
		if item.EventType != string(EventToolStarted) {
			continue
		}
		seen = true
		if item.RelativeMS != 2500 {
			t.Fatalf("relative offset = %v, want 2500", item.RelativeMS)
		}
		if item.Scope != "turn" {
			t.Fatalf("a turn's own event was scoped %q", item.Scope)
		}
	}
	if !seen {
		t.Fatal("the tool event is missing from the timeline")
	}
}

func TestSharedReadingsAreOnTheirOwnSideOfATurnTimeline(t *testing.T) {
	// A GPU that was warm during a turn was warm for the whole host. Mixing the
	// reading into the turn's own timeline would read as the turn causing it.
	store := openTestStore(t)
	turnAt(t, store, "one", "agent-a", "turn-1", "2026-09-05T12:00:00.000Z", nil)
	sampleAt(t, store, "gpu", "2026-09-05T12:00:00.000Z", "gpu_utilization", 88,
		map[string]any{"unit": "percent"})

	detail, found, err := store.TurnDetail(context.Background(), "turn-1")
	if err != nil || !found {
		t.Fatalf("turn detail: found=%v err=%v", found, err)
	}
	for _, item := range detail.Timeline {
		if item.EventType == string(EventHardwareSample) {
			t.Fatal("a shared reading was placed in the turn's own timeline")
		}
	}
	if len(detail.SharedContext) != 1 {
		t.Fatalf("shared context holds %d readings, want 1", len(detail.SharedContext))
	}
	if detail.SharedContext[0].Scope != "shared_context" {
		t.Fatalf("a shared reading was scoped %q", detail.SharedContext[0].Scope)
	}
}

func TestATurnThatDoesNotExistIsNotAnError(t *testing.T) {
	_, found, err := openTestStore(t).TurnDetail(context.Background(), "turn-missing")
	if err != nil {
		t.Fatalf("a missing turn was an error: %v", err)
	}
	if found {
		t.Fatal("a turn that was never recorded was found")
	}
}

func TestATurnIdentifierNothingCouldHaveWrittenIsRefused(t *testing.T) {
	if _, _, err := openTestStore(t).TurnDetail(context.Background(), ""); err == nil {
		t.Fatal("an empty turn id was looked up rather than refused")
	}
}

func TestATruncatedReadSaysSoAndACompleteOneDoesNot(t *testing.T) {
	// A number computed from part of a window is a different claim from one
	// computed from all of it, and a reader has to be able to tell.
	store := openTestStore(t)
	for _, name := range []string{"a", "b"} {
		insert(t, store, event(t, "call-"+name, EventModelCompleted, map[string]any{
			"observed_at": "2026-09-05T12:00:0" + map[string]string{"a": "1", "b": "2"}[name] + ".000Z", "turn_id": "turn-" + name,
			"attributes": map[string]any{"duration_ms": 1.0, "correlation": "exact"},
		}))
	}
	if _, limited, err := store.modelEventsIn(context.Background(), Filter{}, 1); err != nil || !limited {
		t.Fatalf("reading 1 of 2 events reported limited=%v err=%v", limited, err)
	}
	if _, limited, err := store.modelEventsIn(context.Background(), Filter{}, 2); err != nil || limited {
		t.Fatalf("reading 2 of 2 events reported limited=%v err=%v", limited, err)
	}
}

func TestModelEventsAreWalkedInTheOrderTheyHappened(t *testing.T) {
	// A counter of calls and a sweep over decode intervals both assume time
	// runs forwards, but the window is bounded by reading the newest first.
	store := openTestStore(t)
	for _, at := range []string{"01", "02", "03"} {
		insert(t, store, event(t, "call-"+at, EventModelCompleted, map[string]any{
			"observed_at": "2026-09-05T12:00:" + at + ".000Z", "turn_id": "turn-" + at,
			"attributes": map[string]any{"duration_ms": 1.0, "correlation": "exact"},
		}))
	}
	events, _, err := store.modelEventsIn(context.Background(), Filter{}, 10)
	if err != nil {
		t.Fatalf("read model events: %v", err)
	}
	for index := 1; index < len(events); index++ {
		if events[index-1].ObservedAt > events[index].ObservedAt {
			t.Fatalf("events came back newest first: %s before %s",
				events[index-1].ObservedAt, events[index].ObservedAt)
		}
	}
}
