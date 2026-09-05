package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Bounds on what one query may return. They are ceilings rather than defaults:
// a caller that asks for more gets the ceiling, because a read that scans the
// whole table holds a connection open behind every other reader, and the
// answers past these counts are for an export rather than a dashboard.
const (
	defaultListLimit = 100
	maximumListLimit = 500
	maximumSamples   = 500
)

// AgentRow is one agent as a reader sees it.
type AgentRow struct {
	ID                   string  `json:"id"`
	DisplayName          string  `json:"display_name"`
	FirstSeenAt          string  `json:"first_seen_at"`
	LastSeenAt           string  `json:"last_seen_at"`
	Harness              *string `json:"harness"`
	Model                *string `json:"model"`
	EndpointID           *string `json:"endpoint_id"`
	CurrentState         string  `json:"current_state"`
	CurrentTurnID        *string `json:"current_turn_id"`
	CurrentTurnStartedAt *string `json:"current_turn_started_at"`
}

// TurnRow is one turn as a reader sees it.
//
// Every measurement is nullable because a harness that could not measure one is
// the common case rather than the exception, and a zero would be read as a turn
// that answered instantly.
type TurnRow struct {
	ID                  string   `json:"id"`
	AgentID             string   `json:"agent_id"`
	AgentDisplayName    string   `json:"agent_display_name"`
	SessionID           *string  `json:"session_id"`
	StartedAt           string   `json:"started_at"`
	EndedAt             *string  `json:"ended_at"`
	Outcome             *string  `json:"outcome"`
	TTFAMS              *float64 `json:"ttfa_ms"`
	TTFVTMS             *float64 `json:"ttfvt_ms"`
	FirstToolMS         *float64 `json:"first_tool_ms"`
	DurationMS          *float64 `json:"duration_ms"`
	MaxStallMS          *float64 `json:"max_stall_ms"`
	ToolCount           *int     `json:"tool_count"`
	ToolObservationMode *string  `json:"tool_observation_mode"`
	MeasurementQuality  *string  `json:"measurement_quality"`
	ErrorCategory       *string  `json:"error_category"`
	ErrorCode           *string  `json:"error_code"`
	CancellationReason  *string  `json:"cancellation_reason"`
	Harness             *string  `json:"harness"`
	Model               *string  `json:"model"`
	EndpointID          *string  `json:"endpoint_id"`
}

// SampleRow is one infrastructure reading.
//
// It carries scope "shared_context" so a reader cannot mistake it for something
// the agent it appears beside was responsible for. A GPU that was warm during a
// turn was warm for the whole host.
type SampleRow struct {
	Scope              string  `json:"scope"`
	EventType          string  `json:"event_type"`
	ObservedAt         string  `json:"observed_at"`
	EndpointID         *string `json:"endpoint_id"`
	ProviderID         *string `json:"provider_id"`
	NodeID             *string `json:"node_id"`
	MetricName         string  `json:"metric_name"`
	Unit               *string `json:"unit"`
	MeasurementQuality *string `json:"measurement_quality"`
	Value              float64 `json:"value"`
}

// ListAgents returns the agents matching a filter, most recently seen first.
func (s *Store) ListAgents(ctx context.Context, filter Filter, limit int) ([]AgentRow, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	where, values := filter.agentConditions("a").where()
	values = append(values, bound(limit, defaultListLimit, maximumListLimit))
	rows, err := s.read.QueryContext(ctx, `
		SELECT a.id, a.display_name, a.first_seen_at, a.last_seen_at, a.harness, a.model,
		       a.endpoint_id, a.current_state, a.current_turn_id,
		       (SELECT started_at FROM turns current_turn WHERE current_turn.id = a.current_turn_id)
		FROM agents a`+where+` ORDER BY a.last_seen_at DESC, a.id LIMIT ?`, values...)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	agents := []AgentRow{}
	for rows.Next() {
		var agent AgentRow
		if err := rows.Scan(&agent.ID, &agent.DisplayName, &agent.FirstSeenAt, &agent.LastSeenAt,
			&agent.Harness, &agent.Model, &agent.EndpointID, &agent.CurrentState,
			&agent.CurrentTurnID, &agent.CurrentTurnStartedAt); err != nil {
			return nil, fmt.Errorf("read agent: %w", err)
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

const turnColumns = `t.id, t.agent_id, a.display_name, t.session_id, t.started_at, t.ended_at,
	t.outcome, t.ttfa_ms, t.ttfvt_ms, t.first_tool_ms, t.duration_ms, t.max_stall_ms,
	t.tool_count, t.tool_observation_mode, t.measurement_quality, t.error_category,
	t.error_code, t.cancellation_reason, t.harness, t.model, t.endpoint_id`

func scanTurn(scan func(...any) error) (TurnRow, error) {
	var turn TurnRow
	err := scan(&turn.ID, &turn.AgentID, &turn.AgentDisplayName, &turn.SessionID,
		&turn.StartedAt, &turn.EndedAt, &turn.Outcome, &turn.TTFAMS, &turn.TTFVTMS,
		&turn.FirstToolMS, &turn.DurationMS, &turn.MaxStallMS, &turn.ToolCount,
		&turn.ToolObservationMode, &turn.MeasurementQuality, &turn.ErrorCategory,
		&turn.ErrorCode, &turn.CancellationReason, &turn.Harness, &turn.Model, &turn.EndpointID)
	return turn, err
}

// ListTurns returns the turns matching a filter, most recent first.
//
// The ordering breaks ties on id. Two turns that started in the same
// millisecond would otherwise come back in whatever order SQLite chose, and a
// caller paging with an offset would see one turn twice and another not at all.
func (s *Store) ListTurns(ctx context.Context, filter Filter, limit, offset int) ([]TurnRow, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	where, values := filter.turnConditions("t").where()
	values = append(values, bound(limit, defaultListLimit, maximumListLimit), offset)
	rows, err := s.read.QueryContext(ctx, `
		SELECT `+turnColumns+`
		FROM turns t JOIN agents a ON a.id = t.agent_id`+where+`
		ORDER BY t.started_at DESC, t.id LIMIT ? OFFSET ?`, values...)
	if err != nil {
		return nil, fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()

	turns := []TurnRow{}
	for rows.Next() {
		turn, err := scanTurn(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("read turn: %w", err)
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

// ListSamples returns infrastructure readings, most recent first.
//
// Only the window and the endpoint narrow it. The agent dimensions are not
// offered, because filtering shared hardware by an agent would answer a
// question — what this agent's GPU was doing — that the data cannot support.
func (s *Store) ListSamples(ctx context.Context, filter Filter, limit int) ([]SampleRow, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	built := &conditions{}
	built.addIfSet("observed_at >= ?", filter.Since)
	built.addIfSet("observed_at <= ?", filter.Until)
	built.addIfSet("endpoint_id = ?", filter.EndpointID)
	where, values := built.where()
	values = append(values, bound(limit, defaultListLimit, maximumSamples))

	rows, err := s.read.QueryContext(ctx, `
		SELECT event_type, observed_at, endpoint_id, provider_id, node_id,
		       metric_name, unit, measurement_quality, value
		FROM infrastructure_samples`+where+`
		ORDER BY observed_at DESC, event_id LIMIT ?`, values...)
	if err != nil {
		return nil, fmt.Errorf("list samples: %w", err)
	}
	defer rows.Close()

	samples := []SampleRow{}
	for rows.Next() {
		sample := SampleRow{Scope: "shared_context"}
		if err := rows.Scan(&sample.EventType, &sample.ObservedAt, &sample.EndpointID,
			&sample.ProviderID, &sample.NodeID, &sample.MetricName, &sample.Unit,
			&sample.MeasurementQuality, &sample.Value); err != nil {
			return nil, fmt.Errorf("read sample: %w", err)
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

// Dimensions lists the values a filter can usefully take.
//
// It reads the turns table rather than a fixed list, so a dimension offered to
// a reader is one that some turn actually carries. Offering a model nothing ran
// on would let a reader select it and conclude the fleet was idle.
func (s *Store) Dimensions(ctx context.Context) (map[string][]string, error) {
	dimensions := map[string][]string{}
	for _, pair := range []struct{ key, column string }{
		{"agents", "agent_id"}, {"harnesses", "harness"},
		{"models", "model"}, {"endpoints", "endpoint_id"}, {"outcomes", "outcome"},
	} {
		// The column name is from this list, never from a caller.
		rows, err := s.read.QueryContext(ctx,
			`SELECT DISTINCT `+pair.column+` FROM turns WHERE `+pair.column+
				` IS NOT NULL ORDER BY 1 LIMIT 500`)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", pair.key, err)
		}
		values := []string{}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, fmt.Errorf("read %s: %w", pair.key, err)
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		dimensions[pair.key] = values
	}
	return dimensions, nil
}

// payloadsFor reads the stored events of the given types for a set of turns.
//
// The rows come back as the validated payloads that were stored, decoded
// through the same Event type the ingest path produced. A second decoding shape
// here would be a second definition of what an event is.
func (s *Store) payloadsFor(ctx context.Context, turnIDs []string, eventTypes []EventType) ([]Event, error) {
	if len(turnIDs) == 0 || len(eventTypes) == 0 {
		return nil, nil
	}
	values := make([]any, 0, len(turnIDs)+len(eventTypes))
	for _, id := range turnIDs {
		values = append(values, id)
	}
	for _, eventType := range eventTypes {
		values = append(values, string(eventType))
	}
	query := `SELECT payload FROM events WHERE turn_id IN (` + placeholders(len(turnIDs)) +
		`) AND event_type IN (` + placeholders(len(eventTypes)) + `) ORDER BY observed_at, event_id`
	rows, err := s.read.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, fmt.Errorf("read turn events: %w", err)
	}
	defer rows.Close()
	return decodeEvents(rows)
}

func decodeEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read event: %w", err)
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			// A payload that will not decode was written by a version of this
			// store that wrote something else. Skipping it would let an
			// aggregate silently describe a subset of the window; refusing says
			// so.
			return nil, fmt.Errorf("stored event is unreadable: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	marks := make([]byte, 0, count*2-1)
	for index := 0; index < count; index++ {
		if index > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
	}
	return string(marks)
}
