package review

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func submission(output string, changed ...string) Submission {
	return Submission{
		Author:       Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"},
		Reviewer:     Party{Role: RoleReviewer, Agent: "reviewer_02", RunID: "run-b"},
		ChangedPaths: changed,
		Output:       output,
	}
}

func TestEvaluateKeepsACleanReadyVerdict(t *testing.T) {
	outcome, err := Engine{}.Evaluate(submission(
		"VERDICT: ready-for-human-review\nFINDINGS: none\nPROTECTED_PATHS: none\nHIGH_RISK: no",
		"internal/review/engine.go",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Verdict != VerdictReady || outcome.HighRisk || len(outcome.Reasons) != 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestEvaluateWithholdsApprovalOnBlockingFindings(t *testing.T) {
	outcome, err := Engine{}.Evaluate(submission(
		"VERDICT: ready-for-human-review\nFINDINGS:\n- [blocker] a.go: drops errors — return them\n- [low] b.go: typo — fix",
		"a.go", "b.go",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Verdict != VerdictChangesRequested {
		t.Fatalf("verdict = %q", outcome.Verdict)
	}
	if len(outcome.Reasons) != 1 || !strings.Contains(outcome.Reasons[0], "1 finding") {
		t.Fatalf("reasons = %#v", outcome.Reasons)
	}
}

func TestEvaluateEscalatesOnProtectedPathsTheReviewerMissed(t *testing.T) {
	outcome, err := Engine{}.Evaluate(submission(
		"VERDICT: ready-for-human-review\nFINDINGS: none\nPROTECTED_PATHS: none\nHIGH_RISK: no",
		"internal/review/engine.go", ".github/workflows/ci.yml",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Verdict != VerdictEscalate || !outcome.HighRisk {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !slices.Equal(outcome.ProtectedPaths, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("protected paths = %#v", outcome.ProtectedPaths)
	}
}

func TestEvaluateNeverRelaxesTheReviewersVerdict(t *testing.T) {
	outcome, err := Engine{}.Evaluate(submission(
		"VERDICT: escalate\nFINDINGS: none\nPROTECTED_PATHS: none\nHIGH_RISK: no",
		"internal/review/engine.go",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Verdict != VerdictEscalate {
		t.Fatalf("verdict = %q, engine must not relax a reviewer's verdict", outcome.Verdict)
	}
}

func TestEvaluateHonoursConfiguredPolicy(t *testing.T) {
	engine := Engine{
		ProtectedPaths:     []string{"migrations/**"},
		BlockingSeverities: []Severity{SeverityBlocker},
	}
	outcome, err := engine.Evaluate(submission(
		"VERDICT: ready-for-human-review\nFINDINGS:\n- [high] a.go: slow — cache it\nPROTECTED_PATHS: none\nHIGH_RISK: no",
		".github/workflows/ci.yml",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Verdict != VerdictReady {
		t.Fatalf("verdict = %q, want the configured policy to replace the defaults", outcome.Verdict)
	}

	escalated, err := engine.Evaluate(submission(
		"VERDICT: ready-for-human-review\nFINDINGS: none\nHIGH_RISK: no",
		"migrations/0001_init.sql",
	))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if escalated.Verdict != VerdictEscalate {
		t.Fatalf("verdict = %q", escalated.Verdict)
	}
}

func TestEvaluateRefusesWhenTheReviewCannotStand(t *testing.T) {
	notIndependent := submission("VERDICT: ready-for-human-review")
	notIndependent.Reviewer.Agent = notIndependent.Author.Agent
	if _, err := (Engine{}).Evaluate(notIndependent); !errors.Is(err, ErrNotIndependent) {
		t.Fatalf("err = %v, want ErrNotIndependent", err)
	}

	if _, err := (Engine{}).Evaluate(submission("no verdict here")); err == nil {
		t.Fatal("expected an error for unparsable review output")
	}
}
