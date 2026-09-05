package telemetry

import (
	"math"
	"slices"
)

// Summary describes a set of readings.
//
// Every aggregate is a pointer because a set with nothing in it has no mean,
// and reporting zero would put a number on a chart that no measurement
// produced. A dashboard can draw a gap; it cannot un-draw a zero.
type Summary struct {
	Count   int      `json:"count"`
	Mean    *float64 `json:"mean"`
	P05     *float64 `json:"p05"`
	P50     *float64 `json:"p50"`
	P95     *float64 `json:"p95"`
	Minimum *float64 `json:"minimum"`
	Maximum *float64 `json:"maximum"`
}

// Metric is a Summary plus what is known about how the readings were taken.
//
// The quality counts travel with the numbers rather than beside them. A p95
// computed from turns the harness could only estimate is a different claim from
// one computed from measured turns, and a reader given the number alone has no
// way to tell which they are looking at.
type Metric struct {
	Summary
	Median        *float64       `json:"median"`
	QualityCounts map[string]int `json:"quality_counts"`
}

// percentile is a linear interpolation between the two nearest readings.
//
// The definition is fixed here because two implementations of "p95" that round
// differently produce two different numbers from the same data, and a dashboard
// comparing them reports a change that never happened.
func percentile(ordered []float64, fraction float64) *float64 {
	if len(ordered) == 0 {
		return nil
	}
	position := float64(len(ordered)-1) * fraction
	lower := int(position)
	upper := min(lower+1, len(ordered)-1)
	weight := position - float64(lower)
	value := ordered[lower]*(1-weight) + ordered[upper]*weight
	return &value
}

// summarize reduces readings to a Summary. It sorts a copy: the caller's slice
// is often the column order a query returned, and reordering it under them
// would change what a later pass over the same rows means.
func summarize(values []float64) Summary {
	summary := Summary{Count: len(values)}
	if len(values) == 0 {
		return summary
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)

	var total float64
	for _, value := range ordered {
		total += value
	}
	mean := total / float64(len(ordered))
	summary.Mean = &mean
	summary.P05 = percentile(ordered, 0.05)
	summary.P50 = percentile(ordered, 0.50)
	summary.P95 = percentile(ordered, 0.95)
	summary.Minimum = &ordered[0]
	summary.Maximum = &ordered[len(ordered)-1]
	return summary
}

// median is the middle reading, averaging the two middle ones for an even
// count. It is reported alongside p50 because they are the same number only for
// this definition of p50, and a reader comparing them is entitled to see that.
func median(ordered []float64) *float64 {
	if len(ordered) == 0 {
		return nil
	}
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return &ordered[middle]
	}
	value := (ordered[middle-1] + ordered[middle]) / 2
	return &value
}

// reading is one nullable measurement and the quality it was taken at. A turn
// that never reported a metric is not a turn that reported zero, so the two are
// kept apart all the way to the aggregate.
type reading struct {
	value   *float64
	quality string
}

// metricFrom aggregates readings, ignoring the ones that were never taken.
func metricFrom(readings []reading) Metric {
	values := make([]float64, 0, len(readings))
	qualities := map[string]int{}
	for _, item := range readings {
		if item.value == nil || math.IsNaN(*item.value) || math.IsInf(*item.value, 0) {
			continue
		}
		values = append(values, *item.value)
		quality := item.quality
		if quality == "" {
			// A missing quality is recorded as unavailable rather than dropped.
			// Counting only the turns that declared one would make the coverage
			// figure describe the turns that answered rather than the fleet.
			quality = "unavailable"
		}
		qualities[quality]++
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return Metric{Summary: summarize(values), Median: median(ordered), QualityCounts: qualities}
}

// rate divides safely. A rate over no elapsed time is not infinity; it is a
// rate nobody has enough information to state.
func rate(quantity, seconds float64) *float64 {
	if seconds <= 0 || math.IsNaN(quantity) || math.IsInf(quantity, 0) {
		return nil
	}
	value := quantity / seconds
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// share is a proportion of a total, or nil when the total is zero. A success
// rate over no terminal turns is not zero percent; nothing has finished yet.
func share(part, total int) *float64 {
	if total == 0 {
		return nil
	}
	value := float64(part) / float64(total)
	return &value
}
