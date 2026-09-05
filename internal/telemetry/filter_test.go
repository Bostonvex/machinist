package telemetry

import (
	"strings"
	"testing"
)

func TestAWindowBoundaryIncludesTheSecondItNames(t *testing.T) {
	// Times are stored as text and SQLite compares text lexicographically, so
	// "…T12:00:00Z" and the stored "…T12:00:00.000Z" do not order the way their
	// instants do: '.' sorts before 'Z'. Without normalising, a caller asking
	// for a window starting at noon silently loses noon's first second.
	filter, err := Filter{Since: "2026-09-05T12:00:00Z"}.Normalized()
	if err != nil {
		t.Fatalf("a plain RFC 3339 timestamp was refused: %v", err)
	}
	if filter.Since != "2026-09-05T12:00:00.000Z" {
		t.Fatalf("since normalised to %q", filter.Since)
	}
	if !(filter.Since <= "2026-09-05T12:00:00.500Z") {
		t.Fatal("a sample inside the window sorts before the window's start")
	}
}

func TestATimestampThatCannotBeReadIsRefused(t *testing.T) {
	// Passed through to SQLite, "yesterday" compares below every stored
	// timestamp and hands the caller the whole table as if it were the window
	// they asked for — a wrong answer that looks like a right one.
	for _, value := range []string{"yesterday", "2026-13-45", "", "0"} {
		if value == "" {
			continue
		}
		if _, err := (Filter{Since: value}).Normalized(); err == nil {
			t.Fatalf("since=%q was accepted", value)
		}
	}
	if _, err := (Filter{Until: "soon"}).Normalized(); err == nil {
		t.Fatal("until=soon was accepted")
	}
}

func TestAnEmptyWindowIsNotAFilter(t *testing.T) {
	filter, err := Filter{}.Normalized()
	if err != nil {
		t.Fatalf("an unfiltered query was refused: %v", err)
	}
	if where, values := filter.turnConditions("t").where(); where != "" || len(values) != 0 {
		t.Fatalf("an empty filter produced %q with %d values", where, len(values))
	}
}

func TestAWindowThatRunsBackwardsIsRefused(t *testing.T) {
	// It returns nothing, which a reader sees as "no activity" rather than "you
	// asked for a window that runs backwards".
	_, err := Filter{Since: "2026-09-05T12:00:00Z", Until: "2026-09-05T11:00:00Z"}.Normalized()
	if err == nil {
		t.Fatal("an inverted window was accepted")
	}
}

func TestAnOutcomeATurnCannotHaveIsRefused(t *testing.T) {
	// The column only ever holds the outcomes a terminal event assigns. Any
	// other value matches nothing, and a caller with a typo cannot tell that
	// apart from a fleet that did no work.
	if _, err := (Filter{Outcome: "suceeded"}).Normalized(); err == nil {
		t.Fatal("a misspelled outcome was accepted")
	}
	for _, outcome := range terminalOutcomes {
		if _, err := (Filter{Outcome: outcome}).Normalized(); err != nil {
			t.Fatalf("outcome %q is recorded by the store but refused by a query: %v", outcome, err)
		}
	}
}

func TestEveryOutcomeTheStoreRecordsCanBeQueried(t *testing.T) {
	// The valid set is derived from the table that assigns outcomes rather than
	// restated. A second copy is how test_failure became a class the store
	// recorded and a route could not be configured against.
	for _, outcome := range terminalOutcomes {
		if !isOutcome(outcome) {
			t.Fatalf("the store records outcome %q but a filter refuses it", outcome)
		}
	}
}

func TestAnIdentifierLongerThanAnyProducerWritesIsRefused(t *testing.T) {
	if _, err := (Filter{AgentID: strings.Repeat("a", maximumIdentifier+1)}).Normalized(); err == nil {
		t.Fatal("an unbounded agent id was accepted")
	}
}

func TestAFilterBindsItsValuesRatherThanWritingThem(t *testing.T) {
	// Nothing a caller supplies is concatenated into a statement. If it were,
	// this agent id would end the WHERE clause.
	filter, err := Filter{AgentID: "x' OR '1'='1", Harness: "codex"}.Normalized()
	if err != nil {
		t.Fatalf("a hostile-looking but bounded identifier was refused: %v", err)
	}
	where, values := filter.turnConditions("t").where()
	if strings.Contains(where, "OR") || strings.Contains(where, "1'='1") {
		t.Fatalf("a caller's value reached the statement: %q", where)
	}
	if len(values) != 2 || values[0] != "x' OR '1'='1" {
		t.Fatalf("the value was not bound: %v", values)
	}
}

func TestAnAgentIsInEveryWindowItWasRunningIn(t *testing.T) {
	// Filtering an agent by when it was first seen would make it vanish from
	// every window after the one it started in, and a long-lived worker would
	// disappear from the dashboard the longer it ran.
	filter, err := Filter{Since: "2026-09-05T12:00:00Z", Until: "2026-09-05T13:00:00Z"}.Normalized()
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	where, _ := filter.agentConditions("a").where()
	if !strings.Contains(where, "a.last_seen_at >= ?") || !strings.Contains(where, "a.first_seen_at <= ?") {
		t.Fatalf("agents are not selected by overlap: %q", where)
	}
}

func TestALimitIsBounded(t *testing.T) {
	// A read that scans the whole table holds a connection open behind every
	// other reader.
	if got := bound(1_000_000, defaultListLimit, maximumListLimit); got != maximumListLimit {
		t.Fatalf("an unbounded limit gave %d", got)
	}
	if got := bound(0, defaultListLimit, maximumListLimit); got != defaultListLimit {
		t.Fatalf("an unset limit gave %d", got)
	}
	if got := bound(-5, defaultListLimit, maximumListLimit); got != defaultListLimit {
		t.Fatalf("a negative limit gave %d", got)
	}
	if got := bound(25, defaultListLimit, maximumListLimit); got != 25 {
		t.Fatalf("a reasonable limit was changed to %d", got)
	}
}
