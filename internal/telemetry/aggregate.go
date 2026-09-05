package telemetry

import (
	"slices"
	"sort"
)

// Aggregate describes a set of turns.
type Aggregate struct {
	TurnCount           int               `json:"turn_count"`
	ActiveTurns         int               `json:"active_turns"`
	Outcomes            map[string]int    `json:"outcomes"`
	CancellationReasons map[string]int    `json:"cancellation_reasons"`
	SuccessRate         *float64          `json:"success_rate"`
	FailureRate         *float64          `json:"failure_rate"`
	CancellationRate    *float64          `json:"cancellation_rate"`
	ToolObservation     ToolObservation   `json:"tool_observation"`
	Metrics             map[string]Metric `json:"metrics"`
}

// ToolObservation says how much of the fleet could report tool use at all.
//
// It is reported beside the tool counts rather than folded into them. A harness
// that cannot observe tools contributes zero uses, and without the coverage
// figure a fleet where half the harnesses are blind is indistinguishable from
// one where half the agents used no tools.
type ToolObservation struct {
	ObservedTurns    int      `json:"observed_turns"`
	UnavailableTurns int      `json:"unavailable_turns"`
	Coverage         *float64 `json:"coverage"`
	ToolUses         int      `json:"tool_uses"`
	TurnsWithTools   int      `json:"turns_with_tools"`
}

// activeOutcome is the bucket a turn that has not ended sits in. It is not one
// of the stored outcomes — the column is null — so it is named here and
// deliberately kept out of terminalOutcomes, which a query may ask for.
const activeOutcome = "active"

// turnMetrics are the per-turn measurements an aggregate reports. The names are
// the column names, so a metric that exists in the schema and not here is a
// metric no reader can see.
var turnMetrics = []string{"ttfa_ms", "ttfvt_ms", "first_tool_ms", "duration_ms", "max_stall_ms"}

func (t TurnRow) metric(name string) *float64 {
	switch name {
	case "ttfa_ms":
		return t.TTFAMS
	case "ttfvt_ms":
		return t.TTFVTMS
	case "first_tool_ms":
		return t.FirstToolMS
	case "duration_ms":
		return t.DurationMS
	case "max_stall_ms":
		return t.MaxStallMS
	}
	return nil
}

// observedTools reports whether the harness could see this turn's tool use.
//
// A turn that has not ended yet is excluded from both sides of the coverage
// figure. Counting it as unobserved would make a busy fleet look blind while
// the work was in flight, and counting it as observed would credit an
// observation nothing has made.
func (t TurnRow) observedTools() (counted, observed bool) {
	if t.Outcome == nil {
		return false, false
	}
	if t.ToolObservationMode == nil || *t.ToolObservationMode == "unavailable" {
		return true, false
	}
	return true, true
}

// aggregate reduces turns to the numbers a dashboard shows.
func aggregate(turns []TurnRow) Aggregate {
	// Every outcome is present from the start, including the ones that are
	// zero. A map that only held the outcomes that occurred would make "absent"
	// mean both "none happened" and "this build does not know about it", and a
	// reader cannot tell those apart.
	//
	// The keys are read from the table that assigns outcomes rather than listed
	// again here, so a new terminal event cannot become an outcome the store
	// records and this aggregate reports as absent.
	outcomes := map[string]int{activeOutcome: 0}
	for _, outcome := range terminalOutcomes {
		outcomes[outcome] = 0
	}
	result := Aggregate{
		TurnCount:           len(turns),
		Outcomes:            outcomes,
		CancellationReasons: map[string]int{},
		Metrics:             map[string]Metric{},
	}
	for _, turn := range turns {
		outcome := activeOutcome
		if turn.Outcome != nil {
			outcome = *turn.Outcome
		}
		result.Outcomes[outcome]++
		if outcome == terminalOutcomes[EventTurnCancelled] {
			reason := "unavailable"
			if turn.CancellationReason != nil && *turn.CancellationReason != "" {
				reason = *turn.CancellationReason
			}
			result.CancellationReasons[reason]++
		}
		if counted, observed := turn.observedTools(); counted {
			if observed {
				result.ToolObservation.ObservedTurns++
				if turn.ToolCount != nil {
					result.ToolObservation.ToolUses += *turn.ToolCount
					if *turn.ToolCount > 0 {
						result.ToolObservation.TurnsWithTools++
					}
				}
			} else {
				result.ToolObservation.UnavailableTurns++
			}
		}
	}
	result.ActiveTurns = result.Outcomes[activeOutcome]

	// Rates are over the turns that finished, not over every turn selected. A
	// success rate that counted running turns as not-yet-successes would fall
	// whenever the fleet got busy.
	terminal := result.TurnCount - result.ActiveTurns
	result.SuccessRate = share(result.Outcomes[terminalOutcomes[EventTurnCompleted]], terminal)
	result.FailureRate = share(result.Outcomes[terminalOutcomes[EventTurnFailed]], terminal)
	result.CancellationRate = share(result.Outcomes[terminalOutcomes[EventTurnCancelled]], terminal)
	result.ToolObservation.Coverage = share(result.ToolObservation.ObservedTurns, terminal)

	for _, name := range turnMetrics {
		readings := make([]reading, 0, len(turns))
		for _, turn := range turns {
			quality := ""
			if turn.MeasurementQuality != nil {
				quality = *turn.MeasurementQuality
			}
			readings = append(readings, reading{turn.metric(name), quality})
		}
		result.Metrics[name] = metricFrom(readings)
	}
	return result
}

// Group is one aggregate and the dimension value it describes.
type Group struct {
	Value string `json:"value"`
	Aggregate
}

// groupBy splits turns by one dimension and aggregates each part.
//
// A turn whose dimension is null is grouped under "unknown" rather than
// dropped. Dropping it would make the groups sum to less than the fleet, and a
// reader adding up the columns would find work missing with nothing to say
// where it went.
func groupBy(turns []TurnRow, dimension func(TurnRow) *string) []Group {
	grouped := map[string][]TurnRow{}
	for _, turn := range turns {
		label := "unknown"
		if value := dimension(turn); value != nil && *value != "" {
			label = *value
		}
		grouped[label] = append(grouped[label], turn)
	}
	labels := make([]string, 0, len(grouped))
	for label := range grouped {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	groups := make([]Group, 0, len(labels))
	for _, label := range labels {
		groups = append(groups, Group{Value: label, Aggregate: aggregate(grouped[label])})
	}
	return groups
}

// dimensionsOfATurn names the groupings a summary reports, in the order it
// reports them.
var dimensionsOfATurn = []struct {
	key   string
	value func(TurnRow) *string
}{
	{"agents", func(t TurnRow) *string { return &t.AgentID }},
	{"harnesses", func(t TurnRow) *string { return t.Harness }},
	{"models", func(t TurnRow) *string { return t.Model }},
	{"endpoints", func(t TurnRow) *string { return t.EndpointID }},
}

func sortedFloat(values []float64) []float64 {
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return ordered
}
