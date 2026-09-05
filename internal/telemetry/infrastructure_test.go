package telemetry

import (
	"context"
	"testing"
)

// sampleAt inserts one hardware sample.
func sampleAt(t *testing.T, store *Store, name, at, metric string, value float64, edits map[string]any) {
	t.Helper()
	attributes := map[string]any{
		"metric_name": metric, "value": value, "unit": "count",
		"provider_id": "vllm", "node_id": "dgx-spark", "measurement_quality": "exact",
	}
	for key, item := range edits {
		attributes[key] = item
	}
	insert(t, store, event(t, name, EventHardwareSample, map[string]any{
		"observed_at": at, "turn_id": nil, "session_id": nil,
		"agent":      map[string]any{"id": "shared-infrastructure", "display_name": "Shared infrastructure"},
		"attributes": attributes,
	}))
}

func infrastructure(t *testing.T, store *Store, filter Filter) InfrastructureMetrics {
	t.Helper()
	metrics, err := store.InfrastructureMetricsIn(context.Background(), filter)
	if err != nil {
		t.Fatalf("infrastructure metrics: %v", err)
	}
	return metrics
}

func TestACounterIsReportedAsARateAndNotAsATotal(t *testing.T) {
	store := openTestStore(t)
	sampleAt(t, store, "g1", "2026-09-05T12:00:00.000Z", "generation_tokens_total", 1000, nil)
	sampleAt(t, store, "g2", "2026-09-05T12:00:10.000Z", "generation_tokens_total", 2000, nil)

	metrics := infrastructure(t, store, Filter{})
	if len(metrics.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(metrics.Series))
	}
	series := metrics.Series[0]
	if series.RatePerSecond == nil || *series.RatePerSecond != 100 {
		t.Fatalf("rate = %v, want 100 tokens per second", series.RatePerSecond)
	}
	if metrics.GenerationTokensPerSecond == nil || *metrics.GenerationTokensPerSecond != 100 {
		t.Fatalf("fleet generation rate = %v, want 100", metrics.GenerationTokensPerSecond)
	}
}

func TestARestartedCounterDoesNotReportNegativeWork(t *testing.T) {
	store := openTestStore(t)
	sampleAt(t, store, "r1", "2026-09-05T12:00:00.000Z", "generation_tokens_total", 1000, nil)
	sampleAt(t, store, "r2", "2026-09-05T12:00:10.000Z", "generation_tokens_total", 200, nil)
	sampleAt(t, store, "r3", "2026-09-05T12:00:20.000Z", "generation_tokens_total", 400, nil)

	series := infrastructure(t, store, Filter{}).Series[0]
	if series.CounterResets == nil || *series.CounterResets != 1 {
		t.Fatalf("counter resets = %v, want 1", series.CounterResets)
	}
	// Only the 200 the counter climbed after the restart is progress. The drop
	// is not negative work, and the post-restart value is not work done in one
	// interval.
	if series.RatePerSecond == nil || *series.RatePerSecond != 10 {
		t.Fatalf("rate = %v, want 10: only forward steps are work", series.RatePerSecond)
	}
}

func TestTwoNodesReportingTheSameMetricAreTwoSeries(t *testing.T) {
	store := openTestStore(t)
	sampleAt(t, store, "n1", "2026-09-05T12:00:00.000Z", "gpu_utilization", 10,
		map[string]any{"node_id": "dgx-0", "unit": "percent"})
	sampleAt(t, store, "n2", "2026-09-05T12:00:00.000Z", "gpu_utilization", 90,
		map[string]any{"node_id": "dgx-1", "unit": "percent"})

	metrics := infrastructure(t, store, Filter{})
	if len(metrics.Series) != 2 {
		t.Fatalf("series = %d, want 2: two machines must not be averaged into one", len(metrics.Series))
	}
	for _, series := range metrics.Series {
		if series.Mean == 50 {
			t.Fatal("two nodes were averaged into a number describing neither")
		}
	}
}

func TestAGaugeIsNotGivenARate(t *testing.T) {
	store := openTestStore(t)
	sampleAt(t, store, "u1", "2026-09-05T12:00:00.000Z", "gpu_utilization", 40,
		map[string]any{"unit": "percent"})
	sampleAt(t, store, "u2", "2026-09-05T12:00:10.000Z", "gpu_utilization", 60,
		map[string]any{"unit": "percent"})

	series := infrastructure(t, store, Filter{}).Series[0]
	if series.RatePerSecond != nil || series.CounterResets != nil {
		t.Fatalf("a gauge was reported as a counter: %+v", series)
	}
	if series.Mean != 50 || series.Minimum != 40 || series.Maximum != 60 || series.Latest != 60 {
		t.Fatalf("gauge summary is wrong: %+v", series)
	}
}

func TestASampleOutsideTheWindowIsNotInTheRate(t *testing.T) {
	store := openTestStore(t)
	sampleAt(t, store, "w1", "2026-09-05T11:00:00.000Z", "generation_tokens_total", 0, nil)
	sampleAt(t, store, "w2", "2026-09-05T12:00:00.000Z", "generation_tokens_total", 1000, nil)
	sampleAt(t, store, "w3", "2026-09-05T12:00:10.000Z", "generation_tokens_total", 2000, nil)

	metrics := infrastructure(t, store, Filter{Since: "2026-09-05T12:00:00Z"})
	if metrics.SampleCount != 2 {
		t.Fatalf("sample count = %d, want 2", metrics.SampleCount)
	}
	if rate := metrics.Series[0].RatePerSecond; rate == nil || *rate != 100 {
		t.Fatalf("rate = %v, want 100", rate)
	}
}

func TestAnEmptyWindowIsAnEmptyListAndNotNothing(t *testing.T) {
	metrics := infrastructure(t, openTestStore(t), Filter{})
	if metrics.Series == nil {
		t.Fatal("an empty window returned no list at all")
	}
	if metrics.GenerationTokensPerSecond != nil {
		t.Fatal("an empty window reported a generation rate")
	}
}

func TestAnUnreadableFilterFailsTheAggregationRatherThanWideningIt(t *testing.T) {
	if _, err := openTestStore(t).InfrastructureMetricsIn(context.Background(),
		Filter{Since: "yesterday"}); err == nil {
		t.Fatal("an unreadable window was silently ignored")
	}
}
