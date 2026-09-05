package telemetry

import (
	"container/heap"
	"sort"
	"time"
)

// ModelMetrics describes the model calls inside a set of turns.
type ModelMetrics struct {
	CallCount                 int            `json:"call_count"`
	CompletedCount            int            `json:"completed_count"`
	FailedCount               int            `json:"failed_count"`
	ExactCallCount            int            `json:"exact_call_count"`
	AttributedExactCallCount  int            `json:"attributed_exact_call_count"`
	TTFTMS                    Summary        `json:"ttft_ms"`
	InputTokens               Summary        `json:"input_tokens"`
	PerCallOutputTokensPerSec Summary        `json:"per_call_output_tokens_per_second"`
	DecodeConcurrencyBands    []Band         `json:"decode_concurrency_bands"`
	ExactOutputTokens         int            `json:"exact_output_tokens"`
	ExactDecodeMS             *float64       `json:"exact_decode_ms"`
	OutputTokensPerSecond     *float64       `json:"output_tokens_per_second"`
	CorrelationCounts         map[string]int `json:"correlation_counts"`
	Limited                   bool           `json:"limited"`
}

// Band is throughput at one level of concurrent decoding.
//
// The bands exist because throughput per call falls as an endpoint serves more
// requests at once. Reporting one average over all of them describes a load
// that never occurred, and an operator sizing a machine from it plans for
// throughput the endpoint only reaches when it is nearly idle.
type Band struct {
	Band                      string   `json:"band"`
	CallCount                 int      `json:"call_count"`
	AverageConcurrency        *float64 `json:"average_concurrency"`
	OutputTokensPerSecond     *float64 `json:"output_tokens_per_second"`
	PerCallP50TokensPerSecond *float64 `json:"per_call_p50_tokens_per_second"`
}

// decodeInterval is one completed call placed on a timeline.
type decodeInterval struct {
	endpoint        string
	startMS         float64
	endMS           float64
	midpointMS      float64
	decodeMS        float64
	outputTokens    float64
	tokensPerSecond float64
	concurrency     float64
}

// ModelMetricsFrom reduces model events to throughput and latency figures.
//
// Only calls the producer measured exactly contribute to throughput. A decode
// time the harness estimated cannot be divided into a token count to get a
// rate: the result would be a tokens-per-second figure derived from a guess,
// and it would sit on the same axis as the measured ones.
func ModelMetricsFrom(events []Event) ModelMetrics {
	metrics := ModelMetrics{CorrelationCounts: map[string]int{}}

	var ttft, inputTokens, perCallRates []float64
	var exact []decodeInterval
	var decodeMS float64

	for _, event := range events {
		switch event.EventType {
		case EventModelRequestStarted:
			metrics.CallCount++
		case EventModelFirstToken:
			if elapsed, ok := attributeNumber(event.Attributes, "elapsed_ms"); ok {
				ttft = append(ttft, elapsed)
			}
		case EventModelCompleted:
			metrics.CompletedCount++
		case EventModelFailed:
			metrics.FailedCount++
		}

		if event.EventType != EventModelCompleted && event.EventType != EventModelFailed {
			continue
		}
		// Correlation says whether the producer could tie this call to the turn
		// it belongs to. It is counted over every terminal call, including the
		// ones that could not be tied, because the proportion is the point.
		correlation := "unavailable"
		if value, ok := event.Attributes["correlation"].(string); ok && value != "" {
			correlation = value
		}
		metrics.CorrelationCounts[correlation]++

		if event.EventType != EventModelCompleted || !isExact(event.Attributes) {
			continue
		}
		if tokens, ok := attributeNumber(event.Attributes, "input_tokens"); ok {
			inputTokens = append(inputTokens, tokens)
		}
		decode, hasDecode := attributeNumber(event.Attributes, "decode_ms")
		output, hasOutput := attributeNumber(event.Attributes, "output_tokens")
		if !hasDecode || !hasOutput || decode <= 0 {
			continue
		}
		observed, err := time.Parse(timeLayout, event.ObservedAt)
		if err != nil {
			// Without a completion time the call cannot be placed on the
			// timeline, so it cannot be assigned a concurrency. Including it in
			// the totals but not in any band would make the bands sum to less
			// than the whole and give no reason why.
			continue
		}
		completedMS := float64(observed.UnixNano()) / 1e6
		perSecond := output / (decode / 1000)
		exact = append(exact, decodeInterval{
			endpoint: stringOrUnknown(event.EndpointID), startMS: completedMS - decode,
			endMS: completedMS, midpointMS: completedMS - decode/2, decodeMS: decode,
			outputTokens: output, tokensPerSecond: perSecond,
		})
		perCallRates = append(perCallRates, perSecond)
		decodeMS += decode
		metrics.ExactOutputTokens += int(output)
		if correlation == "exact" {
			metrics.AttributedExactCallCount++
		}
	}

	metrics.ExactCallCount = len(exact)
	metrics.TTFTMS = summarize(ttft)
	metrics.InputTokens = summarize(inputTokens)
	metrics.PerCallOutputTokensPerSec = summarize(perCallRates)
	if decodeMS > 0 {
		metrics.ExactDecodeMS = &decodeMS
	}
	// Total throughput is tokens over decode time rather than the mean of the
	// per-call rates. The mean weights a call that produced three tokens the
	// same as one that produced three thousand, which is not what an endpoint's
	// throughput means.
	metrics.OutputTokensPerSecond = rate(float64(metrics.ExactOutputTokens), decodeMS/1000)
	metrics.DecodeConcurrencyBands = concurrencyBands(exact)
	return metrics
}

// isExact reports whether the producer measured this call rather than estimating
// it.
func isExact(attributes map[string]any) bool {
	quality, ok := attributes["measurement_quality"].(string)
	return ok && quality == "exact"
}

// attributeNumber reads a numeric attribute. JSON has one number type, so an
// integer count and a millisecond duration arrive the same way.
func attributeNumber(attributes map[string]any, key string) (float64, bool) {
	value, ok := attributes[key].(float64)
	return value, ok
}

func stringOrUnknown(value *string) string {
	if value == nil || *value == "" {
		return "unknown"
	}
	return *value
}

// bandDefinitions are the concurrency levels reported, as closed ranges. The
// last is open-ended: past eight concurrent decodes the differences between
// individual levels matter less than the fact that the endpoint is saturated.
var bandDefinitions = []struct {
	label            string
	minimum, maximum float64
}{
	{"1", 1, 1}, {"2", 2, 2}, {"3-4", 3, 4}, {"5-8", 5, 8}, {"9+", 9, 0},
}

// endsHeap orders the in-flight calls by when they finish, so the sweep can
// retire the earliest without rescanning the rest.
type endsHeap []float64

func (h endsHeap) Len() int           { return len(h) }
func (h endsHeap) Less(a, b int) bool { return h[a] < h[b] }
func (h endsHeap) Swap(a, b int)      { h[a], h[b] = h[b], h[a] }
func (h *endsHeap) Push(value any)    { *h = append(*h, value.(float64)) }
func (h *endsHeap) Pop() any          { old := *h; last := old[len(old)-1]; *h = old[:len(old)-1]; return last }

// concurrencyBands measures how many calls each call was decoding alongside,
// then reports throughput at each level.
//
// Concurrency is counted per endpoint. Two endpoints each serving one request
// are not one endpoint serving two, and pooling them would report a level of
// contention that no machine experienced.
//
// Each call is measured at its own midpoint rather than at its start. A call
// that begins alone and finishes in a crowd was not decoding alone; the
// midpoint is the level it spent most of its time at.
func concurrencyBands(intervals []decodeInterval) []Band {
	byEndpoint := map[string][]int{}
	for index, interval := range intervals {
		byEndpoint[interval.endpoint] = append(byEndpoint[interval.endpoint], index)
	}
	for _, indexes := range byEndpoint {
		starts := append([]int(nil), indexes...)
		sort.Slice(starts, func(a, b int) bool {
			return intervals[starts[a]].startMS < intervals[starts[b]].startMS
		})
		midpoints := append([]int(nil), indexes...)
		sort.Slice(midpoints, func(a, b int) bool {
			return intervals[midpoints[a]].midpointMS < intervals[midpoints[b]].midpointMS
		})

		active := &endsHeap{}
		next := 0
		for _, index := range midpoints {
			midpoint := intervals[index].midpointMS
			for next < len(starts) && intervals[starts[next]].startMS <= midpoint {
				heap.Push(active, intervals[starts[next]].endMS)
				next++
			}
			for active.Len() > 0 && (*active)[0] < midpoint {
				heap.Pop(active)
			}
			intervals[index].concurrency = float64(active.Len())
		}
	}

	bands := make([]Band, 0, len(bandDefinitions))
	for _, definition := range bandDefinitions {
		var matching []decodeInterval
		for _, interval := range intervals {
			if interval.concurrency < definition.minimum {
				continue
			}
			if definition.maximum > 0 && interval.concurrency > definition.maximum {
				continue
			}
			matching = append(matching, interval)
		}
		band := Band{Band: definition.label, CallCount: len(matching)}
		if len(matching) > 0 {
			var decodeMS, tokens, concurrency float64
			rates := make([]float64, 0, len(matching))
			for _, interval := range matching {
				decodeMS += interval.decodeMS
				tokens += interval.outputTokens
				concurrency += interval.concurrency
				rates = append(rates, interval.tokensPerSecond)
			}
			average := concurrency / float64(len(matching))
			band.AverageConcurrency = &average
			band.OutputTokensPerSecond = rate(tokens, decodeMS/1000)
			band.PerCallP50TokensPerSecond = percentile(sortedFloat(rates), 0.50)
		}
		bands = append(bands, band)
	}
	return bands
}
