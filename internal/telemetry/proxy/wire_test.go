package proxy

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// The proxy's events have one job, which is to be accepted by the collector
// they are sent to. These tests run what the proxy actually emitted through
// the collector's own ingest validation rather than through a restatement of
// it, so a rule that changes on one side cannot pass on the other.
//
// The collector refuses an event with a missing field, an unknown field, an
// identifier it cannot parse or an attribute it does not know. Every one of
// those is silent from the proxy's side: the event is enqueued, delivered, and
// dropped at ingest, and the call it measured is over by the time anyone could
// notice.

func accepted(t *testing.T, event Event) telemetry.Event {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	validated, err := telemetry.ValidateEvent(decoded)
	if err != nil {
		t.Fatalf("the collector refused a %s the proxy emitted: %v\n%s",
			event.EventType, err, encoded)
	}
	return validated
}

func TestEveryEventAForwardedCallEmitsIsAcceptedByTheCollector(t *testing.T) {
	front, sink := declaring(t, sseUpstream(
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"message_delta","usage":{"input_tokens":11,"output_tokens":4,`+
			`"cache_read_input_tokens":3,"output_tokens_details":{"reasoning_tokens":2}}}`,
	))
	if status, _ := answered(t, front, testToken, `{"action":"start","context":{
		"context_id":"ctx-a","agent_id":"reviewer","display_name":"Reviewer",
		"harness":"codex","model":"ds-0731","endpoint_id":"dgx-primary",
		"session_id":"session-1","turn_id":"turn-1"}}`); status != http.StatusOK {
		t.Fatalf("declare: status = %d", status)
	}
	drain(t, front, "/v1/messages")
	sink.settled(t)

	seen := map[string]bool{}
	sink.mutex.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mutex.Unlock()
	for _, event := range events {
		validated := accepted(t, event)
		seen[string(validated.EventType)] = true
	}
	for _, wanted := range []string{ModelRequestStarted, ModelFirstToken, ModelCompleted} {
		if !seen[wanted] {
			t.Fatalf("no %s was emitted; saw %v", wanted, sink.types())
		}
	}
}

func TestAFailedCallsEventsAreAcceptedByTheCollector(t *testing.T) {
	// The failure path builds a different attribute set, so being accepted on
	// the success path says nothing about it.
	front, sink := declaring(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(`{"error":"upstream is unwell"}`))
	}))
	drain(t, front, "/v1/chat/completions")

	if failed := sink.settled(t); failed.EventType != ModelFailed {
		t.Fatalf("event = %s, want a failure", failed.EventType)
	}
	sink.mutex.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mutex.Unlock()
	for _, event := range events {
		accepted(t, event)
	}
}

func TestAnAmbiguousCallIsStillAcceptedByTheCollector(t *testing.T) {
	// Ambiguity leaves session_id and turn_id null, which is the one shape the
	// nullable-identifier rules exist for.
	front, sink := declaring(t, http.HandlerFunc(ok))
	for _, id := range []string{"ctx-a", "ctx-b"} {
		body := `{"action":"start","context":{"context_id":"` + id + `","agent_id":"reviewer",
			"display_name":"Reviewer","session_id":"s1","turn_id":"t-` + id + `"}}`
		if status, _ := answered(t, front, testToken, body); status != http.StatusOK {
			t.Fatalf("declare %s: status = %d", id, status)
		}
	}
	drain(t, front, "/v1/chat/completions")

	completed := sink.settled(t)
	if completed.Attributes["correlation"] != CorrelationAmbiguous {
		t.Fatalf("correlation = %v, want ambiguous", completed.Attributes["correlation"])
	}
	validated := accepted(t, completed)
	if validated.SessionID != nil || validated.TurnID != nil {
		t.Fatal("an ambiguous call named a turn")
	}
}

func TestTheProxysEventIDIsTheIdentifierTheCollectorExpects(t *testing.T) {
	// A 32-hex identifier reads as an id and is refused as one. The failure is
	// invisible from here, so it is asserted here.
	first, second := newIdentifier(), newIdentifier()
	if first == second {
		t.Fatal("two identifiers were the same")
	}
	for _, identifier := range []string{first, second} {
		if len(identifier) != 36 {
			t.Fatalf("identifier %q is not a UUID", identifier)
		}
	}
}
