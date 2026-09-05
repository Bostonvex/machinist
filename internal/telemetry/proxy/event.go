package proxy

import (
	"time"
)

// Event is one measurement, in the shape the collector's ingest route accepts.
//
// It is deliberately not the collector's own Event type: this package is a
// separate process from the collector and talks to it over HTTP, so the only
// thing they share is the wire contract. Importing the type would make a
// change to the collector's internals look like a change to the protocol.
type Event struct {
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	ObservedAt string         `json:"observed_at"`
	SpanID     string         `json:"span_id"`
	Producer   Producer       `json:"producer"`
	Agent      Agent          `json:"agent"`
	Attributes map[string]any `json:"attributes"`
}

type Producer struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
}

type Agent struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// The event types this proxy produces. They are the four the collector's model
// metrics are computed from.
const (
	ModelRequestStarted = "model.request_started"
	ModelFirstToken     = "model.first_token"
	ModelCompleted      = "model.completed"
	ModelFailed         = "model.failed"
)

// Sink is where measurements go. Delivery is the caller's problem, and it is a
// problem with one rule: it must never change what the model client sees.
type Sink interface {
	// Enqueue reports whether the event was accepted. A false is a dropped
	// measurement, never a failed request.
	Enqueue(Event) bool
}

// discard is the sink of a proxy configured without a collector. Measuring
// nothing is a supported deployment: the proxy still forwards.
type discard struct{}

func (discard) Enqueue(Event) bool { return true }

// timestamp is the collector's wire format: UTC, milliseconds, Z.
func timestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}
