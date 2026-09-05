package provider

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// stubProvider stands in for a machine. polls counts how many times it was
// asked, so a test can wait for real progress instead of sleeping.
type stubProvider struct {
	name    string
	polls   atomic.Int64
	samples []Sample
	err     error
	block   chan struct{}
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) Poll(ctx context.Context) ([]Sample, error) {
	p.polls.Add(1)
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.samples, p.err
}

func oneSample() []Sample {
	return []Sample{{
		Scope: ScopeHardware, MetricName: "gpu.0.utilization_percent", Value: 41,
		Unit: "percent", ProviderID: "nvidia-smi", NodeID: "dgx-spark",
	}}
}

type collector struct {
	mutex  sync.Mutex
	events []telemetry.Event
}

func (c *collector) emit(_ context.Context, events []telemetry.Event) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.events = append(c.events, events...)
}

func (c *collector) count() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return len(c.events)
}

func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

// waitFor polls a condition rather than sleeping a fixed interval, so a slow
// machine makes the test slower and never makes it wrong.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOneBrokenProviderDoesNotStopTheOthers(t *testing.T) {
	// A host typically runs the GPU reader and the inference-server reader
	// together, and they fail for entirely unrelated reasons. Losing both
	// because one broke is how a monitoring system stops monitoring at exactly
	// the moment something is wrong.
	broken := &stubProvider{name: "broken", err: errors.New("nvidia-smi is not on this host")}
	working := &stubProvider{name: "working", samples: oneSample()}
	var sink collector

	supervisor, err := NewSupervisor([]Provider{broken, working}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer cancel()

	waitFor(t, "the working provider to report", func() bool { return sink.count() > 0 })
	waitFor(t, "the broken provider to be marked", func() bool {
		return supervisor.Diagnostics()["broken"].Failures > 0
	})

	status := supervisor.Diagnostics()
	if status["broken"].State != "degraded" {
		t.Fatalf("a failing provider was left in state %q", status["broken"].State)
	}
	if status["working"].State != "ok" || status["working"].Samples == 0 {
		t.Fatalf("the healthy provider was affected by the broken one: %+v", status["working"])
	}
}

func TestASlowProviderDelaysOnlyItself(t *testing.T) {
	// A provider polls a machine over SSH or HTTP. One that has stalled must
	// not hold up the reader that is answering.
	stalled := &stubProvider{name: "stalled", block: make(chan struct{})}
	quick := &stubProvider{name: "quick", samples: oneSample()}
	var sink collector

	supervisor, err := NewSupervisor([]Provider{stalled, quick}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer func() { cancel(); close(stalled.block) }()

	waitFor(t, "the quick provider to report repeatedly", func() bool { return quick.polls.Load() >= 2 })
	if stalled.polls.Load() != 1 {
		t.Fatalf("the stalled provider was polled again while still stalled: %d", stalled.polls.Load())
	}
}

func TestAFloodOfSamplesIsRefusedRatherThanStored(t *testing.T) {
	// A provider that suddenly reports thousands of samples has been pointed at
	// something other than what it was written for, and accepting the flood
	// would fill the database with it.
	flood := make([]Sample, maximumSamplesPerPoll+1)
	for index := range flood {
		flood[index] = oneSample()[0]
	}
	provider := &stubProvider{name: "flood", samples: flood}
	var sink collector

	supervisor, err := NewSupervisor([]Provider{provider}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer cancel()

	waitFor(t, "the flood to be refused", func() bool { return supervisor.Diagnostics()["flood"].Failures > 0 })
	if sink.count() != 0 {
		t.Fatalf("%d events from a flood were stored", sink.count())
	}
}

func TestOneUnusableSampleDoesNotDiscardThePoll(t *testing.T) {
	// A GPU reporting a nonsense temperature should not also cost the
	// utilisation reading taken from the same line.
	mixed := append(oneSample(), Sample{Scope: ScopeHardware, MetricName: "not a name", Value: 1, Unit: "percent"})
	provider := &stubProvider{name: "mixed", samples: mixed}
	var sink collector

	supervisor, err := NewSupervisor([]Provider{provider}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer cancel()

	waitFor(t, "the good sample to arrive", func() bool { return sink.count() > 0 })
	if status := supervisor.Diagnostics()["mixed"]; status.State != "ok" {
		t.Fatalf("a poll with one bad sample was recorded as %q", status.State)
	}
}

func TestASupervisorSaysWhyAProviderIsQuiet(t *testing.T) {
	// A provider that never worked and one that stopped working look nothing
	// alike here, which is the difference between a missing binary and an
	// endpoint that went down.
	provider := &stubProvider{name: "nvidia-smi", err: errors.New("nvidia-smi was not found on PATH")}
	var sink collector
	supervisor, err := NewSupervisor([]Provider{provider}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	if pending := supervisor.Diagnostics()["nvidia-smi"]; pending.State != "pending" {
		t.Fatalf("a provider that has not run yet reported %q", pending.State)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer cancel()

	waitFor(t, "a diagnosis", func() bool { return supervisor.Diagnostics()["nvidia-smi"].Message != "" })
	if message := supervisor.Diagnostics()["nvidia-smi"].Message; !strings.Contains(message, "PATH") {
		t.Fatalf("the diagnosis did not carry the reason: %q", message)
	}
}

func TestCancellationIsNotAProviderFailure(t *testing.T) {
	// Shutting the collector down must not leave every provider recorded as
	// broken; the next operator to read the diagnostics would go looking for a
	// fault that was a stop.
	stalled := &stubProvider{name: "stalled", block: make(chan struct{})}
	var sink collector
	supervisor, err := NewSupervisor([]Provider{stalled}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { supervisor.Run(ctx); close(done) }()
	waitFor(t, "the first poll", func() bool { return stalled.polls.Load() > 0 })
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if failures := supervisor.Diagnostics()["stalled"].Failures; failures != 0 {
		t.Fatalf("shutdown was recorded as %d provider failures", failures)
	}
	close(stalled.block)
}

func TestASupervisorRefusesAnUnworkableSchedule(t *testing.T) {
	// Below a second the poller costs more than what it measures; above an hour
	// the samples are too sparse to say anything about a turn.
	for _, interval := range []time.Duration{0, time.Millisecond, 2 * time.Hour} {
		if _, err := NewSupervisor(nil, func(context.Context, []telemetry.Event) {}, interval, "1.2.3", quiet()); err == nil {
			t.Fatalf("interval %s was accepted", interval)
		}
	}
	if _, err := NewSupervisor(nil, nil, time.Second, "1.2.3", quiet()); err == nil {
		t.Fatal("a supervisor with nowhere to send samples was accepted")
	}
}

func TestTwoProvidersCannotShareAName(t *testing.T) {
	// They would share a status row, and an operator reading it could not tell
	// which of them was failing.
	first := &stubProvider{name: "vllm"}
	second := &stubProvider{name: "vllm"}
	_, err := NewSupervisor([]Provider{first, second}, func(context.Context, []telemetry.Event) {}, time.Second, "1.2.3", quiet())
	if err == nil {
		t.Fatal("two providers with the same name were accepted")
	}
}

func TestEveryEmittedEventIsUnique(t *testing.T) {
	// The store deduplicates on event id, so colliding identifiers would be
	// silently dropped and a series would go quiet for no visible reason.
	provider := &stubProvider{name: "gpu", samples: oneSample()}
	var sink collector
	supervisor, err := NewSupervisor([]Provider{provider}, sink.emit, time.Second, "1.2.3", quiet())
	if err != nil {
		t.Fatalf("could not build a supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	defer cancel()

	waitFor(t, "several polls", func() bool { return sink.count() >= 3 })
	cancel()

	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	seen := map[string]bool{}
	for _, event := range sink.events {
		if seen[event.EventID] {
			t.Fatalf("event id %s was emitted twice", event.EventID)
		}
		seen[event.EventID] = true
	}
}
