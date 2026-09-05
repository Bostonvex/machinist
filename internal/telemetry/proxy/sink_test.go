package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// collected is a stand-in collector that records what arrived.
type collected struct {
	mutex    sync.Mutex
	batches  [][]any
	tokens   []string
	statuses []int
	refuse   int
	delay    time.Duration
}

// hold makes every answer take this long, so that a submission can be observed
// while it is still in flight.
func (c *collected) hold(delay time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.delay = delay
}

func (c *collected) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	var batch []any
	_ = json.Unmarshal(body, &batch)

	c.mutex.Lock()
	c.batches = append(c.batches, batch)
	c.tokens = append(c.tokens, request.Header.Get("Authorization"))
	status := c.refuse
	delay := c.delay
	c.mutex.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-request.Context().Done():
		}
	}
	if status != 0 {
		response.WriteHeader(status)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

func (c *collected) received() [][]any {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([][]any(nil), c.batches...)
}

func (c *collected) total() int {
	count := 0
	for _, batch := range c.received() {
		count += len(batch)
	}
	return count
}

func sinking(t *testing.T, token string) (*collected, *Collector, *httptest.Server) {
	t.Helper()
	upstream := &collected{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)

	sink, err := NewCollector(CollectorConfig{
		URL: server.URL + collectorIngestPath, Token: token, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sink.Close(ctx)
	})
	return upstream, sink, server
}

// unanswering is a collector that accepts a connection and never replies.
//
// It is released explicitly at the end of the test rather than left blocking,
// because httptest.Server.Close waits for outstanding requests: a handler that
// truly never returned would hang the test binary rather than the sink.
func unanswering(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	// Cleanups run last-registered-first, so the release happens before the
	// close that waits on it.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server
}

func sample(id string) Event {
	return Event{
		SchemaVersion: SchemaVersion, EventID: newIdentifier(),
		EventType: ModelCompleted, ObservedAt: timestamp(time.Now()),
		Producer: Producer{Name: "machinist-model-proxy", Version: Version, InstanceID: newIdentifier()},
		Agent:    Agent{ID: "reviewer", DisplayName: "Reviewer"},
		SpanID:   optional(id),
		Attributes: map[string]any{
			"duration_ms": 1.0, "http_status": 200, "correlation": CorrelationExact,
		},
	}
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTheSinkDeliversWhatItWasGiven(t *testing.T) {
	upstream, sink, _ := sinking(t, "collector-token")
	for index := 0; index < 5; index++ {
		if !sink.Enqueue(sample("span-" + itoa(index))) {
			t.Fatalf("event %d was dropped by an empty queue", index)
		}
	}
	// Waited on the sink's own count, not the collector's. The collector
	// records a batch before it answers, and the sink counts it as sent only
	// after the answer arrives — so waiting on arrival can win the race
	// against the counter that the assertion below is about.
	waitFor(t, func() bool { return sink.Stats().Sent == 5 }, "five events to be delivered")

	if upstream.total() != 5 {
		t.Fatalf("the collector received %d events", upstream.total())
	}
	if token := upstream.tokens[0]; token != "Bearer collector-token" {
		t.Fatalf("authorization = %q", token)
	}
	if stats := sink.Stats(); stats.Dropped != 0 || stats.Failed != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestTheSinkSendsBatchesTheCollectorWillAccept(t *testing.T) {
	// The collector refuses a batch over its own maximum, and a refusal is
	// indistinguishable here from any other. Staying under it is the only way
	// a full queue does not become a total loss.
	upstream, sink, _ := sinking(t, "")
	for index := 0; index < MaximumBatch*3; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	waitFor(t, func() bool { return sink.Stats().Sent == MaximumBatch*3 }, "every event to be delivered")

	for _, batch := range upstream.received() {
		if len(batch) > telemetry.DefaultMaximumBatch {
			t.Fatalf("a batch of %d exceeds the collector's maximum of %d",
				len(batch), telemetry.DefaultMaximumBatch)
		}
		if len(batch) > MaximumBatch {
			t.Fatalf("a batch of %d exceeds this sink's own maximum", len(batch))
		}
	}
}

func TestTheSinkNeverBlocksTheCallItIsMeasuring(t *testing.T) {
	// A proxy whose telemetry slowed the calls it measured would be changing
	// the thing it exists to observe. The collector here never answers.
	stalled := unanswering(t)

	sink, err := NewCollector(CollectorConfig{
		URL: stalled.URL + collectorIngestPath, Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(ctx)
	})

	started := time.Now()
	for index := 0; index < QueueCapacity*2; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("enqueuing took %s against a collector that never answers", elapsed)
	}
	if queued := sink.Stats().Queued; queued > QueueCapacity {
		t.Fatalf("queued = %d, want no more than %d", queued, QueueCapacity)
	}
}

func TestAFullQueueDropsTheOldestAndSaysSo(t *testing.T) {
	// An operator cannot tell a quiet system from a lost stream unless the
	// loss is counted. Without this, an empty dashboard has two explanations
	// that look exactly alike.
	stalled := unanswering(t)
	sink, err := NewCollector(CollectorConfig{
		URL: stalled.URL + collectorIngestPath, Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sink.Close(ctx)
	})

	for index := 0; index < QueueCapacity+100; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	if dropped := sink.Stats().Dropped; dropped == 0 {
		t.Fatal("a queue past its capacity reported no loss")
	}
}

func TestARefusedBatchIsCountedAndNotRetriedForever(t *testing.T) {
	upstream, sink, _ := sinking(t, "")
	upstream.mutex.Lock()
	upstream.refuse = http.StatusUnprocessableEntity
	upstream.mutex.Unlock()

	for index := 0; index < 10; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	waitFor(t, func() bool { return sink.Stats().Failed == 10 }, "the refusal to be counted")
	if queued := sink.Stats().Queued; queued != 0 {
		t.Fatalf("queued = %d, want a refused batch not requeued", queued)
	}
}

func TestClosingDeliversWhatIsStillQueued(t *testing.T) {
	// A shutdown that dropped the last batch would lose exactly the events
	// describing whatever caused the shutdown.
	upstream, sink, _ := sinking(t, "")
	for index := 0; index < 20; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if upstream.total() != 20 {
		t.Fatalf("delivered %d of 20 before closing", upstream.total())
	}
	if sink.Enqueue(sample("late")) {
		t.Fatal("a closed sink accepted an event")
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
}

func TestClosingWaitsForASubmissionAlreadyInFlight(t *testing.T) {
	// The sender is the only goroutine that takes from the queue. A Close that
	// drained the queue itself would find nothing left while the sender was
	// still delivering, cancel the context underneath it, and lose exactly the
	// batch describing whatever caused the shutdown.
	upstream, sink, _ := sinking(t, "")
	upstream.hold(200 * time.Millisecond)
	for index := 0; index < 20; index++ {
		sink.Enqueue(sample("span-" + itoa(index)))
	}
	// The queue empties when the sender takes the batch, which is before the
	// collector has answered: from here the submission is in flight.
	waitFor(t, func() bool { return sink.Stats().Queued == 0 }, "the sender to take the batch")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The stub records a batch before it answers, so its tally cannot tell a
	// delivery from a cancelled one. The sink counts a batch sent only once
	// the answer arrives, which is the thing at issue.
	if stats := sink.Stats(); stats.Sent != 20 || stats.Failed != 0 {
		t.Fatalf("sent %d, failed %d; want 20 delivered and none lost to the close",
			stats.Sent, stats.Failed)
	}
}

func TestTheSinkRefusesACollectorItShouldNotSpeakTo(t *testing.T) {
	// This stream is a live description of what every agent on this machine is
	// doing. Sending it off the box must be impossible rather than a
	// configuration mistake.
	refused := map[string]string{
		"empty":            "",
		"not a loopback":   "http://collector.example/api/v1/events",
		"a private range":  "http://10.0.0.5:7900/api/v1/events",
		"https elsewhere":  "https://127.0.0.1:7900/api/v1/events",
		"another path":     "http://127.0.0.1:7900/admin",
		"a query":          "http://127.0.0.1:7900/api/v1/events?token=x",
		"credentials":      "http://user:pass@127.0.0.1:7900/api/v1/events",
		"a fragment":       "http://127.0.0.1:7900/api/v1/events#x",
		"a hostname alias": "http://localhost:7900/api/v1/events",
	}
	for name, value := range refused {
		if _, err := NewCollector(CollectorConfig{URL: value}); err == nil {
			t.Fatalf("%s (%q) was accepted as a collector", name, value)
		}
	}
	sink, err := NewCollector(CollectorConfig{URL: "http://127.0.0.1:7900" + collectorIngestPath})
	if err != nil {
		t.Fatalf("the collector's own URL was refused: %v", err)
	}
	_ = sink.Close(context.Background())
}

func TestTheSinkAgreesWithTheCollectorOnWhereEventsGo(t *testing.T) {
	// The path is written in both packages so neither has to import the other.
	// Writing it twice is only safe if something notices when they diverge.
	if collectorIngestPath != telemetry.IngestPath {
		t.Fatalf("the sink posts to %q; the collector listens on %q",
			collectorIngestPath, telemetry.IngestPath)
	}
}

func TestTheProxyDeliversARealMeasurementToARealCollector(t *testing.T) {
	// End to end through both halves: a forwarded call, the events it emits,
	// the sink, and the collector's own validation of what arrived.
	upstream := &collected{}
	collector := httptest.NewServer(upstream)
	t.Cleanup(collector.Close)

	sink, err := NewCollector(CollectorConfig{
		URL: collector.URL + collectorIngestPath, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}

	origin := httptest.NewServer(http.HandlerFunc(ok))
	t.Cleanup(origin.Close)
	settings, err := Validate(Config{
		Upstream: origin.URL, Model: "ds-0731", EndpointID: "dgx-primary",
		ConnectTimeout: time.Second, ResponseTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	front := httptest.NewServer(New(settings, sink).Handler())
	t.Cleanup(front.Close)

	drain(t, front, "/v1/chat/completions")
	// The request returns when the headers arrive; the proxy finishes
	// measuring after the body is copied and the sink delivers after that.
	waitFor(t, func() bool { return upstream.total() >= 2 }, "the call to be delivered")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	if upstream.total() < 2 {
		t.Fatalf("the collector received %d events, want a start and a completion", upstream.total())
	}
	for _, batch := range upstream.received() {
		for _, event := range batch {
			if _, err := telemetry.ValidateEvent(event); err != nil {
				encoded, _ := json.Marshal(event)
				t.Fatalf("the collector refused what the sink delivered: %v\n%s", err, encoded)
			}
		}
	}
}
