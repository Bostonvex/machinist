package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/telemetry"
)

// nowForDemoTest is a fixed instant, so a failure here is about the events and
// never about when the test ran.
func nowForDemoTest() time.Time {
	return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
}

// encodeForTest and decodeForTest put an event through JSON, because that is
// what ingest validates: a Go value that never crossed the wire would skip the
// exact-fields check the contract is mostly made of.
func encodeForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded
}

func decodeForTest(t *testing.T, encoded []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(encoded, into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func readTokenForTest(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func TestDemoRefusesWithoutTheAcknowledgement(t *testing.T) {
	// These turns are indistinguishable from real ones once stored, and they
	// move the averages an operator reads. Writing them must be a deliberate
	// act, not the consequence of typing a verb.
	path := enabledCollectorAt(t, t.TempDir(), "")
	code, stdout, stderr := run(t, "collector", "demo", "--config", path)
	if code == 0 {
		t.Fatalf("demo wrote synthetic events unasked: %s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "--confirm-synthetic-events") {
		t.Fatalf("the refusal does not name the flag: %s", stderr)
	}
}

func TestDemoSaysTheCollectorIsNotRunning(t *testing.T) {
	// The most likely reason to be here, and the only one the operator can act
	// on. A generic dial error would send them to the wrong place.
	path := enabledCollectorAt(t, t.TempDir(), "")
	rewrite(t, path, `listen = "127.0.0.1:0"`, `listen = "127.0.0.1:1"`)
	code, stdout, stderr := run(t, "collector", "demo", "--config", path, "--confirm-synthetic-events")
	if code == 0 {
		t.Fatalf("demo reported success with nothing listening: %s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "machinist collector start") {
		t.Fatalf("the failure does not say how to fix it: %s", stderr)
	}
}

func TestDemoTurnsAreAcceptedByTheRealIngestContract(t *testing.T) {
	// The point of this verb is to answer "is any of this working". If the
	// events it invents could not pass validation, it would answer that
	// question wrongly in the direction that wastes the most time.
	for _, event := range syntheticTurns(nowForDemoTest()) {
		var submitted any
		encoded := encodeForTest(t, event)
		decodeForTest(t, encoded, &submitted)
		if _, err := telemetry.ValidateEvent(submitted); err != nil {
			t.Fatalf("a synthetic %s would be refused at ingest: %v", event.EventType, err)
		}
	}
}

func TestDemoSendsCompleteTurns(t *testing.T) {
	// A turn with no completion sits in the live view forever. An operator
	// running this to check the collector would be left with an artefact that
	// looks exactly like a hung agent.
	started := map[string]bool{}
	completed := map[string]bool{}
	for _, event := range syntheticTurns(nowForDemoTest()) {
		if event.TurnID == nil {
			t.Fatalf("a synthetic %s carries no turn", event.EventType)
		}
		switch event.EventType {
		case telemetry.EventTurnStarted:
			started[*event.TurnID] = true
		case telemetry.EventTurnCompleted:
			completed[*event.TurnID] = true
		}
	}
	if len(started) != 2 {
		t.Fatalf("started %d turns, want two so the dashboard has something to rank", len(started))
	}
	for turn := range started {
		if !completed[turn] {
			t.Fatalf("turn %s never completes", turn)
		}
	}
}

func TestDemoAgentsAreNameableAsSynthetic(t *testing.T) {
	// The wire contract has no field for "this is not real", so the name is
	// the only thing an operator can filter on afterwards.
	for _, event := range syntheticTurns(nowForDemoTest()) {
		if !strings.HasPrefix(event.Agent.ID, demoAgentPrefix) {
			t.Fatalf("agent %q is not identifiable as synthetic", event.Agent.ID)
		}
	}
}

func TestDemoDeliversToARunningCollector(t *testing.T) {
	path := enabledCollectorAt(t, t.TempDir(), "")
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	base := startedCollector(t, loaded.Collector)
	rewrite(t, path, `listen = "127.0.0.1:0"`, `listen = "`+strings.TrimPrefix(base, "http://")+`"`)

	code, stdout, stderr := run(t, "collector", "demo", "--config", path, "--confirm-synthetic-events")
	if code != 0 {
		t.Fatalf("demo failed against a running collector: %s%s", stdout, stderr)
	}
	if !strings.Contains(stdout, "accepted 8 synthetic events") || !strings.Contains(stdout, "stored 8") {
		t.Fatalf("demo does not report what the collector took: %s", stdout)
	}

	token, err := readTokenForTest(loaded.Collector.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if held := eventsHeld(t, base, token); held != 8 {
		t.Fatalf("the collector holds %d events, want the 8 the demo sent", held)
	}
}
