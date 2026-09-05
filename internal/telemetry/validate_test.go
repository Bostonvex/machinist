package telemetry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// decode parses a JSON document the way the ingest server will, so the tests
// exercise the types a decoder actually produces rather than hand-built Go maps.
func decode(t *testing.T, document string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		t.Fatalf("test fixture is not JSON: %v", err)
	}
	return value
}

const turnCompleted = `{
  "schema_version": 1,
  "event_id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
  "event_type": "turn.completed",
  "observed_at": "2026-09-05T12:00:00.123456Z",
  "monotonic_offset_ms": 4200.5,
  "producer": {"name": "machinist", "version": "0.5.0", "instance_id": "mac-mini"},
  "agent": {"id": "dgx-deepcode", "display_name": "DGX DeepCode"},
  "harness": "deepcode",
  "model": "ds-0731",
  "endpoint_id": "mac-mini",
  "session_id": "session-1",
  "turn_id": "turn-1",
  "span_id": null,
  "parent_span_id": null,
  "attributes": {"duration_ms": 4200.5, "tool_count": 3, "outcome": "succeeded", "measurement_quality": "exact"}
}`

// spoil re-encodes the fixture with one field replaced, so each test changes
// exactly one thing about an otherwise valid event.
func spoil(t *testing.T, mutate func(map[string]any)) any {
	t.Helper()
	value := decode(t, turnCompleted).(map[string]any)
	mutate(value)
	return value
}

func mustReject(t *testing.T, value any, code string) {
	t.Helper()
	_, err := ValidateEvent(value)
	if err == nil {
		t.Fatalf("event was accepted; expected %s", code)
	}
	var validation ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error %v is not a ValidationError", err)
	}
	if validation.Code != code {
		t.Fatalf("code = %q, want %q (path %s)", validation.Code, code, validation.Path)
	}
}

func TestAValidEventIsNormalized(t *testing.T) {
	event, err := ValidateEvent(decode(t, turnCompleted))
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != EventTurnCompleted {
		t.Fatalf("event type = %q", event.EventType)
	}
	// Timestamps are normalized to UTC with millisecond precision, so events
	// from different producers sort against each other.
	if event.ObservedAt != "2026-09-05T12:00:00.123Z" {
		t.Fatalf("observed_at = %q", event.ObservedAt)
	}
	if event.Harness == nil || *event.Harness != "deepcode" {
		t.Fatalf("harness = %v", event.Harness)
	}
	// A field the producer explicitly reported as absent stays distinguishable
	// from one it set to something.
	if event.SpanID != nil {
		t.Fatalf("span_id = %v, want nil", event.SpanID)
	}
	if event.Attributes["tool_count"] != int64(3) {
		t.Fatalf("tool_count = %#v", event.Attributes["tool_count"])
	}
}

// A producer that disagrees with the collector about the contract is not a
// producer to guess about.
func TestAnotherSchemaVersionIsRefused(t *testing.T) {
	for _, version := range []any{2, 0, "1", 1.5, nil} {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["schema_version"] = version
		}), "unsupported_schema_version")
	}
}

// Exact means exact in both directions: an unknown field is a producer saying
// something the contract has no meaning for, and a missing one is a producer
// failing to say anything at all.
func TestFieldsAreExactInBothDirections(t *testing.T) {
	mustReject(t, spoil(t, func(event map[string]any) {
		event["prompt"] = "the actual prompt text"
	}), "unknown_field")

	mustReject(t, spoil(t, func(event map[string]any) {
		delete(event, "agent")
	}), "missing_field")
}

// The whole contract is metadata-only. An attribute the event type does not
// declare is the obvious way to smuggle content through, so it is refused
// rather than stored as an extra.
func TestAnUndeclaredAttributeIsRefused(t *testing.T) {
	mustReject(t, spoil(t, func(event map[string]any) {
		event["attributes"].(map[string]any)["completion"] = "the model's answer"
	}), "unknown_attribute")

	// Declared for another event type is not declared for this one.
	mustReject(t, spoil(t, func(event map[string]any) {
		event["attributes"].(map[string]any)["http_status"] = 200.0
	}), "unknown_attribute")
}

func TestEnumeratedAttributesRejectWhatIsNotEnumerated(t *testing.T) {
	for attribute, code := range map[string]string{
		"measurement_quality": "invalid_measurement_quality",
	} {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["attributes"].(map[string]any)[attribute] = "probably"
		}), code)
	}
}

func TestNumbersMustBeFiniteAndInRange(t *testing.T) {
	for _, value := range []any{-1.0, 1e16, "4200", true, nil} {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["attributes"].(map[string]any)["duration_ms"] = value
		}), "invalid_number")
	}
	for _, value := range []any{-1.0, 3.5, 1e13, "3"} {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["attributes"].(map[string]any)["tool_count"] = value
		}), "invalid_integer")
	}
}

// The producer and the collector are frequently not in the same zone. Assuming
// UTC would silently move every measurement by hours.
func TestATimestampWithoutAZoneIsRefusedNotAssumedUTC(t *testing.T) {
	mustReject(t, spoil(t, func(event map[string]any) {
		event["observed_at"] = "2026-09-05T12:00:00.123456"
	}), "timestamp_requires_timezone")

	mustReject(t, spoil(t, func(event map[string]any) {
		event["observed_at"] = "not a time"
	}), "invalid_timestamp")
}

// An offset timestamp is accepted and converted, because it did declare its
// zone — the refusal above is about absence, not about non-UTC.
func TestAnOffsetTimestampIsConvertedToUTC(t *testing.T) {
	event, err := ValidateEvent(spoil(t, func(event map[string]any) {
		event["observed_at"] = "2026-09-05T08:00:00.500-04:00"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if event.ObservedAt != "2026-09-05T12:00:00.500Z" {
		t.Fatalf("observed_at = %q", event.ObservedAt)
	}
}

// The defence is that no field has any business holding a credential. This is
// the backstop for a token put somewhere it structurally fits.
func TestCredentialShapesAreRefusedWhereverTheyFit(t *testing.T) {
	credentials := []string{
		"gh" + "p_0123456789abcdefghijklmnopqrstuvwxyz",
		"s" + "k-0123456789abcdefghijklmnop",
		"AK" + "IAIOSFODNN7EXAMPLE",
	}
	for _, credential := range credentials {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["producer"].(map[string]any)["version"] = credential
		}), "secret_like_value")

		mustReject(t, spoil(t, func(event map[string]any) {
			event["agent"].(map[string]any)["display_name"] = credential
		}), "secret_like_value")
	}
}

// A control character in a stored string is a log-injection and a terminal
// hazard, and no legitimate identifier carries one.
func TestControlCharactersAndNewlinesAreRefused(t *testing.T) {
	for _, value := range []string{"agent\nid", "agent\rid", "agent\x00id", "agent\x1bid"} {
		mustReject(t, spoil(t, func(event map[string]any) {
			event["agent"].(map[string]any)["display_name"] = value
		}), "unsafe_string")
	}
}

func TestIdentifiersMustLookLikeIdentifiers(t *testing.T) {
	for _, value := range []string{"-leading-dash", "has space", "has\"quote", ""} {
		_, err := ValidateEvent(spoil(t, func(event map[string]any) {
			event["harness"] = value
		}))
		if err == nil {
			t.Fatalf("identifier %q was accepted", value)
		}
	}
}

func TestUnknownEventTypesAreRefused(t *testing.T) {
	mustReject(t, spoil(t, func(event map[string]any) {
		event["event_type"] = "turn.invented"
	}), "unsupported_event_type")
}

func TestEventIDsMustBeUUIDs(t *testing.T) {
	mustReject(t, spoil(t, func(event map[string]any) {
		event["event_id"] = "not-a-uuid"
	}), "invalid_uuid")
}

// An error carries a code and a path and never the value, because the value is
// the input suspected of holding a secret.
func TestAValidationErrorNeverEchoesTheValue(t *testing.T) {
	credential := "gh" + "p_0123456789abcdefghijklmnopqrstuvwxyz"
	_, err := ValidateEvent(spoil(t, func(event map[string]any) {
		event["producer"].(map[string]any)["version"] = credential
	}))
	if err == nil {
		t.Fatal("credential accepted")
	}
	if strings.Contains(err.Error(), credential) {
		t.Fatalf("error echoed the rejected value: %v", err)
	}
	if !strings.Contains(err.Error(), "$.producer.version") {
		t.Fatalf("error does not name the failing path: %v", err)
	}
}

func TestBatchesAreBoundedAndNeverEmpty(t *testing.T) {
	single := decode(t, turnCompleted)
	events, err := ValidateBatch(single, DefaultMaximumBatch)
	if err != nil || len(events) != 1 {
		t.Fatalf("single event batch: %v, %d events", err, len(events))
	}

	if _, err := ValidateBatch([]any{}, DefaultMaximumBatch); err == nil {
		t.Fatal("an empty batch was accepted")
	}

	oversized := make([]any, 3)
	for index := range oversized {
		oversized[index] = decode(t, turnCompleted)
	}
	if _, err := ValidateBatch(oversized, 2); err == nil {
		t.Fatal("an oversized batch was accepted")
	}

	// One bad event refuses the batch. A partially stored batch would leave the
	// producer unable to tell what was kept.
	mixed := []any{decode(t, turnCompleted), spoil(t, func(event map[string]any) {
		event["event_type"] = "turn.invented"
	})}
	if _, err := ValidateBatch(mixed, DefaultMaximumBatch); err == nil {
		t.Fatal("a batch with an invalid event was accepted")
	}
}

// json.Number is what a decoder configured with UseNumber produces, and the
// ingest server will use one to keep large integers exact.
func TestNumbersDecodedAsJSONNumberAreAccepted(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(turnCompleted))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	event, err := ValidateEvent(value)
	if err != nil {
		t.Fatal(err)
	}
	if event.Attributes["tool_count"] != int64(3) {
		t.Fatalf("tool_count = %#v", event.Attributes["tool_count"])
	}
}

// Every attribute the event table names must have a declared shape. A drift
// between the two tables is how an attribute ends up stored unvalidated.
func TestEveryDeclaredAttributeHasAShape(t *testing.T) {
	for eventType, attributes := range eventAttributes {
		for attribute := range attributes {
			switch {
			case has(numericAttributes, attribute), has(integerAttributes, attribute),
				has(stringAttributes, attribute):
			case attribute == "measurement_quality", attribute == "correlation",
				attribute == "cancellation_reason", attribute == "tool_observation_mode":
			default:
				t.Errorf("%s declares attribute %q with no shape", eventType, attribute)
			}
		}
	}
	for attribute := range commonAttributes {
		if attribute != "measurement_quality" {
			t.Errorf("common attribute %q has no shape", attribute)
		}
	}
}

func TestEventTypeValidRejectsWhatItDoesNotDefine(t *testing.T) {
	if EventType("turn.invented").Valid() || EventType("").Valid() {
		t.Fatal("an undefined event type is valid")
	}
	if !EventTurnCompleted.Valid() {
		t.Fatal("a defined event type is invalid")
	}
}
