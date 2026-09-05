package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// event builds a valid event of the given type by editing the fixture, so a
// storage test still goes through the validator the ingest path uses.
func event(t *testing.T, id string, eventType EventType, edits map[string]any) Event {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(turnCompleted), &document); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	document["event_id"] = uuidFor(id)
	document["event_type"] = string(eventType)
	document["attributes"] = map[string]any{}
	for key, value := range edits {
		if attribute, ok := value.(map[string]any); ok && key == "attributes" {
			document["attributes"] = attribute
			continue
		}
		document[key] = value
	}
	validated, err := ValidateEvent(document)
	if err != nil {
		t.Fatalf("test event %s is not valid: %v", eventType, err)
	}
	return validated
}

// uuidFor turns a readable test name into the UUID the contract requires, so a
// failure names the event a reader recognises instead of a hex string.
func uuidFor(name string) string {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

func insert(t *testing.T, store *Store, events ...Event) int {
	t.Helper()
	inserted, err := store.Insert(context.Background(), events)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return inserted
}

func scalar[T any](t *testing.T, store *Store, query string, arguments ...any) T {
	t.Helper()
	var value T
	if err := store.read.QueryRow(query, arguments...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func TestAnEventIsStoredWithTheStateItImplies(t *testing.T) {
	store := openTestStore(t)
	if inserted := insert(t, store, event(t, "e1", EventTurnStarted, nil)); inserted != 1 {
		t.Fatalf("inserted %d events, want 1", inserted)
	}
	if state := scalar[string](t, store, `SELECT current_state FROM agents WHERE id='dgx-deepcode'`); state != "waiting_for_activity" {
		t.Errorf("agent state is %q after turn.started", state)
	}
	if turn := scalar[string](t, store, `SELECT current_turn_id FROM agents WHERE id='dgx-deepcode'`); turn != "turn-1" {
		t.Errorf("agent is holding turn %q", turn)
	}
	if started := scalar[string](t, store, `SELECT started_at FROM turns WHERE id='turn-1'`); started == "" {
		t.Error("the turn was not recorded")
	}
}

// A producer that cannot confirm its batch landed should resend it rather than
// drop telemetry. Resending must be free.
func TestResendingABatchChangesNothing(t *testing.T) {
	store := openTestStore(t)
	batch := []Event{
		event(t, "e1", EventTurnStarted, nil),
		event(t, "e2", EventTurnCompleted, map[string]any{
			"attributes": map[string]any{"duration_ms": 4200.5, "tool_count": 3.0},
		}),
	}
	if inserted := insert(t, store, batch...); inserted != 2 {
		t.Fatalf("first send inserted %d, want 2", inserted)
	}
	if inserted := insert(t, store, batch...); inserted != 0 {
		t.Fatalf("resend inserted %d events, want 0", inserted)
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 2 {
		t.Errorf("the database holds %d events after a resend, want 2", count)
	}
	if tools := scalar[int](t, store, `SELECT tool_count FROM turns WHERE id='turn-1'`); tools != 3 {
		t.Errorf("tool_count is %d after a resend, want 3 — the turn was derived twice", tools)
	}
}

// An agent whose backlog drains late must not appear to go backwards. This is
// the ordering rule that keeps a finished agent from flickering back to running.
func TestAnOlderEventDoesNotOverwriteANewerState(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "late", EventTurnCompleted, map[string]any{
		"observed_at": "2026-09-05T12:00:10.000Z",
		"attributes":  map[string]any{"duration_ms": 10000.0},
	}))
	insert(t, store, event(t, "early", EventTurnStarted, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z",
	}))
	if state := scalar[string](t, store, `SELECT current_state FROM agents WHERE id='dgx-deepcode'`); state != "completed" {
		t.Errorf("a late-arriving earlier event moved the agent to %q", state)
	}
}

// A turn that ends holds no agent. Otherwise a reader sees work in progress
// that nothing is doing.
func TestEndingATurnReleasesTheAgent(t *testing.T) {
	store := openTestStore(t)
	insert(t, store,
		event(t, "e1", EventTurnStarted, nil),
		event(t, "e2", EventTurnCompleted, map[string]any{
			"observed_at": "2026-09-05T12:00:10.000Z",
			"attributes":  map[string]any{"duration_ms": 10000.0},
		}))
	var turn *string
	if err := store.read.QueryRow(`SELECT current_turn_id FROM agents WHERE id='dgx-deepcode'`).Scan(&turn); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if turn != nil {
		t.Errorf("the agent is still holding turn %q after it completed", *turn)
	}
	if outcome := scalar[string](t, store, `SELECT outcome FROM turns WHERE id='turn-1'`); outcome != "completed" {
		t.Errorf("turn outcome is %q", outcome)
	}
}

// A harness that only reports "this just happened" and one that reports its own
// milestone timings should produce the same turn record. Which of the two a
// producer is should not change what a reader can ask.
func TestAMilestoneWithoutItsOwnNameIsStillTimed(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "e1", EventTurnFirstTool, map[string]any{
		"attributes": map[string]any{"elapsed_ms": 850.0},
	}))
	if ms := scalar[float64](t, store, `SELECT first_tool_ms FROM turns WHERE id='turn-1'`); ms != 850 {
		t.Errorf("first_tool_ms is %v, want 850 derived from elapsed_ms", ms)
	}
}

// The stall a turn is judged by is its worst one, not its most recent.
func TestATurnKeepsItsWorstStall(t *testing.T) {
	store := openTestStore(t)
	stall := func(id string, gap float64, at string) Event {
		return event(t, id, EventTurnStall, map[string]any{
			"observed_at": at,
			"attributes":  map[string]any{"gap_ms": gap, "threshold_ms": 500.0},
		})
	}
	insert(t, store,
		stall("s1", 9000, "2026-09-05T12:00:01.000Z"),
		stall("s2", 1200, "2026-09-05T12:00:02.000Z"))
	if ms := scalar[float64](t, store, `SELECT max_stall_ms FROM turns WHERE id='turn-1'`); ms != 9000 {
		t.Errorf("max_stall_ms is %v, want 9000 — a later smaller stall overwrote the worst one", ms)
	}
}

// Infrastructure describes a machine. Letting it touch the agent tables would
// report a host that is warm as an agent that is working.
func TestASampleDescribesAMachineAndNotAnAgent(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "s1", EventHardwareSample, map[string]any{
		"turn_id": nil,
		"attributes": map[string]any{
			"provider_id": "nvidia_smi", "node_id": "dgx-0",
			"metric_name": "gpu_utilization", "value": 91.0, "unit": "percent",
		},
	}))
	if count := scalar[int](t, store, `SELECT count(*) FROM agents`); count != 0 {
		t.Errorf("a hardware sample created %d agent rows", count)
	}
	if value := scalar[float64](t, store, `SELECT value FROM infrastructure_samples WHERE event_id=?`, uuidFor("s1")); value != 91 {
		t.Errorf("sample value is %v", value)
	}
	if node := scalar[string](t, store, `SELECT node_id FROM infrastructure_samples WHERE event_id=?`, uuidFor("s1")); node != "dgx-0" {
		t.Errorf("sample node is %q", node)
	}
}

// A model call the producer could not tie to a turn is still worth aggregating
// across a fleet, but attributing it would put one agent's latency on whichever
// turn happened to be open. A wrong attribution is worse than an absent one:
// it cannot be told apart from a real measurement.
func TestAnUnattributedModelCallIsKeptButNotAttributed(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "m1", EventModelCompleted, map[string]any{
		"attributes": map[string]any{"duration_ms": 300.0, "correlation": "ambiguous"},
	}))
	if count := scalar[int](t, store, `SELECT count(*) FROM events WHERE event_id=?`, uuidFor("m1")); count != 1 {
		t.Error("the model event was not kept")
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM turns`); count != 0 {
		t.Errorf("an unattributed model call was written onto %d turns", count)
	}
}

func TestAnExactlyAttributedModelCallIsAttributed(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "m1", EventModelCompleted, map[string]any{
		"attributes": map[string]any{"duration_ms": 300.0, "correlation": "exact"},
	}))
	if count := scalar[int](t, store, `SELECT count(*) FROM turns WHERE id='turn-1'`); count != 1 {
		t.Error("an exactly attributed model call did not reach its turn")
	}
}

// A database a later build wrote may hold columns this one does not know about.
// Opening it read-write would quietly degrade those rows.
func TestAFutureDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.write.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatalf("set version: %v", err)
	}
	store.Close()

	if _, err := OpenStore(path); err == nil {
		t.Fatal("a database written by a newer build was opened read-write")
	}
}

func TestReopeningAnExistingDatabaseKeepsItsEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	insert(t, store, event(t, "e1", EventTurnStarted, nil))
	store.Close()

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if count := scalar[int](t, reopened, `SELECT count(*) FROM events`); count != 1 {
		t.Errorf("the reopened database holds %d events, want 1", count)
	}
}
