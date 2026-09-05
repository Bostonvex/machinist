package telemetry

import (
	"net/http"
	"testing"
)

func TestAnAgentSummaryAnswersForOneAgent(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "a", "agent-a", "turn-a", nowish(), nil)
	turnAt(t, store, "b", "agent-b", "turn-b", nowish(), nil)

	decoded := body(t, get(t, server, AgentsPath+"/agent-a/summary"))
	agent := decoded["agent"].(map[string]any)
	if agent["id"] != "agent-a" {
		t.Fatalf("agent = %v", agent["id"])
	}
	turns := decoded["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["id"] != "turn-a" {
		t.Fatalf("turns = %v, want only agent-a's", turns)
	}
}

func TestAnAgentSummaryCannotBeAimedAtAnotherAgent(t *testing.T) {
	// A request for agent A carrying ?agent=B is a contradiction. Answering it
	// with B's turns under A's name is the one wrong answer nothing on the
	// page would reveal.
	server, store := newTestServer(t)
	turnAt(t, store, "a", "agent-a", "turn-a", nowish(), nil)
	turnAt(t, store, "b", "agent-b", "turn-b", nowish(), nil)

	decoded := body(t, get(t, server, AgentsPath+"/agent-a/summary?agent=agent-b"))
	if decoded["agent"].(map[string]any)["id"] != "agent-a" {
		t.Fatalf("the path agent was overridden by the query")
	}
	turns := decoded["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["id"] != "turn-a" {
		t.Fatalf("turns = %v, want only the agent named in the path", turns)
	}
}

func TestAnAgentWithNoTurnsInTheWindowIsIdleAndNotAbsent(t *testing.T) {
	// Narrowing the window must not make an agent disappear. A 404 there says
	// the agent is gone, and it is not — it just did nothing this hour.
	server, store := newTestServer(t)
	turnAt(t, store, "old", "agent-a", "turn-old", "2026-09-05T00:00:00.000Z", nil)

	recorder := get(t, server, AgentsPath+"/agent-a/summary?since=2026-09-05T23:00:00Z&until=2026-09-06T00:00:00Z")
	if recorder.Code != http.StatusOK {
		t.Fatalf("an idle agent answered %d", recorder.Code)
	}
	decoded := body(t, recorder)
	if decoded["agent"].(map[string]any)["id"] != "agent-a" {
		t.Fatalf("agent = %v", decoded["agent"])
	}
	if turns := decoded["turns"].([]any); len(turns) != 0 {
		t.Fatalf("turns = %v, want none in this window", turns)
	}
}

func TestAnAgentThatNeverExistedIsNotFound(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "a", "agent-a", "turn-a", nowish(), nil)

	if recorder := get(t, server, AgentsPath+"/agent-zzz/summary"); recorder.Code != http.StatusNotFound {
		t.Fatalf("an unknown agent answered %d", recorder.Code)
	}
}

func TestAnUnreadableWindowIsRefusedByTheAgentSummary(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "a", "agent-a", "turn-a", nowish(), nil)

	if recorder := get(t, server, AgentsPath+"/agent-a/summary?since=yesterday"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable date answered %d", recorder.Code)
	}
}

func TestTheAgentSummaryAggregatesTheTurnsItReturns(t *testing.T) {
	// The numbers and the list below them have to be the same turns, or the
	// reader is checking one claim against another.
	server, store := newTestServer(t)
	turnAt(t, store, "a", "agent-a", "turn-a", nowish(), nil)
	turnAt(t, store, "b", "agent-a", "turn-b", nowish(), nil)

	decoded := body(t, get(t, server, AgentsPath+"/agent-a/summary"))
	turns := decoded["turns"].([]any)
	aggregate := decoded["aggregate"].(map[string]any)
	if got := aggregate["turn_count"]; got != float64(len(turns)) {
		t.Fatalf("turn_count = %v, turns = %d", got, len(turns))
	}
	if decoded["limited"] != false {
		t.Fatalf("two turns were reported as a limited answer")
	}
}
