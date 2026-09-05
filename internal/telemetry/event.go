// Package telemetry carries the metadata-only agent telemetry contract.
//
// Ported from Bostonvex/buzz-agent-observability collector/schema.py as part of
// migration plan Phase C.
//
// Every event is metadata about work: how long a turn took, how many tokens it
// used, which tool ran. No prompt, no completion, no file content, no argument
// ever belongs in one. Validation enforces that structurally — an event may
// carry only the fields its own type declares, and only in the shapes declared
// for them — so a producer cannot smuggle content through in a field nobody
// thought to constrain.
package telemetry

// SchemaVersion is the event contract version. An event declaring any other
// version is rejected rather than interpreted: a producer that disagrees with
// the collector about the contract is not a producer to guess about.
const SchemaVersion = 1

// EventType is the kind of thing an event reports.
type EventType string

const (
	EventProcessStarted        EventType = "process.started"
	EventProcessExited         EventType = "process.exited"
	EventSessionStarted        EventType = "session.started"
	EventSessionEnded          EventType = "session.ended"
	EventTurnStarted           EventType = "turn.started"
	EventTurnFirstActivity     EventType = "turn.first_activity"
	EventTurnFirstVisibleText  EventType = "turn.first_visible_text"
	EventTurnFirstTool         EventType = "turn.first_tool"
	EventTurnStall             EventType = "turn.stall"
	EventTurnCompleted         EventType = "turn.completed"
	EventTurnFailed            EventType = "turn.failed"
	EventTurnCancelled         EventType = "turn.cancelled"
	EventToolStarted           EventType = "tool.started"
	EventToolUpdated           EventType = "tool.updated"
	EventToolCompleted         EventType = "tool.completed"
	EventToolFailed            EventType = "tool.failed"
	EventUsageUpdated          EventType = "usage.updated"
	EventModelRequestStarted   EventType = "model.request_started"
	EventModelFirstToken       EventType = "model.first_token"
	EventModelCompleted        EventType = "model.completed"
	EventModelFailed           EventType = "model.failed"
	EventCollectorDroppedEvent EventType = "collector.dropped_events"
	EventProtocolAnomaly       EventType = "protocol.anomaly"
	EventServerSample          EventType = "server.sample"
	EventHardwareSample        EventType = "hardware.sample"
)

// eventAttributes is the closed set of attributes each event type may carry.
//
// It is both the list of event types and the list of what each may say. An
// event type absent from this table does not exist, and an attribute absent
// from its entry is refused — which is what keeps the contract metadata-only.
var eventAttributes = map[EventType]map[string]struct{}{
	EventProcessStarted:       set("harness_version", "tool_observation_mode"),
	EventProcessExited:        set("exit_code", "signal", "outcome"),
	EventSessionStarted:       set(),
	EventSessionEnded:         set("duration_ms", "outcome"),
	EventTurnStarted:          set("turn_class", "temperature_profile"),
	EventTurnFirstActivity:    set("elapsed_ms", "update_kind"),
	EventTurnFirstVisibleText: set("elapsed_ms"),
	EventTurnFirstTool:        set("elapsed_ms", "tool_kind"),
	EventTurnStall:            set("elapsed_ms", "gap_ms", "threshold_ms"),
	EventTurnCompleted: set("duration_ms", "ttfa_ms", "ttfvt_ms", "first_tool_ms",
		"max_stall_ms", "tool_count", "tool_observation_mode", "outcome"),
	EventTurnFailed: set("duration_ms", "ttfa_ms", "ttfvt_ms", "first_tool_ms",
		"max_stall_ms", "tool_count", "tool_observation_mode", "error_category", "error_code"),
	EventTurnCancelled: set("duration_ms", "ttfa_ms", "ttfvt_ms", "first_tool_ms",
		"max_stall_ms", "tool_count", "tool_observation_mode", "cancellation_reason"),
	EventToolStarted:         set("tool_kind", "status"),
	EventToolUpdated:         set("tool_kind", "status", "elapsed_ms"),
	EventToolCompleted:       set("tool_kind", "status", "duration_ms"),
	EventToolFailed:          set("tool_kind", "status", "duration_ms", "error_category", "error_code"),
	EventUsageUpdated:        set("token_kind", "value", "semantics"),
	EventModelRequestStarted: set("correlation"),
	EventModelFirstToken:     set("elapsed_ms", "correlation"),
	EventModelCompleted: set("duration_ms", "connection_ms", "first_byte_ms", "decode_ms",
		"http_status", "input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens", "correlation"),
	EventModelFailed: set("duration_ms", "connection_ms", "first_byte_ms", "http_status",
		"error_category", "error_code", "correlation"),
	EventCollectorDroppedEvent: set("dropped_count", "queue_depth"),
	EventProtocolAnomaly:       set("anomaly_kind", "line_bytes"),
	EventServerSample:          set("metric_name", "value", "unit"),
	EventHardwareSample:        set("provider_id", "node_id", "metric_name", "value", "unit"),
}

// Valid reports whether t is an event type the contract defines.
func (t EventType) Valid() bool {
	_, ok := eventAttributes[t]
	return ok
}

// commonAttributes may accompany any event type.
var commonAttributes = set("measurement_quality")

// Closed attribute vocabularies. A value outside one of these is refused rather
// than stored as an opaque string: the whole point of an enumerated field is
// that a reader can rely on the enumeration.
var (
	qualityValues            = set("exact", "derived", "estimated", "unavailable")
	correlationValues        = set("exact", "ambiguous", "unavailable")
	toolObservationModes     = set("acp_updates", "execution_hook", "unavailable")
	cancellationReasonValues = set("client_requested", "superseded_by_prompt", "agent_reported")
)

// Attribute shapes. Every attribute named in eventAttributes must appear in
// exactly one of these, or in the enumerated set above; an attribute in none of
// them is refused as unsupported rather than stored unvalidated.
var (
	numericAttributes = set("elapsed_ms", "gap_ms", "threshold_ms", "duration_ms",
		"connection_ms", "first_byte_ms", "decode_ms", "ttfa_ms", "ttfvt_ms",
		"first_tool_ms", "max_stall_ms", "value")
	integerAttributes = set("exit_code", "tool_count", "http_status", "input_tokens",
		"output_tokens", "cached_tokens", "reasoning_tokens", "dropped_count",
		"queue_depth", "line_bytes")
	stringAttributes = set("harness_version", "tool_observation_mode", "signal",
		"outcome", "turn_class", "temperature_profile", "update_kind", "tool_kind",
		"status", "error_category", "error_code", "token_kind", "semantics",
		"anomaly_kind", "metric_name", "unit", "provider_id", "node_id")
)

// Producer is the software that emitted the event.
type Producer struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
}

// Agent is who the work was done as. It is an identity, never a credential.
type Agent struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Event is one validated, storage-safe telemetry event.
//
// The nullable fields are pointers so that "not reported" and "reported as
// empty" stay distinguishable — a distinction a reader needs and a plain string
// would erase.
type Event struct {
	SchemaVersion     int            `json:"schema_version"`
	EventID           string         `json:"event_id"`
	EventType         EventType      `json:"event_type"`
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

func set(values ...string) map[string]struct{} {
	members := make(map[string]struct{}, len(values))
	for _, value := range values {
		members[value] = struct{}{}
	}
	return members
}
