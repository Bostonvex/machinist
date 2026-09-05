package telemetry

import (
	"math"
	"testing"
)

func value(t *testing.T, pointer *float64) float64 {
	t.Helper()
	if pointer == nil {
		t.Fatal("expected a number, got nothing")
	}
	return *pointer
}

// near compares to within rounding. It is not called close, because shadowing
// the builtin in a package whose tests open and close things is a trap.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNothingMeasuredIsNotZero(t *testing.T) {
	// Reporting zero would put a number on a chart that no measurement
	// produced. A dashboard can draw a gap; it cannot un-draw a zero.
	summary := summarize(nil)
	if summary.Count != 0 {
		t.Fatalf("count was %d", summary.Count)
	}
	for name, pointer := range map[string]*float64{
		"mean": summary.Mean, "p50": summary.P50, "p95": summary.P95,
		"minimum": summary.Minimum, "maximum": summary.Maximum,
	} {
		if pointer != nil {
			t.Fatalf("%s was reported as %v with nothing to measure", name, *pointer)
		}
	}
}

func TestAPercentileInterpolatesBetweenTheNearestReadings(t *testing.T) {
	// The definition is pinned because two implementations of "p95" that round
	// differently give two numbers from the same data, and a dashboard
	// comparing them reports a change that never happened. These are the values
	// the Python collector produced for the same input.
	readings := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	summary := summarize(readings)
	for name, expected := range map[string]struct {
		got  *float64
		want float64
	}{
		"p05":     {summary.P05, 1.45},
		"p50":     {summary.P50, 5.5},
		"p95":     {summary.P95, 9.55},
		"mean":    {summary.Mean, 5.5},
		"minimum": {summary.Minimum, 1},
		"maximum": {summary.Maximum, 10},
	} {
		if got := value(t, expected.got); !near(got, expected.want) {
			t.Errorf("%s was %v, want %v", name, got, expected.want)
		}
	}
}

func TestOneReadingIsItsOwnPercentile(t *testing.T) {
	summary := summarize([]float64{7})
	if got := value(t, summary.P95); got != 7 {
		t.Fatalf("p95 of one reading was %v", got)
	}
}

func TestSummarizingDoesNotReorderTheCallersRows(t *testing.T) {
	// The slice a caller passes is often the order a query returned, and
	// reordering it under them changes what a later pass over the same rows
	// means.
	readings := []float64{3, 1, 2}
	summarize(readings)
	if readings[0] != 3 || readings[1] != 1 || readings[2] != 2 {
		t.Fatalf("the caller's slice was sorted: %v", readings)
	}
}

func TestAMetricCountsTheQualityOfWhatItMeasured(t *testing.T) {
	// A p95 over turns a harness could only estimate is a different claim from
	// one over measured turns, and a reader given the number alone cannot tell
	// which they are looking at.
	one, two, three := 1.0, 2.0, 3.0
	metric := metricFrom([]reading{
		{&one, "exact"}, {&two, "exact"}, {&three, "estimated"}, {nil, "exact"},
	})
	if metric.Count != 3 {
		t.Fatalf("count was %d; a reading that was never taken was counted", metric.Count)
	}
	if metric.QualityCounts["exact"] != 2 || metric.QualityCounts["estimated"] != 1 {
		t.Fatalf("quality counts were %v", metric.QualityCounts)
	}
}

func TestAReadingWithNoDeclaredQualityIsCountedAsUnavailable(t *testing.T) {
	// Counting only the turns that declared one would make the coverage figure
	// describe the turns that answered rather than the fleet.
	one := 1.0
	metric := metricFrom([]reading{{&one, ""}})
	if metric.QualityCounts["unavailable"] != 1 {
		t.Fatalf("quality counts were %v", metric.QualityCounts)
	}
}

func TestAnUnmeasurableReadingIsNotAveragedIn(t *testing.T) {
	// A NaN in the column poisons every aggregate computed from it, and the
	// mean of a set containing one is NaN however many good readings surround
	// it.
	one, bad := 1.0, math.NaN()
	metric := metricFrom([]reading{{&one, "exact"}, {&bad, "exact"}})
	if metric.Count != 1 {
		t.Fatalf("count was %d", metric.Count)
	}
	if got := value(t, metric.Mean); got != 1 {
		t.Fatalf("mean was %v", got)
	}
}

func TestAMedianAveragesTheTwoMiddleReadings(t *testing.T) {
	one, two, three, four := 1.0, 2.0, 3.0, 4.0
	metric := metricFrom([]reading{{&one, ""}, {&two, ""}, {&three, ""}, {&four, ""}})
	if got := value(t, metric.Median); got != 2.5 {
		t.Fatalf("median was %v", got)
	}
}

func TestARateOverNoTimeIsNotInfinity(t *testing.T) {
	// It is a rate nobody has enough information to state, and infinity on a
	// tokens-per-second chart reads as a record throughput.
	if rate(100, 0) != nil {
		t.Fatal("a rate over zero elapsed time was reported")
	}
	if rate(100, -1) != nil {
		t.Fatal("a rate over negative time was reported")
	}
	if got := rate(100, 2); got == nil || *got != 50 {
		t.Fatalf("a normal rate was %v", got)
	}
}

func TestAShareOfNothingIsNotZeroPercent(t *testing.T) {
	// A success rate over no terminal turns is not zero; nothing has finished.
	if share(0, 0) != nil {
		t.Fatal("a share of an empty set was reported as a number")
	}
	if got := share(1, 4); got == nil || *got != 0.25 {
		t.Fatalf("a normal share was %v", got)
	}
}
