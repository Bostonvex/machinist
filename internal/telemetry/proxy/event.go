package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// SchemaVersion is the collector's event contract version. It is stated here
// rather than imported so that this package and the collector are two ends of
// a protocol rather than one program: the proxy is a separate process and may
// be pointed at a collector it was not built alongside. The test that runs an
// emitted event through the collector's own validator is what keeps the two
// honest.
const SchemaVersion = 1

// Event is one measurement, in the shape the collector's ingest route accepts.
//
// Every field is present on every event, including the ones that are null. The
// collector refuses an event with a missing field, and it is right to: a
// producer that omitted a field has not reported it as absent, it has failed
// to say anything, and the two are not the same fact.
type Event struct {
	SchemaVersion     int            `json:"schema_version"`
	EventID           string         `json:"event_id"`
	EventType         string         `json:"event_type"`
	ObservedAt        string         `json:"observed_at"`
	MonotonicOffsetMS float64        `json:"monotonic_offset_ms"`
	Producer          Producer       `json:"producer"`
	Agent             Agent          `json:"agent"`
	Harness           *string        `json:"harness"`
	Model             *string        `json:"model"`
	EndpointID        *string        `json:"endpoint_id"`
	SessionID         *string        `json:"session_id"`
	TurnID            *string        `json:"turn_id"`
	SpanID            *string        `json:"span_id"`
	ParentSpanID      *string        `json:"parent_span_id"`
	Attributes        map[string]any `json:"attributes"`
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

// optional is a nullable wire field. An empty string is sent as null, because
// the collector reads "" as a malformed identifier and null as "not reported",
// and not reported is what an empty one means here.
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// newIdentifier is a random UUID, which is what the collector's ingest route
// requires an event id to be. It deduplicates on the id, so two events sharing
// one become one event.
func newIdentifier() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail on any platform this runs on, and an
		// identifier derived from something weaker would collide silently.
		panic("machinist proxy: no randomness available: " + err.Error())
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
