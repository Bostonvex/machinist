package factoryrun

import (
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

func TestValidateRequiresIdentity(t *testing.T) {
	if err := (Evidence{}).Validate(); err == nil {
		t.Fatal("expected validation error for empty evidence")
	}
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r"}
	if err := e.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
}

// The marker may only record a verdict the review engine can itself produce.
func TestValidateRejectsVerdictOutsideReviewContract(t *testing.T) {
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: "totally-approved-merge-it"}
	if err := e.Validate(); err == nil {
		t.Fatal("expected validation error for unknown verdict")
	}
	for _, v := range []review.Verdict{review.VerdictReady, review.VerdictChangesRequested, review.VerdictEscalate} {
		e.Verdict = v
		if err := e.Validate(); err != nil {
			t.Fatalf("review verdict %q rejected: %v", v, err)
		}
	}
}

func TestValidateRejectsUnknownCheckState(t *testing.T) {
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Checks: []Check{{Name: "test", State: "green-ish"}}}
	if err := e.Validate(); err == nil {
		t.Fatal("expected validation error for unknown check state")
	}
}

func TestChecksPassingIsNotVacuous(t *testing.T) {
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r"}
	if e.ChecksPassing() {
		t.Fatal("evidence with no checks must not report checks passing")
	}
	e.Checks = []Check{{Name: "test", State: CheckSuccess}, {Name: "vet", State: CheckPending}}
	if e.ChecksPassing() {
		t.Fatal("a pending check must not report as passing")
	}
}

// SameEvidence is what decides whether a republication is material, so every
// field it ignores is a field the marker would stop reporting changes to.
func TestSameEvidenceIgnoresOnlyTheTimestamp(t *testing.T) {
	base := Evidence{
		JobID: "j", RunID: "r", AttemptID: "a", Branch: "b", PR: "1", Repo: "o/r",
		Stage: StageRunning, Verdict: review.VerdictChangesRequested,
		Issues: []string{"#4"}, Checks: []Check{{Name: "test", State: CheckSuccess}},
	}
	later := base
	later.UpdatedAt = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if !base.SameEvidence(later) {
		t.Fatal("a differing timestamp alone is not new evidence")
	}
	for name, mutate := range map[string]func(*Evidence){
		"job":     func(e *Evidence) { e.JobID = "j2" },
		"run":     func(e *Evidence) { e.RunID = "r2" },
		"attempt": func(e *Evidence) { e.AttemptID = "a2" },
		"branch":  func(e *Evidence) { e.Branch = "b2" },
		"pr":      func(e *Evidence) { e.PR = "2" },
		"repo":    func(e *Evidence) { e.Repo = "o/other" },
		"stage":   func(e *Evidence) { e.Stage = StageComplete },
		"verdict": func(e *Evidence) { e.Verdict = review.VerdictEscalate },
		"issues":  func(e *Evidence) { e.Issues = []string{"#5"} },
		"checks":  func(e *Evidence) { e.Checks = []Check{{Name: "test", State: CheckFailure}} },
	} {
		changed := base
		mutate(&changed)
		if base.SameEvidence(changed) {
			t.Fatalf("a changed %s must count as new evidence", name)
		}
	}
}

func TestValidateRejectsUnknownStage(t *testing.T) {
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: "nearly-done"}
	if err := e.Validate(); err == nil {
		t.Fatal("expected validation error for unknown stage")
	}
	for _, stage := range []Stage{StageClaimed, StageRunning, StageComplete, StageFailed, StageParked} {
		e.Stage = stage
		if err := e.Validate(); err != nil {
			t.Fatalf("stage %q rejected: %v", stage, err)
		}
	}
}
