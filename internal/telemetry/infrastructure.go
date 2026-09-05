package telemetry

import (
	"context"
	"fmt"
	"time"
)

// maximumSampleWindow bounds the samples one aggregation reads. Past this the
// query stops describing a dashboard window and starts being an export, and it
// holds a read connection open behind every other reader while it runs.
const maximumSampleWindow = 5000

// InfrastructureMetrics is the shared hardware and server state over a window.
type InfrastructureMetrics struct {
	Series                    []Series `json:"series"`
	SampleCount               int      `json:"sample_count"`
	GenerationTokensPerSecond *float64 `json:"generation_tokens_per_second"`
}

// Series is one metric from one source over the window.
type Series struct {
	// Source is "server" or "hardware": what the metric describes, not who it
	// belongs to. It is deliberately not called scope — a listed sample's scope
	// is "shared_context", which answers the different question of whether the
	// reading can be attributed to an agent, and one name for two axes is how a
	// reader ends up filtering on the wrong one.
	Source             string   `json:"source"`
	EndpointID         *string  `json:"endpoint_id"`
	ProviderID         *string  `json:"provider_id"`
	NodeID             *string  `json:"node_id"`
	MetricName         string   `json:"metric_name"`
	Unit               *string  `json:"unit"`
	SampleCount        int      `json:"sample_count"`
	Mean               float64  `json:"mean"`
	Minimum            float64  `json:"minimum"`
	Maximum            float64  `json:"maximum"`
	Latest             float64  `json:"latest"`
	LatestAt           string   `json:"latest_at"`
	MeasurementQuality string   `json:"measurement_quality"`
	RatePerSecond      *float64 `json:"rate_per_second"`
	CounterResets      *int     `json:"counter_resets"`
}

// counterMetrics are the ones that only go up. A counter's value is meaningless
// on its own — a total of nine million tokens says nothing about now — so these
// are reported as a rate over the window instead.
var counterMetrics = map[string]bool{
	"prompt_tokens_total":       true,
	"generation_tokens_total":   true,
	"successful_requests_total": true,
	"preemptions_total":         true,
}

// seriesKey is the identity of one series. All six fields are part of it: two
// nodes reporting gpu.0.utilization_percent are two series, and merging them
// would average two machines into one number that describes neither.
type seriesKey struct {
	eventType, endpointID, providerID, nodeID, metricName, unit string
}

type seriesAccumulator struct {
	key                             seriesKey
	order                           int
	count                           int
	total, minimum, maximum, latest float64
	latestAt                        string
	quality                         string
	firstSeconds, lastSeconds       float64
	previous                        float64
	hasPrevious                     bool
	positiveDelta                   float64
	resets                          int
}

// InfrastructureMetricsIn aggregates the samples in a window.
//
// Only the window and the endpoint narrow it, matching ListSamples: shared
// hardware cannot be filtered by agent without answering a question the data
// does not support.
func (s *Store) InfrastructureMetricsIn(ctx context.Context, filter Filter) (InfrastructureMetrics, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return InfrastructureMetrics{}, err
	}
	built := &conditions{}
	built.addIfSet("observed_at >= ?", filter.Since)
	built.addIfSet("observed_at <= ?", filter.Until)
	built.addIfSet("endpoint_id = ?", filter.EndpointID)
	where, values := built.where()

	// Ordered oldest first, because a counter's rate is the sum of its forward
	// steps and reading the series backwards would report every step as a
	// reset. The window is bounded by taking the most recent samples, then
	// walking them in order.
	rows, err := s.read.QueryContext(ctx, `
		SELECT event_type, endpoint_id, provider_id, node_id, metric_name, unit,
		       measurement_quality, value, observed_at
		FROM (SELECT * FROM infrastructure_samples`+where+`
		      ORDER BY observed_at DESC, event_id LIMIT ?)
		ORDER BY observed_at ASC, event_id ASC`,
		append(values, maximumSampleWindow)...)
	if err != nil {
		return InfrastructureMetrics{}, fmt.Errorf("read samples: %w", err)
	}
	defer rows.Close()

	accumulators := map[seriesKey]*seriesAccumulator{}
	var order []seriesKey
	for rows.Next() {
		var key seriesKey
		var endpoint, provider, node, unit, quality *string
		var value float64
		var observedAt string
		if err := rows.Scan(&key.eventType, &endpoint, &provider, &node, &key.metricName,
			&unit, &quality, &value, &observedAt); err != nil {
			return InfrastructureMetrics{}, fmt.Errorf("read sample: %w", err)
		}
		key.endpointID, key.providerID = text(endpoint), text(provider)
		key.nodeID, key.unit = text(node), text(unit)

		seconds, err := epochSeconds(observedAt)
		if err != nil {
			// A sample whose time cannot be read cannot be placed in the
			// window, so it cannot contribute to a rate. Counting it in the
			// mean but not the elapsed time would inflate the rate silently.
			continue
		}

		accumulator, known := accumulators[key]
		if !known {
			accumulator = &seriesAccumulator{
				key: key, order: len(order), minimum: value, maximum: value,
				firstSeconds: seconds,
			}
			accumulators[key] = accumulator
			order = append(order, key)
		}
		accumulator.count++
		accumulator.total += value
		accumulator.minimum = min(accumulator.minimum, value)
		accumulator.maximum = max(accumulator.maximum, value)
		accumulator.lastSeconds = seconds
		accumulator.latest = value
		accumulator.latestAt = observedAt
		accumulator.quality = text(quality)

		if counterMetrics[key.metricName] {
			if accumulator.hasPrevious {
				if value >= accumulator.previous {
					accumulator.positiveDelta += value - accumulator.previous
				} else {
					// A counter that went down was restarted. Counting the drop
					// as negative progress would make a restarted endpoint
					// report negative throughput; counting the new value as a
					// step would report the whole counter as work done in one
					// interval.
					accumulator.resets++
				}
			}
			accumulator.previous = value
			accumulator.hasPrevious = true
		}
	}
	if err := rows.Err(); err != nil {
		return InfrastructureMetrics{}, err
	}

	metrics := InfrastructureMetrics{Series: []Series{}}
	var generationRates []float64
	for _, key := range order {
		accumulator := accumulators[key]
		series := Series{
			Source:      sourceOf(key.eventType),
			EndpointID:  optional(key.endpointID),
			ProviderID:  optional(key.providerID),
			NodeID:      optional(key.nodeID),
			MetricName:  key.metricName,
			Unit:        optional(key.unit),
			SampleCount: accumulator.count,
			Mean:        accumulator.total / float64(accumulator.count),
			Minimum:     accumulator.minimum,
			Maximum:     accumulator.maximum,
			Latest:      accumulator.latest,
			LatestAt:    accumulator.latestAt,
			// A sample that declared no quality is reported as unavailable
			// rather than assumed exact.
			MeasurementQuality: "unavailable",
		}
		if accumulator.quality != "" {
			series.MeasurementQuality = accumulator.quality
		}
		if counterMetrics[key.metricName] {
			series.RatePerSecond = rate(accumulator.positiveDelta, accumulator.lastSeconds-accumulator.firstSeconds)
			resets := accumulator.resets
			series.CounterResets = &resets
			if key.metricName == "generation_tokens_total" && series.RatePerSecond != nil {
				generationRates = append(generationRates, *series.RatePerSecond)
			}
		}
		metrics.SampleCount += accumulator.count
		metrics.Series = append(metrics.Series, series)
	}
	if len(generationRates) > 0 {
		// Summed across endpoints, because the fleet's generation rate is what
		// every endpoint produced together.
		var total float64
		for _, value := range generationRates {
			total += value
		}
		metrics.GenerationTokensPerSecond = &total
	}
	return metrics, nil
}

func sourceOf(eventType string) string {
	if eventType == string(EventServerSample) {
		return "server"
	}
	return "hardware"
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func epochSeconds(observedAt string) (float64, error) {
	parsed, err := time.Parse(timeLayout, observedAt)
	if err != nil {
		return 0, err
	}
	return float64(parsed.UnixNano()) / 1e9, nil
}
