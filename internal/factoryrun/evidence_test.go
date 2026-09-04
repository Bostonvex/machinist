package factoryrun

import (
	"testing"

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
