package telemetry

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ValidationError reports why an event was refused.
//
// It carries a stable code and the JSON path that failed, and never the value
// that failed. A collector's error responses and logs are read by more people
// than the events are, so echoing a rejected value would put unvalidated
// producer input — the very input suspected of carrying a secret — somewhere it
// is even easier to read.
type ValidationError struct {
	Code string
	Path string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s at %s", e.Code, e.Path)
}

func fail(code, path string) error {
	return ValidationError{Code: code, Path: path}
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@=-]{0,255}$`)
	controlPattern    = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
	uuidPattern       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// secretPatterns are credential shapes that must never be stored, whatever
// field they arrive in.
//
// This is a backstop, not the defence. The defence is that no field in this
// contract has any business holding a credential: the shapes are enumerated,
// the identifiers are pattern-matched, and there is no free-text field. This
// catches the case where a producer puts a token somewhere it structurally
// fits, such as a version string.
//
// The literals are split so that scanning this repository for credentials does
// not match the scanner itself.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile("AK" + `IA[0-9A-Z]{16}`),
	regexp.MustCompile("gh" + `[pousr]_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile("s" + `k-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile("xo" + `x[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile("-----BEGIN " + `(?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
}

// topLevelFields is the exact set of fields an event carries. Exact means
// exact: an unknown field is refused, and so is a missing one. A producer that
// omits a field has not reported it as absent — it has failed to say anything,
// and the two are not the same.
var topLevelFields = set("schema_version", "event_id", "event_type", "observed_at",
	"monotonic_offset_ms", "producer", "agent", "harness", "model", "endpoint_id",
	"session_id", "turn_id", "span_id", "parent_span_id", "attributes")

var (
	producerFields = set("name", "version", "instance_id")
	agentFields    = set("id", "display_name")
)

// ValidateEvent validates one decoded event and returns it normalized.
//
// The returned Event is storage-safe: every string has been length-bounded,
// control-character checked, and screened for credential shapes; every number
// is finite and in range; every timestamp is UTC with a timezone it actually
// declared. Nothing reaches storage that did not pass through here.
func ValidateEvent(value any) (Event, error) {
	raw, err := requireExactFields(value, topLevelFields, "$")
	if err != nil {
		return Event{}, err
	}

	version, ok := asNumber(raw["schema_version"])
	if !ok || int(version) != SchemaVersion || version != math.Trunc(version) {
		return Event{}, fail("unsupported_schema_version", "$.schema_version")
	}

	eventID, err := safeString(raw["event_id"], "$.event_id", 36, false)
	if err != nil {
		return Event{}, err
	}
	if !uuidPattern.MatchString(eventID) {
		return Event{}, fail("invalid_uuid", "$.event_id")
	}

	typeName, err := safeString(raw["event_type"], "$.event_type", 64, true)
	if err != nil {
		return Event{}, err
	}
	eventType := EventType(typeName)
	if !eventType.Valid() {
		return Event{}, fail("unsupported_event_type", "$.event_type")
	}

	producer, err := validateProducer(raw["producer"])
	if err != nil {
		return Event{}, err
	}
	agent, err := validateAgent(raw["agent"])
	if err != nil {
		return Event{}, err
	}

	observedAt, err := timestamp(raw["observed_at"], "$.observed_at")
	if err != nil {
		return Event{}, err
	}
	offset, err := number(raw["monotonic_offset_ms"], "$.monotonic_offset_ms", false)
	if err != nil {
		return Event{}, err
	}

	event := Event{
		SchemaVersion:     SchemaVersion,
		EventID:           strings.ToLower(eventID),
		EventType:         eventType,
		ObservedAt:        observedAt,
		MonotonicOffsetMS: offset,
		Producer:          producer,
		Agent:             agent,
	}

	for field, target := range map[string]**string{
		"harness": &event.Harness, "model": &event.Model, "endpoint_id": &event.EndpointID,
		"session_id": &event.SessionID, "turn_id": &event.TurnID,
		"span_id": &event.SpanID, "parent_span_id": &event.ParentSpanID,
	} {
		identifier, err := nullableIdentifier(raw[field], "$."+field)
		if err != nil {
			return Event{}, err
		}
		*target = identifier
	}

	attributes, err := validateAttributes(eventType, raw["attributes"])
	if err != nil {
		return Event{}, err
	}
	event.Attributes = attributes
	return event, nil
}

// DefaultMaximumBatch is how many events one submission may carry.
const DefaultMaximumBatch = 100

// ValidateBatch validates a submission, which is either one event or a list of
// them. An empty list is refused rather than accepted as nothing to do: a
// producer that meant to send nothing does not send a batch.
func ValidateBatch(value any, maximum int) ([]Event, error) {
	if maximum <= 0 {
		maximum = DefaultMaximumBatch
	}
	items, ok := value.([]any)
	if !ok {
		event, err := ValidateEvent(value)
		if err != nil {
			return nil, err
		}
		return []Event{event}, nil
	}
	if len(items) == 0 {
		return nil, fail("empty_batch", "$")
	}
	if len(items) > maximum {
		return nil, fail("batch_too_large", "$")
	}
	events := make([]Event, 0, len(items))
	for _, item := range items {
		event, err := ValidateEvent(item)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func validateProducer(value any) (Producer, error) {
	raw, err := requireExactFields(value, producerFields, "$.producer")
	if err != nil {
		return Producer{}, err
	}
	name, err := safeString(raw["name"], "$.producer.name", 128, true)
	if err != nil {
		return Producer{}, err
	}
	version, err := safeString(raw["version"], "$.producer.version", 64, true)
	if err != nil {
		return Producer{}, err
	}
	instance, err := safeString(raw["instance_id"], "$.producer.instance_id", 128, true)
	if err != nil {
		return Producer{}, err
	}
	return Producer{Name: name, Version: version, InstanceID: instance}, nil
}

func validateAgent(value any) (Agent, error) {
	raw, err := requireExactFields(value, agentFields, "$.agent")
	if err != nil {
		return Agent{}, err
	}
	id, err := safeString(raw["id"], "$.agent.id", 128, true)
	if err != nil {
		return Agent{}, err
	}
	name, err := safeString(raw["display_name"], "$.agent.display_name", 128, false)
	if err != nil {
		return Agent{}, err
	}
	return Agent{ID: id, DisplayName: name}, nil
}

func validateAttributes(eventType EventType, value any) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fail("expected_object", "$.attributes")
	}
	allowed := eventAttributes[eventType]
	for _, key := range sortedKeys(raw) {
		if _, ok := allowed[key]; ok {
			continue
		}
		if _, ok := commonAttributes[key]; ok {
			continue
		}
		return nil, fail("unknown_attribute", "$.attributes."+key)
	}

	result := make(map[string]any, len(raw))
	for _, key := range sortedKeys(raw) {
		item := raw[key]
		path := "$.attributes." + key
		switch {
		case has(numericAttributes, key):
			parsed, err := number(item, path, false)
			if err != nil {
				return nil, err
			}
			result[key] = parsed
		case has(integerAttributes, key):
			parsed, err := number(item, path, true)
			if err != nil {
				return nil, err
			}
			result[key] = int64(parsed)
		case key == "measurement_quality":
			if err := enumerated(item, qualityValues, "invalid_measurement_quality", path); err != nil {
				return nil, err
			}
			result[key] = item
		case key == "correlation":
			if err := enumerated(item, correlationValues, "invalid_correlation", path); err != nil {
				return nil, err
			}
			result[key] = item
		case key == "cancellation_reason":
			if err := enumerated(item, cancellationReasonValues, "invalid_cancellation_reason", path); err != nil {
				return nil, err
			}
			result[key] = item
		case key == "tool_observation_mode":
			if err := enumerated(item, toolObservationModes, "invalid_tool_observation_mode", path); err != nil {
				return nil, err
			}
			result[key] = item
		case has(stringAttributes, key):
			parsed, err := safeString(item, path, 128, false)
			if err != nil {
				return nil, err
			}
			result[key] = parsed
		default:
			// The allowlist above admitted this attribute but no shape table
			// claims it, so the two have drifted apart. Refusing is the only
			// answer that does not store an unvalidated value.
			return nil, fail("unsupported_attribute", path)
		}
	}
	return result, nil
}

func requireExactFields(value any, allowed map[string]struct{}, path string) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fail("expected_object", path)
	}
	var unknown []string
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fail("unknown_field", path+"."+unknown[0])
	}
	var missing []string
	for key := range allowed {
		if _, ok := raw[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fail("missing_field", path+"."+missing[0])
	}
	return raw, nil
}

func safeString(value any, path string, maximum int, identifier bool) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > maximum {
		return "", fail("invalid_string", path)
	}
	if controlPattern.MatchString(text) || strings.ContainsAny(text, "\n\r") {
		return "", fail("unsafe_string", path)
	}
	if identifier && !identifierPattern.MatchString(text) {
		return "", fail("invalid_identifier", path)
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(text) {
			return "", fail("secret_like_value", path)
		}
	}
	return text, nil
}

func nullableIdentifier(value any, path string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, err := safeString(value, path, 256, true)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

func number(value any, path string, integer bool) (float64, error) {
	if _, isBool := value.(bool); isBool {
		// JSON true is a number in exactly no useful sense, and Go would not
		// convert it anyway; this exists so the refusal is the documented one.
		return 0, fail("invalid_number", path)
	}
	parsed, ok := asNumber(value)
	if !ok {
		if integer {
			return 0, fail("invalid_integer", path)
		}
		return 0, fail("invalid_number", path)
	}
	if integer {
		if parsed != math.Trunc(parsed) || parsed < 0 || parsed > 1e12 {
			return 0, fail("invalid_integer", path)
		}
		return parsed, nil
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1e15 {
		return 0, fail("invalid_number", path)
	}
	return parsed, nil
}

// asNumber accepts the shapes a JSON decoder produces for a number, including
// json.Number when the caller decoded with UseNumber.
func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// timestamp normalizes an RFC 3339 time to UTC with millisecond precision.
//
// A timestamp with no timezone is refused rather than assumed to be UTC. The
// producer and the collector are frequently not in the same zone, and a guess
// here would silently move every measurement by hours.
func timestamp(value any, path string) (string, error) {
	raw, err := safeString(value, path, 64, false)
	if err != nil {
		return "", err
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		if _, offsetless := time.Parse("2006-01-02T15:04:05.999999999", raw); offsetless == nil {
			return "", fail("timestamp_requires_timezone", path)
		}
		return "", fail("invalid_timestamp", path)
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

func enumerated(value any, allowed map[string]struct{}, code, path string) error {
	text, ok := value.(string)
	if !ok || !has(allowed, text) {
		return fail(code, path)
	}
	return nil
}

func has(members map[string]struct{}, key string) bool {
	_, ok := members[key]
	return ok
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
