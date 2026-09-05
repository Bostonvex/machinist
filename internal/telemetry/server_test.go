package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := openTestStore(t)
	server, err := NewServer(store, testToken, 0, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server, store
}

func post(t *testing.T, server *Server, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, IngestPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testToken)
	if edit != nil {
		edit(request)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func batch(t *testing.T, events ...Event) string {
	t.Helper()
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("encode batch: %v", err)
	}
	return string(encoded)
}

func TestAValidBatchIsAcceptedAndStored(t *testing.T) {
	server, store := newTestServer(t)
	response := post(t, server, batch(t, event(t, "e1", EventTurnStarted, nil)), nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	var body map[string]int
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["accepted"] != 1 || body["inserted"] != 1 {
		t.Errorf("accepted %d, inserted %d", body["accepted"], body["inserted"])
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 1 {
		t.Errorf("the store holds %d events", count)
	}
}

// A resend is reported honestly: everything was accepted, nothing was new.
// Reporting them as inserted would tell a producer its retry created
// duplicates; reporting them as rejected would make it retry forever.
func TestAResendIsAcceptedButNotCountedAsNew(t *testing.T) {
	server, _ := newTestServer(t)
	document := batch(t, event(t, "e1", EventTurnStarted, nil))
	post(t, server, document, nil)
	response := post(t, server, document, nil)

	var body map[string]int
	json.Unmarshal(response.Body.Bytes(), &body)
	if response.Code != http.StatusAccepted || body["accepted"] != 1 || body["inserted"] != 0 {
		t.Errorf("status %d, accepted %d, inserted %d", response.Code, body["accepted"], body["inserted"])
	}
}

func TestAnUnauthenticatedBatchIsRefused(t *testing.T) {
	server, store := newTestServer(t)
	document := batch(t, event(t, "e1", EventTurnStarted, nil))
	for name, edit := range map[string]func(*http.Request){
		"no header":    func(r *http.Request) { r.Header.Del("Authorization") },
		"wrong token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32)) },
		"no scheme":    func(r *http.Request) { r.Header.Set("Authorization", testToken) },
		"token prefix": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken[:16]) },
	} {
		response := post(t, server, document, edit)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, response.Code)
		}
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 0 {
		t.Errorf("%d events were stored without a valid token", count)
	}
}

// The refusal says the credential was wrong and nothing else. Saying whether
// the header was missing, malformed, or simply not this token tells a caller
// which to fix, and the only caller needing that is one without the token.
func TestARefusalDoesNotSayWhichPartWasWrong(t *testing.T) {
	server, _ := newTestServer(t)
	document := batch(t, event(t, "e1", EventTurnStarted, nil))
	missing := post(t, server, document, func(r *http.Request) { r.Header.Del("Authorization") }).Body.String()
	wrong := post(t, server, document, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	}).Body.String()
	if missing != wrong {
		t.Errorf("a missing credential and a wrong one are distinguishable:\n  %s\n  %s", missing, wrong)
	}
}

// The producer is told the field to fix, and never the value that was
// rejected — a batch refused for holding something it should not have must not
// put that thing into an HTTP response as well.
func TestARejectedEventNamesTheFieldAndNotTheValue(t *testing.T) {
	server, _ := newTestServer(t)
	document := `[{"schema_version":1,"event_id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301",
      "event_type":"turn.completed","observed_at":"2026-09-05T12:00:00.000Z","monotonic_offset_ms":1,
      "producer":{"name":"machinist","version":"ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","instance_id":"m"},
      "agent":{"id":"a","display_name":"A"},"harness":null,"model":null,"endpoint_id":null,
      "session_id":null,"turn_id":null,"span_id":null,"parent_span_id":null,"attributes":{}}]`
	response := post(t, server, document, nil)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	var body failure
	json.Unmarshal(response.Body.Bytes(), &body)
	if body.Error == "" || body.Path == "" {
		t.Errorf("the producer was not told what to fix: %+v", body)
	}
	if strings.Contains(response.Body.String(), "ghp_") {
		t.Errorf("the rejected value was echoed back: %s", response.Body)
	}
}

func TestABodyLargerThanTheCapIsRefused(t *testing.T) {
	server, store := newTestServer(t)
	response := post(t, server, `[`+strings.Repeat(" ", MaximumBody+1)+`]`, nil)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d: %s", response.Code, response.Body)
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 0 {
		t.Error("an oversized body reached the store")
	}
}

func TestAMalformedRequestIsRefusedBeforeTheStore(t *testing.T) {
	server, store := newTestServer(t)
	cases := map[string]struct {
		body   string
		edit   func(*http.Request)
		status int
	}{
		"not json": {`{"events":[`, nil, http.StatusBadRequest},
		"empty":    {``, nil, http.StatusBadRequest},
		"wrong type": {`[]`, func(r *http.Request) {
			r.Header.Set("Content-Type", "text/plain")
		}, http.StatusUnsupportedMediaType},
	}
	for name, testCase := range cases {
		response := post(t, server, testCase.body, testCase.edit)
		if response.Code != testCase.status {
			t.Errorf("%s: status %d, want %d", name, response.Code, testCase.status)
		}
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 0 {
		t.Error("a malformed request reached the store")
	}
}

// A batch over the cap is refused whole rather than truncated. Storing the
// first hundred of a larger batch and reporting success would leave the
// producer believing events landed that did not.
func TestAnOversizedBatchIsRefusedWholeRatherThanTruncated(t *testing.T) {
	server, store := newTestServer(t)
	events := make([]Event, 0, DefaultMaximumBatch+1)
	for i := range DefaultMaximumBatch + 1 {
		events = append(events, event(t, fmt.Sprintf("e%d", i), EventTurnStarted, nil))
	}
	response := post(t, server, batch(t, events...), nil)
	if response.Code != http.StatusUnprocessableEntity {
		t.Errorf("status %d: %s", response.Code, response.Body)
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 0 {
		t.Errorf("%d events of a refused batch were stored", count)
	}
}

// Telemetry is metadata, but it is still a live description of what every agent
// on this machine is doing. Reaching it from another host should be a
// deliberate act, not a configuration slip.
func TestTheCollectorListensOnlyOnLoopback(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "192.168.1.10:0", ":0"} {
		listener, err := Listen(address)
		if err == nil {
			listener.Close()
			t.Errorf("the collector bound %s", address)
		}
	}
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback was refused: %v", err)
	}
	listener.Close()
}

// An ingest endpoint with no credential would let anything that can open a
// loopback socket write into the record other tools are read from.
func TestAServerWithoutACredentialIsRefused(t *testing.T) {
	store := openTestStore(t)
	for _, token := range []string{"", "short", strings.Repeat("x", 31)} {
		if _, err := NewServer(store, token, 0, nil); err == nil {
			t.Errorf("a server was built with token %q", token)
		}
	}
	if _, err := NewServer(nil, testToken, 0, nil); err == nil {
		t.Error("a server was built without a store")
	}
}

func TestExpiredEventsArePurgedAndDerivedStateIsKept(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "old", EventTurnStarted, map[string]any{
		"observed_at": "2026-08-01T12:00:00.000Z",
	}))
	insert(t, store, event(t, "new", EventTurnStarted, map[string]any{
		"observed_at": "2026-09-05T12:00:00.000Z",
	}))
	cutoff, _ := time.Parse(time.RFC3339, "2026-09-01T00:00:00Z")
	removed, err := store.Purge(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if removed != 1 {
		t.Errorf("purged %d events, want 1", removed)
	}
	if count := scalar[int](t, store, `SELECT count(*) FROM events`); count != 1 {
		t.Errorf("%d events remain", count)
	}
	// agents and turns are small and are what a reader asks about. Purging raw
	// events must not take the answers with them.
	if count := scalar[int](t, store, `SELECT count(*) FROM agents`); count != 1 {
		t.Errorf("purging raw events removed %d agents", 1-count)
	}
}

func TestHealthReportsWhatTheDatabaseHolds(t *testing.T) {
	server, store := newTestServer(t)
	insert(t, store, event(t, "e1", EventTurnStarted, nil))

	request := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	var body map[string]any
	json.Unmarshal(recorder.Body.Bytes(), &body)
	if body["status"] != "ok" || body["events"] != float64(1) || body["agents"] != float64(1) {
		t.Errorf("health reports %v", body)
	}
}
