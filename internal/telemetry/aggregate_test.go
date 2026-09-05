package telemetry

import "testing"

// turnRow builds a finished turn row, so each test changes only the field it is
// about.
func turnRow(edits ...func(*TurnRow)) TurnRow {
	outcome, mode, quality := "completed", "acp_updates", "exact"
	count := 1
	duration := 1000.0
	row := TurnRow{
		ID: "turn", AgentID: "agent-a", StartedAt: "2026-09-05T12:00:00.000Z",
		Outcome: &outcome, ToolObservationMode: &mode, MeasurementQuality: &quality,
		ToolCount: &count, DurationMS: &duration,
	}
	for _, edit := range edits {
		edit(&row)
	}
	return row
}

func outcomeOf(value string) func(*TurnRow) {
	return func(row *TurnRow) { row.Outcome = &value }
}

func TestEveryOutcomeIsReportedEvenWhenItDidNotHappen(t *testing.T) {
	// A caller reading zero must be able to tell "none of these" from "this
	// build does not know that outcome".
	result := aggregate([]TurnRow{turnRow()})
	for _, outcome := range terminalOutcomes {
		if _, present := result.Outcomes[outcome]; !present {
			t.Fatalf("outcome %q is recorded by the store but missing from the aggregate", outcome)
		}
	}
	if _, present := result.Outcomes[activeOutcome]; !present {
		t.Fatal("a turn still running has no bucket to be counted in")
	}
}

func TestARunningTurnDoesNotLowerTheSuccessRate(t *testing.T) {
	finished := turnRow()
	running := turnRow(func(row *TurnRow) { row.Outcome = nil })
	result := aggregate([]TurnRow{finished, running})

	if result.ActiveTurns != 1 {
		t.Fatalf("active turns = %d, want 1", result.ActiveTurns)
	}
	if result.SuccessRate == nil || *result.SuccessRate != 1 {
		t.Fatalf("success rate = %v, want 1: a turn still running is not a failure", result.SuccessRate)
	}
}

func TestASuccessRateOverNoFinishedTurnsIsNotZero(t *testing.T) {
	result := aggregate([]TurnRow{turnRow(func(row *TurnRow) { row.Outcome = nil })})
	if result.SuccessRate != nil {
		t.Fatalf("success rate = %v, want nothing: no turn has finished", *result.SuccessRate)
	}
}

func TestABlindHarnessIsCountedAsBlindAndNotAsIdle(t *testing.T) {
	blind := turnRow(func(row *TurnRow) {
		unavailable := "unavailable"
		row.ToolObservationMode = &unavailable
		row.ToolCount = nil
	})
	result := aggregate([]TurnRow{turnRow(), blind})

	if result.ToolObservation.UnavailableTurns != 1 {
		t.Fatalf("unavailable turns = %d, want 1", result.ToolObservation.UnavailableTurns)
	}
	if result.ToolObservation.Coverage == nil || *result.ToolObservation.Coverage != 0.5 {
		t.Fatalf("coverage = %v, want 0.5", result.ToolObservation.Coverage)
	}
	if result.ToolObservation.ToolUses != 1 {
		t.Fatalf("tool uses = %d, want 1: an unobserved turn must not contribute zero", result.ToolObservation.ToolUses)
	}
}

func TestATurnStillRunningIsInNeitherSideOfCoverage(t *testing.T) {
	running := turnRow(func(row *TurnRow) { row.Outcome = nil })
	result := aggregate([]TurnRow{turnRow(), running})

	observation := result.ToolObservation
	if observation.ObservedTurns+observation.UnavailableTurns != 1 {
		t.Fatalf("coverage counted %d turns, want only the one that finished",
			observation.ObservedTurns+observation.UnavailableTurns)
	}
	if observation.Coverage == nil || *observation.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1: work in flight must not read as blindness", observation.Coverage)
	}
}

func TestACancellationWithNoStatedReasonIsStillCounted(t *testing.T) {
	result := aggregate([]TurnRow{turnRow(outcomeOf("cancelled"))})
	if result.CancellationReasons["unavailable"] != 1 {
		t.Fatalf("cancellation reasons = %v, want one unavailable", result.CancellationReasons)
	}
}

func TestTheGroupsAddUpToTheFleet(t *testing.T) {
	// A turn with no harness is grouped under "unknown" rather than dropped,
	// so a reader adding the columns finds the same total.
	known := "codex"
	turns := []TurnRow{
		turnRow(func(row *TurnRow) { row.Harness = &known }),
		turnRow(func(row *TurnRow) { row.Harness = nil }),
	}
	var total int
	for _, group := range groupBy(turns, func(t TurnRow) *string { return t.Harness }) {
		total += group.TurnCount
	}
	if total != len(turns) {
		t.Fatalf("groups hold %d turns, fleet holds %d", total, len(turns))
	}
}

func TestGroupsComeBackInAStableOrder(t *testing.T) {
	first, second := "aider", "codex"
	turns := []TurnRow{
		turnRow(func(row *TurnRow) { row.Harness = &second }),
		turnRow(func(row *TurnRow) { row.Harness = &first }),
	}
	groups := groupBy(turns, func(t TurnRow) *string { return t.Harness })
	if len(groups) != 2 || groups[0].Value != first || groups[1].Value != second {
		t.Fatalf("groups = %v, want them sorted by label", groups)
	}
}

func TestEveryTurnMetricInTheSchemaIsReadable(t *testing.T) {
	// A metric named in turnMetrics that no TurnRow field answers would be
	// reported as absent for every turn forever.
	value := 5.0
	row := turnRow(func(row *TurnRow) {
		row.TTFAMS, row.TTFVTMS, row.FirstToolMS = &value, &value, &value
		row.DurationMS, row.MaxStallMS = &value, &value
	})
	for _, name := range turnMetrics {
		if row.metric(name) == nil {
			t.Fatalf("metric %q is reported but nothing reads it from a turn", name)
		}
	}
	result := aggregate([]TurnRow{row})
	for _, name := range turnMetrics {
		if result.Metrics[name].Count != 1 {
			t.Fatalf("metric %q counted %d readings, want 1", name, result.Metrics[name].Count)
		}
	}
}
