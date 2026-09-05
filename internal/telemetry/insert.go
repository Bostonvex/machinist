package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Insert records events and updates the state derived from them.
//
// It returns the number of events that were new. Re-sending a batch is not an
// error and does not double-count: a producer that cannot confirm its last
// batch landed should be able to send it again, because the alternative is a
// producer that drops telemetry rather than risk a duplicate.
func (s *Store) Insert(ctx context.Context, events []Event) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	receivedAt := s.now().UTC().Format(timeLayout)

	transaction, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin telemetry write: %w", err)
	}
	defer transaction.Rollback()

	inserted := 0
	for _, event := range events {
		new, err := s.insertOne(ctx, transaction, event, receivedAt)
		if err != nil {
			// The whole batch fails. A partially applied batch would leave the
			// derived tables describing a turn from half its events, and the
			// producer has no way to know which half to resend.
			return 0, err
		}
		if new {
			inserted++
		}
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit telemetry write: %w", err)
	}
	return inserted, nil
}

func (s *Store) insertOne(ctx context.Context, tx *sql.Tx, event Event, receivedAt string) (bool, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("encode event %s: %w", event.EventID, err)
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events(
  event_id, schema_version, event_type, observed_at, received_at,
  agent_id, agent_display_name, harness, model, endpoint_id, session_id, turn_id, payload
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.EventID, event.SchemaVersion, string(event.EventType), event.ObservedAt, receivedAt,
		event.Agent.ID, event.Agent.DisplayName,
		event.Harness, event.Model, event.EndpointID, event.SessionID, event.TurnID, string(payload))
	if err != nil {
		return false, fmt.Errorf("record event %s: %w", event.EventID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record event %s: %w", event.EventID, err)
	}
	if changed == 0 {
		// Already held. Deriving from it again would double-count a turn's
		// tools or move an agent's state backwards on a replay.
		return false, nil
	}

	if isInfrastructure(event.EventType) {
		return true, s.insertSample(ctx, tx, event)
	}
	// A model call the producer could not tie to a specific turn still belongs
	// in events, where it can be aggregated across a fleet. It does not belong
	// in the derived tables: attributing it would put one agent's latency on
	// whichever turn happened to be open, and a wrong attribution is worse than
	// an absent one because it cannot be told apart from a real measurement.
	if strings.HasPrefix(string(event.EventType), "model.") && stringAttribute(event, "correlation") != "exact" {
		return true, nil
	}
	if err := s.upsertAgent(ctx, tx, event); err != nil {
		return false, err
	}
	return true, s.upsertTurn(ctx, tx, event)
}

func (s *Store) insertSample(ctx context.Context, tx *sql.Tx, event Event) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO infrastructure_samples(
  event_id, observed_at, event_type, endpoint_id, provider_id, node_id,
  metric_name, unit, measurement_quality, value
) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		event.EventID, event.ObservedAt, string(event.EventType), event.EndpointID,
		optionalString(event, "provider_id"), optionalString(event, "node_id"),
		stringAttribute(event, "metric_name"), optionalString(event, "unit"),
		optionalString(event, "measurement_quality"), numberAttribute(event, "value"))
	if err != nil {
		return fmt.Errorf("record sample %s: %w", event.EventID, err)
	}
	return nil
}

func (s *Store) upsertAgent(ctx context.Context, tx *sql.Tx, event Event) error {
	var lastSeen, currentState string
	var currentTurn *string
	err := tx.QueryRowContext(ctx, `SELECT last_seen_at, current_state, current_turn_id FROM agents WHERE id=?`,
		event.Agent.ID).Scan(&lastSeen, &currentState, &currentTurn)
	known := err == nil
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read agent %s: %w", event.Agent.ID, err)
	}
	// Events arrive out of order — a retried batch, a producer whose queue
	// drained late. An older event must not overwrite what a newer one already
	// established, or an agent that finished half an hour ago flickers back to
	// running whenever its backlog is delivered.
	if known && event.ObservedAt < lastSeen {
		return nil
	}

	state, ok := agentStates[event.EventType]
	if !ok {
		// Nothing this event type says bears on what the agent is doing. Hold
		// the state rather than guessing at one.
		if known {
			state = currentState
		} else {
			state = "active"
		}
	}
	turn := event.TurnID
	if clearsCurrentTurn[event.EventType] {
		turn = nil
	} else if turn == nil {
		turn = currentTurn
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO agents(
  id, display_name, first_seen_at, last_seen_at, harness, model, endpoint_id, current_state, current_turn_id
) VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  display_name=excluded.display_name,
  last_seen_at=excluded.last_seen_at,
  harness=COALESCE(excluded.harness, agents.harness),
  model=COALESCE(excluded.model, agents.model),
  endpoint_id=COALESCE(excluded.endpoint_id, agents.endpoint_id),
  current_state=excluded.current_state,
  current_turn_id=excluded.current_turn_id`,
		event.Agent.ID, event.Agent.DisplayName, event.ObservedAt, event.ObservedAt,
		event.Harness, event.Model, event.EndpointID, state, turn)
	if err != nil {
		return fmt.Errorf("record agent %s: %w", event.Agent.ID, err)
	}
	return nil
}

func (s *Store) upsertTurn(ctx context.Context, tx *sql.Tx, event Event) error {
	if event.TurnID == nil || *event.TurnID == "" {
		return nil
	}
	kind := event.EventType
	turnEvent := strings.HasPrefix(string(kind), "turn.")
	outcome, terminal := terminalOutcomes[kind]

	var endedAt, outcomeValue any
	if terminal {
		endedAt, outcomeValue = event.ObservedAt, outcome
	}

	// A duration attribute is trusted where the producer sent one, and derived
	// from elapsed_ms on the milestone event otherwise. A harness that reports
	// its own milestone timings and one that only says "this just happened"
	// should produce the same turn record; which of the two a producer is
	// should not change what a reader can ask.
	elapsed := func(on EventType) any {
		if value, ok := numberOrNil(event, "elapsed_ms"); ok && kind == on {
			return value
		}
		return nil
	}
	firstOf := func(name string, on EventType) any {
		if value, ok := numberOrNil(event, name); ok {
			return value
		}
		return elapsed(on)
	}

	var maxStall any
	if turnEvent {
		maxStall = firstOf("max_stall_ms", EventTurnStall)
		if maxStall == nil && kind == EventTurnStall {
			if value, ok := numberOrNil(event, "gap_ms"); ok {
				maxStall = value
			}
		}
	}
	onlyWhen := func(condition bool, value any) any {
		if condition {
			return value
		}
		return nil
	}

	_, err := tx.ExecContext(ctx, `INSERT INTO turns(
  id, agent_id, session_id, started_at, ended_at, outcome,
  ttfa_ms, ttfvt_ms, first_tool_ms, duration_ms, max_stall_ms,
  tool_count, tool_observation_mode, measurement_quality,
  error_category, error_code, cancellation_reason, harness, model, endpoint_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  started_at=MIN(turns.started_at, excluded.started_at),
  ended_at=COALESCE(excluded.ended_at, turns.ended_at),
  outcome=COALESCE(excluded.outcome, turns.outcome),
  ttfa_ms=COALESCE(excluded.ttfa_ms, turns.ttfa_ms),
  ttfvt_ms=COALESCE(excluded.ttfvt_ms, turns.ttfvt_ms),
  first_tool_ms=COALESCE(excluded.first_tool_ms, turns.first_tool_ms),
  duration_ms=COALESCE(excluded.duration_ms, turns.duration_ms),
  max_stall_ms=CASE
    WHEN excluded.max_stall_ms IS NULL THEN turns.max_stall_ms
    WHEN turns.max_stall_ms IS NULL THEN excluded.max_stall_ms
    ELSE MAX(turns.max_stall_ms, excluded.max_stall_ms) END,
  tool_count=COALESCE(excluded.tool_count, turns.tool_count),
  tool_observation_mode=COALESCE(excluded.tool_observation_mode, turns.tool_observation_mode),
  measurement_quality=COALESCE(excluded.measurement_quality, turns.measurement_quality),
  error_category=COALESCE(excluded.error_category, turns.error_category),
  error_code=COALESCE(excluded.error_code, turns.error_code),
  cancellation_reason=COALESCE(excluded.cancellation_reason, turns.cancellation_reason),
  harness=COALESCE(excluded.harness, turns.harness),
  model=COALESCE(excluded.model, turns.model),
  endpoint_id=COALESCE(excluded.endpoint_id, turns.endpoint_id)`,
		*event.TurnID, event.Agent.ID, event.SessionID, event.ObservedAt, endedAt, outcomeValue,
		firstOf("ttfa_ms", EventTurnFirstActivity),
		firstOf("ttfvt_ms", EventTurnFirstVisibleText),
		firstOf("first_tool_ms", EventTurnFirstTool),
		onlyWhen(terminal, numberAttribute(event, "duration_ms")),
		maxStall,
		onlyWhen(terminal, numberAttribute(event, "tool_count")),
		onlyWhen(terminal, optionalString(event, "tool_observation_mode")),
		onlyWhen(turnEvent, optionalString(event, "measurement_quality")),
		onlyWhen(kind == EventTurnFailed, optionalString(event, "error_category")),
		onlyWhen(kind == EventTurnFailed, optionalString(event, "error_code")),
		onlyWhen(kind == EventTurnCancelled, optionalString(event, "cancellation_reason")),
		event.Harness, event.Model, event.EndpointID)
	if err != nil {
		return fmt.Errorf("record turn %s: %w", *event.TurnID, err)
	}
	return nil
}

const timeLayout = "2006-01-02T15:04:05.000Z"

// Attribute readers. Every event reaching the store has been through
// ValidateEvent, so a present attribute already has the declared shape and
// these do not re-check it; an absent one is nil, which SQLite stores as NULL.

func stringAttribute(event Event, name string) string {
	value, _ := event.Attributes[name].(string)
	return value
}

func optionalString(event Event, name string) any {
	if value, ok := event.Attributes[name].(string); ok {
		return value
	}
	return nil
}

func numberOrNil(event Event, name string) (float64, bool) {
	raw, present := event.Attributes[name]
	if !present {
		return 0, false
	}
	return asNumber(raw)
}

func numberAttribute(event Event, name string) any {
	if value, ok := numberOrNil(event, name); ok {
		return value
	}
	return nil
}
