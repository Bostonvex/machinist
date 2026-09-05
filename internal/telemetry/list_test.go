package telemetry

import (
	"context"
	"testing"
)

// turnAt inserts a completed turn for one agent at a given time.
func turnAt(t *testing.T, store *Store, name, agentID, turnID, at string, edits map[string]any) {
	t.Helper()
	started := map[string]any{
		"observed_at": at, "turn_id": turnID,
		"agent": map[string]any{"id": agentID, "display_name": agentID},
	}
	for key, item := range edits {
		started[key] = item
	}
	insert(t, store, event(t, name+"-started", EventTurnStarted, started))

	completed := map[string]any{}
	for key, item := range started {
		completed[key] = item
	}
	completed["observed_at"] = at
	completed["attributes"] = map[string]any{
		"duration_ms": 1000.0, "tool_count": 1, "outcome": "succeeded", "measurement_quality": "exact",
	}
	insert(t, store, event(t, name+"-completed", EventTurnCompleted, completed))
}

func TestAListingReturnsOnlyTheWindowItWasAskedFor(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "early", "agent-a", "turn-early", "2026-09-05T10:00:00.000Z", nil)
	turnAt(t, store, "late", "agent-a", "turn-late", "2026-09-05T14:00:00.000Z", nil)

	turns, err := store.ListTurns(context.Background(),
		Filter{Since: "2026-09-05T12:00:00Z"}, 100, 0)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != "turn-late" {
		t.Fatalf("the window returned %d turns: %+v", len(turns), turns)
	}
}

func TestATurnStartingExactlyOnTheBoundaryIsInTheWindow(t *testing.T) {
	// The boundary second is the one a caller most often means, and losing it
	// to a text comparison is invisible: the count is simply lower.
	store := openTestStore(t)
	turnAt(t, store, "boundary", "agent-a", "turn-boundary", "2026-09-05T12:00:00.000Z", nil)

	turns, err := store.ListTurns(context.Background(), Filter{Since: "2026-09-05T12:00:00Z"}, 100, 0)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("the turn on the boundary was dropped from its own window")
	}
}

func TestATurnCarriesTheNameOfTheAgentThatRanIt(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "named", "agent-a", "turn-named", "2026-09-05T12:00:00.000Z", nil)

	turns, err := store.ListTurns(context.Background(), Filter{}, 100, 0)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if turns[0].AgentDisplayName == "" {
		t.Fatal("a turn came back without the agent's display name")
	}
	if turns[0].Outcome == nil || *turns[0].Outcome != "completed" {
		t.Fatalf("outcome was %v", turns[0].Outcome)
	}
}

func TestPagingDoesNotShowATurnTwiceOrSkipOne(t *testing.T) {
	// Two turns started in the same millisecond come back in whatever order
	// SQLite chose unless the ordering breaks ties, and a caller paging with an
	// offset then sees one twice and another not at all.
	store := openTestStore(t)
	for _, id := range []string{"turn-1", "turn-2", "turn-3", "turn-4"} {
		turnAt(t, store, id, "agent-a", id, "2026-09-05T12:00:00.000Z", nil)
	}
	seen := map[string]bool{}
	for offset := 0; offset < 4; offset += 2 {
		page, err := store.ListTurns(context.Background(), Filter{}, 2, offset)
		if err != nil {
			t.Fatalf("list turns: %v", err)
		}
		for _, turn := range page {
			if seen[turn.ID] {
				t.Fatalf("turn %s appeared on two pages", turn.ID)
			}
			seen[turn.ID] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("paging returned %d of 4 turns", len(seen))
	}
}

func TestAFilterOnADimensionNarrowsTheListing(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "codex", "agent-a", "turn-codex", "2026-09-05T12:00:00.000Z",
		map[string]any{"harness": "codex"})
	turnAt(t, store, "aider", "agent-b", "turn-aider", "2026-09-05T12:00:00.000Z",
		map[string]any{"harness": "aider"})

	turns, err := store.ListTurns(context.Background(), Filter{Harness: "codex"}, 100, 0)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != "turn-codex" {
		t.Fatalf("the harness filter returned %+v", turns)
	}
}

func TestAnAgentStillRunningFromBeforeTheWindowIsStillListed(t *testing.T) {
	// An agent selected by when it was first seen vanishes from every window
	// after the one it started in, so a long-lived worker disappears from the
	// dashboard the longer it runs.
	store := openTestStore(t)
	turnAt(t, store, "old", "agent-a", "turn-old", "2026-09-05T08:00:00.000Z", nil)
	turnAt(t, store, "new", "agent-a", "turn-new", "2026-09-05T14:00:00.000Z", nil)

	agents, err := store.ListAgents(context.Background(),
		Filter{Since: "2026-09-05T12:00:00Z"}, 100)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("an agent that was running through the window was not listed: %+v", agents)
	}
	if agents[0].FirstSeenAt >= "2026-09-05T12:00:00.000Z" {
		t.Fatalf("first_seen_at was rewritten to fit the window: %s", agents[0].FirstSeenAt)
	}
}

func TestAnAgentFilteredByOutcomeMustHaveHadOne(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "done", "agent-a", "turn-done", "2026-09-05T12:00:00.000Z", nil)
	insert(t, store, event(t, "running", EventTurnStarted, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z", "turn_id": "turn-running",
		"agent": map[string]any{"id": "agent-b", "display_name": "agent-b"},
	}))

	agents, err := store.ListAgents(context.Background(), Filter{Outcome: "completed"}, 100)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "agent-a" {
		t.Fatalf("the outcome filter returned %+v", agents)
	}
}

func TestSharedSamplesAreNeverAttributedToAnAgent(t *testing.T) {
	// A GPU that was warm during a turn was warm for the whole host, and a
	// reader must not be able to mistake the reading for something the agent
	// beside it was responsible for.
	store := openTestStore(t)
	insert(t, store, event(t, "sample", EventHardwareSample, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z",
		"turn_id":     nil, "session_id": nil, "endpoint_id": nil,
		"agent": map[string]any{"id": "shared-infrastructure", "display_name": "Shared infrastructure"},
		"attributes": map[string]any{
			"metric_name": "gpu.0.utilization_percent", "value": 41.0,
			"unit": "percent", "measurement_quality": "exact",
			"provider_id": "nvidia-smi", "node_id": "dgx-spark",
		},
	}))
	samples, err := store.ListSamples(context.Background(), Filter{}, 100)
	if err != nil {
		t.Fatalf("list samples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(samples))
	}
	if samples[0].Scope != "shared_context" {
		t.Fatalf("a shared reading was scoped %q", samples[0].Scope)
	}
	if samples[0].NodeID == nil || *samples[0].NodeID != "dgx-spark" {
		t.Fatalf("the reading lost the node it came from: %+v", samples[0])
	}
}

func TestADimensionIsOfferedOnlyIfSomeTurnCarriesIt(t *testing.T) {
	// Offering a model nothing ran on lets a reader select it and conclude the
	// fleet was idle.
	store := openTestStore(t)
	turnAt(t, store, "one", "agent-a", "turn-one", "2026-09-05T12:00:00.000Z",
		map[string]any{"harness": "codex", "model": "ds-0731"})

	dimensions, err := store.Dimensions(context.Background())
	if err != nil {
		t.Fatalf("dimensions: %v", err)
	}
	if len(dimensions["models"]) != 1 || dimensions["models"][0] != "ds-0731" {
		t.Fatalf("models were %v", dimensions["models"])
	}
	if len(dimensions["outcomes"]) != 1 || dimensions["outcomes"][0] != "completed" {
		t.Fatalf("outcomes were %v", dimensions["outcomes"])
	}
	for _, key := range []string{"agents", "harnesses", "models", "endpoints", "outcomes"} {
		if _, present := dimensions[key]; !present {
			t.Fatalf("dimension %q was not reported at all", key)
		}
	}
}

func TestAnEmptyListingIsAnEmptyListAndNotNothing(t *testing.T) {
	// A nil slice encodes as JSON null, and a dashboard iterating over null
	// fails where one iterating over [] draws an empty chart.
	store := openTestStore(t)
	turns, err := store.ListTurns(context.Background(), Filter{}, 100, 0)
	if err != nil || turns == nil {
		t.Fatalf("an empty turn listing was %v (%v)", turns, err)
	}
	agents, err := store.ListAgents(context.Background(), Filter{}, 100)
	if err != nil || agents == nil {
		t.Fatalf("an empty agent listing was %v (%v)", agents, err)
	}
	samples, err := store.ListSamples(context.Background(), Filter{}, 100)
	if err != nil || samples == nil {
		t.Fatalf("an empty sample listing was %v (%v)", samples, err)
	}
}

func TestAnUnreadableFilterFailsTheQueryRatherThanWideningIt(t *testing.T) {
	store := openTestStore(t)
	for _, filter := range []Filter{{Since: "yesterday"}, {Outcome: "suceeded"}} {
		if _, err := store.ListTurns(context.Background(), filter, 100, 0); err == nil {
			t.Fatalf("filter %+v was silently ignored", filter)
		}
		if _, err := store.ListAgents(context.Background(), filter, 100); err == nil {
			t.Fatalf("filter %+v was silently ignored by the agent listing", filter)
		}
	}
}
