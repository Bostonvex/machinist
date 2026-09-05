package telemetry

import (
	"fmt"
	"strings"
	"time"
)

// Filter narrows a query to a window and a set of dimensions.
//
// It is a closed struct rather than a map because every field here becomes part
// of a SQL statement. The values are bound as parameters and the column names
// come from this file; nothing a caller supplies is ever concatenated into the
// statement itself.
type Filter struct {
	Since      string
	Until      string
	AgentID    string
	Harness    string
	Model      string
	EndpointID string
	Outcome    string
}

// maximumIdentifier bounds a dimension value. These arrive from a query string
// and are compared against columns; a value longer than any identifier the
// producer could have written is a mistake or a probe, and either way it
// matches nothing.
const maximumIdentifier = 256

// Normalized returns the filter with its timestamps in the stored form, or an
// error naming the field that could not be read.
//
// Timestamps matter more here than they look. Times are stored as text and
// SQLite compares text lexicographically, so "2026-09-05T12:00:00Z" and the
// stored "2026-09-05T12:00:00.000Z" do not order the way their instants do:
// '.' sorts before 'Z', which silently drops the first second of the window a
// caller asked for. Normalising to the stored layout is what makes the
// comparison mean what it reads as.
//
// An unparseable timestamp is refused rather than dropped. Python's collector
// passed the string through to SQLite, where "yesterday" compares below every
// stored timestamp and the caller is handed the whole table as if it were the
// window they asked for — a wrong answer that looks like a right one.
func (f Filter) Normalized() (Filter, error) {
	normalized := f
	for _, field := range []struct {
		name  string
		value *string
	}{{"since", &normalized.Since}, {"until", &normalized.Until}} {
		if *field.value == "" {
			continue
		}
		parsed, err := parseTimestamp(*field.value)
		if err != nil {
			return Filter{}, fmt.Errorf("%s is not a timestamp", field.name)
		}
		*field.value = parsed.UTC().Format(timeLayout)
	}
	if normalized.Since != "" && normalized.Until != "" && normalized.Since > normalized.Until {
		// An inverted window returns nothing, which reads as "no activity"
		// rather than "you asked for a window that runs backwards".
		return Filter{}, fmt.Errorf("since is after until")
	}
	for _, field := range []struct {
		name, value string
	}{
		{"agent_id", normalized.AgentID}, {"harness", normalized.Harness},
		{"model", normalized.Model}, {"endpoint_id", normalized.EndpointID},
		{"outcome", normalized.Outcome},
	} {
		if len(field.value) > maximumIdentifier {
			return Filter{}, fmt.Errorf("%s is too long to be an identifier", field.name)
		}
	}
	if normalized.Outcome != "" && !isOutcome(normalized.Outcome) {
		// The outcome column only ever holds these. Any other value silently
		// matches nothing, and a caller with a typo cannot tell that apart from
		// a fleet that did no work.
		return Filter{}, fmt.Errorf("outcome %q is not one that a turn can have", normalized.Outcome)
	}
	return normalized, nil
}

// isOutcome reports whether a turn can end this way. The answer is derived from
// the table that assigns outcomes rather than restated, so a new terminal event
// cannot become an outcome the store records and a query cannot ask for.
func isOutcome(value string) bool {
	for _, outcome := range terminalOutcomes {
		if outcome == value {
			return true
		}
	}
	return false
}

// parseTimestamp accepts the stored layout and RFC 3339, which is what a caller
// writing a query string by hand will produce.
func parseTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{timeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable")
}

// clause is one bound comparison. Nothing in this file builds a clause from a
// caller-supplied string; the SQL is written here and only the value travels.
type clause struct {
	sql   string
	value any
}

type conditions struct {
	clauses []clause
}

func (c *conditions) add(sql string, value any) {
	c.clauses = append(c.clauses, clause{sql, value})
}

// addIfSet skips an empty value rather than comparing against the empty string,
// which would match the rows where the column is genuinely empty.
func (c *conditions) addIfSet(sql, value string) {
	if value != "" {
		c.add(sql, value)
	}
}

func (c *conditions) where() (string, []any) {
	if len(c.clauses) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(c.clauses))
	values := make([]any, 0, len(c.clauses))
	for _, item := range c.clauses {
		parts = append(parts, item.sql)
		values = append(values, item.value)
	}
	return " WHERE " + strings.Join(parts, " AND "), values
}

// turnConditions builds the turn-table predicate for a filter. A turn is placed
// in the window by when it started, so a long turn appears in the window it
// began in rather than in every window it overlaps.
func (f Filter) turnConditions(alias string) *conditions {
	built := &conditions{}
	built.addIfSet(alias+".agent_id = ?", f.AgentID)
	built.addIfSet(alias+".harness = ?", f.Harness)
	built.addIfSet(alias+".model = ?", f.Model)
	built.addIfSet(alias+".endpoint_id = ?", f.EndpointID)
	built.addIfSet(alias+".outcome = ?", f.Outcome)
	built.addIfSet(alias+".started_at >= ?", f.Since)
	built.addIfSet(alias+".started_at <= ?", f.Until)
	return built
}

// agentConditions places an agent in the window by overlap rather than by a
// single instant: an agent that was last seen inside the window is in it, and
// so is one that was first seen before the window and is still running. An
// agent filtered by when it was first seen would vanish from every window after
// the one it started in.
func (f Filter) agentConditions(alias string) *conditions {
	built := &conditions{}
	built.addIfSet(alias+".id = ?", f.AgentID)
	built.addIfSet(alias+".harness = ?", f.Harness)
	built.addIfSet(alias+".model = ?", f.Model)
	built.addIfSet(alias+".endpoint_id = ?", f.EndpointID)
	built.addIfSet(alias+".last_seen_at >= ?", f.Since)
	built.addIfSet(alias+".first_seen_at <= ?", f.Until)
	if f.Outcome != "" {
		built.add("EXISTS (SELECT 1 FROM turns outcome_turn WHERE outcome_turn.agent_id = "+
			alias+".id AND outcome_turn.outcome = ?)", f.Outcome)
	}
	return built
}

// bound clamps a caller's limit into a range this database can answer without
// holding a read open long enough to matter.
func bound(limit, fallback, maximum int) int {
	if limit <= 0 {
		limit = fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}
