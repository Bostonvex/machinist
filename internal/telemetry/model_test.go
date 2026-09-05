package telemetry

import (
	"math"
	"testing"
)

// modelCall builds one completed model call at a given time.
func modelCall(observedAt string, decodeMS, outputTokens float64, edits map[string]any) Event {
	attributes := map[string]any{
		"duration_ms": decodeMS, "decode_ms": decodeMS, "output_tokens": outputTokens,
		"input_tokens": 100.0, "correlation": "exact", "measurement_quality": "exact",
		"http_status": 200.0,
	}
	for key, value := range edits {
		attributes[key] = value
	}
	endpoint := "mac-mini"
	if named, ok := edits["endpoint_id"].(string); ok {
		endpoint = named
		delete(attributes, "endpoint_id")
	}
	return Event{
		EventType: EventModelCompleted, ObservedAt: observedAt,
		EndpointID: &endpoint, Attributes: attributes,
	}
}

func TestThroughputIsTokensOverTimeAndNotTheMeanOfRates(t *testing.T) {
	// One fast tiny call and one slow large one. The mean of the per-call rates
	// weights them equally; the endpoint did not.
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:00.000Z", 1000, 10, nil),
		modelCall("2026-09-05T12:00:10.000Z", 1000, 1000, nil),
	})
	if metrics.OutputTokensPerSecond == nil {
		t.Fatal("no throughput was reported for two measured calls")
	}
	if want := 1010.0 / 2.0; math.Abs(*metrics.OutputTokensPerSecond-want) > 1e-9 {
		t.Fatalf("throughput = %v, want %v (tokens over decode time)", *metrics.OutputTokensPerSecond, want)
	}
}

func TestAnEstimatedCallIsNotDividedIntoARate(t *testing.T) {
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:00.000Z", 1000, 500, map[string]any{"measurement_quality": "estimated"}),
	})
	if metrics.ExactCallCount != 0 {
		t.Fatalf("exact calls = %d, want 0: the producer estimated this one", metrics.ExactCallCount)
	}
	if metrics.OutputTokensPerSecond != nil {
		t.Fatalf("throughput = %v, want nothing: it would be derived from a guess", *metrics.OutputTokensPerSecond)
	}
	if metrics.CorrelationCounts["exact"] != 1 {
		t.Fatalf("correlation counts = %v: an estimated call still happened", metrics.CorrelationCounts)
	}
}

func TestACallThatCouldNotBeTiedToATurnIsStillCounted(t *testing.T) {
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:00.000Z", 1000, 100, map[string]any{"correlation": "unavailable"}),
	})
	if metrics.ExactCallCount != 1 {
		t.Fatalf("exact calls = %d, want 1", metrics.ExactCallCount)
	}
	if metrics.AttributedExactCallCount != 0 {
		t.Fatalf("attributed calls = %d, want 0", metrics.AttributedExactCallCount)
	}
	if metrics.CorrelationCounts["unavailable"] != 1 {
		t.Fatalf("correlation counts = %v, want the uncorrelated call counted", metrics.CorrelationCounts)
	}
}

func TestTwoEndpointsServingOneCallEachAreNotOneEndpointServingTwo(t *testing.T) {
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:01.000Z", 1000, 100, map[string]any{"endpoint_id": "one"}),
		modelCall("2026-09-05T12:00:01.000Z", 1000, 100, map[string]any{"endpoint_id": "two"}),
	})
	for _, band := range metrics.DecodeConcurrencyBands {
		if band.Band == "1" && band.CallCount != 2 {
			t.Fatalf("band 1 holds %d calls, want both: concurrency is per endpoint", band.CallCount)
		}
		if band.Band == "2" && band.CallCount != 0 {
			t.Fatalf("band 2 holds %d calls, want none", band.CallCount)
		}
	}
}

func TestOverlappingCallsOnOneEndpointLandInTheConcurrentBand(t *testing.T) {
	// Both decode across 12:00:00-12:00:02, so each is alongside the other at
	// its own midpoint.
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:02.000Z", 2000, 100, nil),
		modelCall("2026-09-05T12:00:02.000Z", 2000, 100, nil),
	})
	for _, band := range metrics.DecodeConcurrencyBands {
		if band.Band != "2" {
			continue
		}
		if band.CallCount != 2 {
			t.Fatalf("band 2 holds %d calls, want 2", band.CallCount)
		}
		if band.AverageConcurrency == nil || *band.AverageConcurrency != 2 {
			t.Fatalf("average concurrency = %v, want 2", band.AverageConcurrency)
		}
		return
	}
	t.Fatal("band 2 was not reported at all")
}

func TestACallIsMeasuredWhereItSpentItsTimeAndNotWhereItStarted(t *testing.T) {
	// The long call starts alone at 12:00:00 and runs to 12:00:10. The short
	// one runs 12:00:08-12:00:10. At the long call's midpoint (12:00:05) it is
	// still alone, so measuring at the start and at the midpoint agree here;
	// the short call, which began in company, must not be counted as alone.
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:10.000Z", 10000, 1000, nil),
		modelCall("2026-09-05T12:00:10.000Z", 2000, 200, nil),
	})
	counts := map[string]int{}
	for _, band := range metrics.DecodeConcurrencyBands {
		counts[band.Band] = band.CallCount
	}
	if counts["1"] != 1 || counts["2"] != 1 {
		t.Fatalf("bands = %v, want one call alone and one alongside", counts)
	}
}

func TestACallWithNoUsableDecodeTimeIsLeftOutOfEveryBand(t *testing.T) {
	metrics := ModelMetricsFrom([]Event{
		modelCall("2026-09-05T12:00:00.000Z", 0, 100, nil),
	})
	if metrics.ExactCallCount != 0 {
		t.Fatalf("exact calls = %d, want 0: a zero decode time is not a measurement", metrics.ExactCallCount)
	}
	var banded int
	for _, band := range metrics.DecodeConcurrencyBands {
		banded += band.CallCount
	}
	if banded != 0 {
		t.Fatalf("bands hold %d calls, want none", banded)
	}
}

func TestTheBandsHoldEveryMeasuredCallExactlyOnce(t *testing.T) {
	var events []Event
	for _, at := range []string{"12:00:01", "12:00:02", "12:00:03", "12:00:04"} {
		events = append(events, modelCall("2026-09-05T"+at+".000Z", 1000, 100, nil))
	}
	metrics := ModelMetricsFrom(events)
	var banded int
	for _, band := range metrics.DecodeConcurrencyBands {
		banded += band.CallCount
	}
	if banded != metrics.ExactCallCount {
		t.Fatalf("bands hold %d calls but %d were measured: the bands must partition them",
			banded, metrics.ExactCallCount)
	}
}

func TestAFailedCallIsCountedAsACallAndNotAsThroughput(t *testing.T) {
	failed := Event{
		EventType: EventModelFailed, ObservedAt: "2026-09-05T12:00:00.000Z",
		Attributes: map[string]any{"correlation": "exact", "error_category": "transient"},
	}
	metrics := ModelMetricsFrom([]Event{failed})
	if metrics.FailedCount != 1 {
		t.Fatalf("failed calls = %d, want 1", metrics.FailedCount)
	}
	if metrics.OutputTokensPerSecond != nil {
		t.Fatal("a failed call contributed throughput")
	}
}
