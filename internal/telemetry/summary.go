package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Bounds on what one summary reads. They are the reason a summary reports
// whether it was limited: a number computed from the most recent 500 turns of a
// busier window is a different claim from one computed from all of them, and a
// reader has to be able to tell.
const (
	maximumSummaryTurns  = 500
	maximumSummaryEvents = 2000
	maximumTimelineItems = 500
	maximumSharedItems   = 200
)

// FleetSummary is everything a dashboard shows for one window.
type FleetSummary struct {
	Fleet      Fleet               `json:"fleet"`
	Groups     map[string][]Group  `json:"groups"`
	Dimensions map[string][]string `json:"dimensions"`
	Limited    bool                `json:"limited"`
}

// Fleet is the aggregate over every turn in the window, plus what the models
// and the machines were doing.
type Fleet struct {
	ActiveAgents int `json:"active_agents"`
	Aggregate
	ModelMetrics          ModelMetrics          `json:"model_metrics"`
	InfrastructureMetrics InfrastructureMetrics `json:"infrastructure_metrics"`
}

// Summary answers the whole dashboard from one filter.
func (s *Store) Summary(ctx context.Context, filter Filter) (FleetSummary, error) {
	normalized, err := filter.Normalized()
	if err != nil {
		return FleetSummary{}, err
	}
	turns, err := s.ListTurns(ctx, normalized, maximumSummaryTurns, 0)
	if err != nil {
		return FleetSummary{}, err
	}
	agents, err := s.ListAgents(ctx, normalized, maximumSummaryTurns)
	if err != nil {
		return FleetSummary{}, err
	}
	dimensions, err := s.Dimensions(ctx)
	if err != nil {
		return FleetSummary{}, err
	}
	events, limitedEvents, err := s.modelEventsIn(ctx, normalized, maximumSummaryEvents)
	if err != nil {
		return FleetSummary{}, err
	}
	// Infrastructure is shared, so only the dimensions that describe a machine
	// narrow it. Passing the agent filters through would silently return no
	// samples whenever a reader selected a harness.
	infrastructure, err := s.InfrastructureMetricsIn(ctx, Filter{
		Since: normalized.Since, Until: normalized.Until, EndpointID: normalized.EndpointID,
	})
	if err != nil {
		return FleetSummary{}, err
	}

	fleet := Fleet{Aggregate: aggregate(turns)}
	for _, agent := range agents {
		if agent.CurrentTurnID != nil {
			fleet.ActiveAgents++
		}
	}
	fleet.ModelMetrics = ModelMetricsFrom(events)
	fleet.ModelMetrics.Limited = limitedEvents
	fleet.InfrastructureMetrics = infrastructure

	groups := map[string][]Group{}
	for _, dimension := range dimensionsOfATurn {
		groups[dimension.key] = groupBy(turns, dimension.value)
	}
	return FleetSummary{
		Fleet: fleet, Groups: groups, Dimensions: dimensions,
		// Limited says the window held more turns than were read. Without it a
		// reader cannot tell a quiet fleet from a truncated answer.
		Limited: len(turns) == maximumSummaryTurns,
	}, nil
}

// modelEventsIn reads the model events matching a filter, newest first, and
// reports whether there were more than it read.
func (s *Store) modelEventsIn(ctx context.Context, filter Filter, limit int) ([]Event, bool, error) {
	built := &conditions{}
	built.addIfSet("e.agent_id = ?", filter.AgentID)
	built.addIfSet("e.harness = ?", filter.Harness)
	built.addIfSet("e.model = ?", filter.Model)
	built.addIfSet("e.endpoint_id = ?", filter.EndpointID)
	built.addIfSet("e.observed_at >= ?", filter.Since)
	built.addIfSet("e.observed_at <= ?", filter.Until)
	if filter.Outcome != "" {
		built.add("EXISTS (SELECT 1 FROM turns model_turn WHERE model_turn.id = e.turn_id"+
			" AND model_turn.outcome = ?)", filter.Outcome)
	}
	modelEvents := []EventType{EventModelRequestStarted, EventModelFirstToken,
		EventModelCompleted, EventModelFailed}
	types := make([]any, 0, len(modelEvents))
	for _, eventType := range modelEvents {
		types = append(types, string(eventType))
	}
	built.addMany("e.event_type IN ("+placeholders(len(types))+")", types...)
	where, values := built.where()

	// One row past the limit, so a window with exactly the limit and one with
	// more are distinguishable. Reporting "limited" for both would put a
	// caveat on a complete answer.
	rows, err := s.read.QueryContext(ctx, `
		SELECT e.payload FROM events e`+where+`
		ORDER BY e.observed_at DESC, e.event_id DESC LIMIT ?`, append(values, limit+1)...)
	if err != nil {
		return nil, false, fmt.Errorf("read model events: %w", err)
	}
	defer rows.Close()
	events, err := decodeEvents(rows)
	if err != nil {
		return nil, false, err
	}
	limited := len(events) > limit
	if limited {
		events = events[:limit]
	}
	// Read newest first to bound the window, then walked oldest first, because
	// a counter of calls and a sweep over decode intervals both assume time
	// runs forwards.
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, limited, nil
}

// TurnDetail is one turn with everything observed during it.
type TurnDetail struct {
	Turn          TurnRow        `json:"turn"`
	Timeline      []TimelineItem `json:"timeline"`
	SharedContext []TimelineItem `json:"shared_context"`
	ModelMetrics  ModelMetrics   `json:"model_metrics"`
}

// TimelineItem is one event placed relative to the turn it belongs to.
type TimelineItem struct {
	EventType         string         `json:"event_type"`
	ObservedAt        string         `json:"observed_at"`
	MonotonicOffsetMS float64        `json:"monotonic_offset_ms"`
	RelativeMS        float64        `json:"relative_ms"`
	SpanID            *string        `json:"span_id"`
	ParentSpanID      *string        `json:"parent_span_id"`
	Attributes        map[string]any `json:"attributes"`
	// Scope separates what this turn did from what was happening around it. A
	// GPU that was warm during a turn was warm for the whole host, and a
	// timeline that mixed the two would read as the turn having caused it.
	Scope string `json:"scope"`
}

// TurnDetail returns one turn, or false if there is no such turn.
func (s *Store) TurnDetail(ctx context.Context, turnID string) (TurnDetail, bool, error) {
	if turnID == "" || len(turnID) > maximumIdentifier {
		return TurnDetail{}, false, fmt.Errorf("turn id is not an identifier")
	}
	row := s.read.QueryRowContext(ctx, `SELECT `+turnColumns+`
		FROM turns t JOIN agents a ON a.id = t.agent_id WHERE t.id = ?`, turnID)
	turn, err := scanTurn(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TurnDetail{}, false, nil
		}
		return TurnDetail{}, false, fmt.Errorf("read turn: %w", err)
	}

	origin, err := time.Parse(timeLayout, turn.StartedAt)
	if err != nil {
		return TurnDetail{}, false, fmt.Errorf("turn %s has an unreadable start time", turnID)
	}
	timeline, events, err := s.timelineFor(ctx, origin,
		`SELECT event_type, observed_at, payload FROM events
		 WHERE turn_id = ? ORDER BY observed_at, event_id LIMIT ?`,
		"turn", turnID, maximumTimelineItems)
	if err != nil {
		return TurnDetail{}, false, err
	}

	// Shared context is bounded by the turn: from when it started to when it
	// ended, or to now if it has not. A turn still running has no end, and
	// reading to the end of time would attach every later sample to it.
	until := s.now().UTC().Format(timeLayout)
	if turn.EndedAt != nil {
		until = *turn.EndedAt
	}
	shared, _, err := s.timelineFor(ctx, origin,
		`SELECT event_type, observed_at, payload FROM events
		 WHERE event_type IN ('server.sample', 'hardware.sample')
		   AND observed_at >= ? AND observed_at <= ?
		 ORDER BY observed_at, event_id LIMIT ?`,
		"shared_context", turn.StartedAt, until, maximumSharedItems)
	if err != nil {
		return TurnDetail{}, false, err
	}

	var modelEvents []Event
	for _, event := range events {
		switch event.EventType {
		case EventModelRequestStarted, EventModelFirstToken, EventModelCompleted, EventModelFailed:
			modelEvents = append(modelEvents, event)
		}
	}
	return TurnDetail{
		Turn: turn, Timeline: timeline, SharedContext: shared,
		ModelMetrics: ModelMetricsFrom(modelEvents),
	}, true, nil
}

func (s *Store) timelineFor(ctx context.Context, origin time.Time, query, scope string, arguments ...any) ([]TimelineItem, []Event, error) {
	rows, err := s.read.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, nil, fmt.Errorf("read timeline: %w", err)
	}
	defer rows.Close()

	items := []TimelineItem{}
	var events []Event
	for rows.Next() {
		var eventType, observedAt string
		var payload []byte
		if err := rows.Scan(&eventType, &observedAt, &payload); err != nil {
			return nil, nil, fmt.Errorf("read timeline entry: %w", err)
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			// Consistent with the rest of the store: an unreadable stored event
			// is refused rather than skipped, because a timeline missing an
			// entry it cannot show still reads as a complete timeline.
			return nil, nil, fmt.Errorf("stored event is unreadable: %w", err)
		}
		events = append(events, event)

		relative := 0.0
		if at, err := time.Parse(timeLayout, observedAt); err == nil {
			// Clamped at zero. A sample recorded a millisecond before the turn
			// started is clock jitter, and a negative offset would place it
			// before the origin of a timeline that begins at the origin.
			relative = max(0, float64(at.Sub(origin).Nanoseconds())/1e6)
		}
		items = append(items, TimelineItem{
			EventType: eventType, ObservedAt: observedAt,
			MonotonicOffsetMS: event.MonotonicOffsetMS, RelativeMS: relative,
			SpanID: event.SpanID, ParentSpanID: event.ParentSpanID,
			Attributes: event.Attributes, Scope: scope,
		})
	}
	return items, events, rows.Err()
}
