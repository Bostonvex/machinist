package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestASubscriberSeesWhatWasStored(t *testing.T) {
	server, _ := newTestServer(t)
	stream := server.broker.subscribe()
	defer server.broker.unsubscribe(stream)

	stored := event(t, "one", EventTurnStarted, map[string]any{"turn_id": "turn-1"})
	if recorder := post(t, server, batch(t, stored), nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("ingest answered %d", recorder.Code)
	}
	select {
	case live := <-stream:
		if live.EventID != stored.EventID || live.EventType != string(EventTurnStarted) {
			t.Fatalf("live event does not describe what was stored: %+v", live)
		}
		if live.AgentID != stored.Agent.ID {
			t.Fatalf("live agent = %q, want %q", live.AgentID, stored.Agent.ID)
		}
	default:
		t.Fatal("nothing was published for a stored event")
	}
}

func TestALiveEventCarriesNoAttributes(t *testing.T) {
	// Attributes hold whatever a producer put in them. The stream is read by a
	// page in a browser, and forwarding the payload would put unvalidated
	// producer text on a channel nothing checked it for.
	server, _ := newTestServer(t)
	stream := server.broker.subscribe()
	defer server.broker.unsubscribe(stream)
	post(t, server, batch(t, event(t, "one", EventTurnStarted, map[string]any{
		"turn_id":    "turn-1",
		"attributes": map[string]any{"turn_class": "interactive"},
	})), nil)

	live := <-stream
	encoded := mustEncode(t, live)
	if strings.Contains(encoded, "turn_class") || strings.Contains(encoded, "attributes") {
		t.Fatalf("a live event carried producer attributes: %s", encoded)
	}
}

func TestAStalledReaderLosesItsOldestEventsAndNotTheCollector(t *testing.T) {
	// A browser tab on a laptop that went to sleep must not be able to slow
	// down the collector every agent on the machine is writing to.
	subject := newBroker()
	stream := subject.subscribe()
	defer subject.unsubscribe(stream)

	events := make([]Event, 0, subscriberDepth+10)
	for index := 0; index < cap(events); index++ {
		events = append(events, Event{EventID: string(rune('a'+index%26)) + itoa(index),
			EventType: EventTurnStarted, Agent: Agent{ID: "agent-a"}})
	}
	done := make(chan struct{})
	go func() { subject.publish(events); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on a subscriber that was not reading")
	}
	if length := len(stream); length != subscriberDepth {
		t.Fatalf("subscriber holds %d events, want its full depth of %d", length, subscriberDepth)
	}
	// The newest state is what a live view is for, so the end survived.
	last := events[len(events)-1]
	var newest LiveEvent
	for len(stream) > 0 {
		newest = <-stream
	}
	if newest.EventID != last.EventID {
		t.Fatalf("the stalled reader lost the newest event, keeping %q", newest.EventID)
	}
}

func TestUnsubscribingTwiceIsNotAPanic(t *testing.T) {
	subject := newBroker()
	stream := subject.subscribe()
	subject.unsubscribe(stream)
	subject.unsubscribe(stream)
	if subject.subscriberCount() != 0 {
		t.Fatal("a removed subscriber is still registered")
	}
}

func TestTheLiveStreamAnnouncesItselfAndLetsGoWhenTheReaderDoes(t *testing.T) {
	server, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, LivePath, nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	finished := make(chan struct{})
	go func() { server.Handler().ServeHTTP(recorder, request); close(finished) }()

	waitUntil(t, func() bool { return server.broker.subscriberCount() == 1 })
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not end when the reader went away")
	}
	if got := recorder.Body.String(); !strings.HasPrefix(got, "event: ready\n") {
		t.Fatalf("the stream did not announce itself: %q", got)
	}
	if recorder.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	waitUntil(t, func() bool { return server.broker.subscriberCount() == 0 })
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was never met")
}
