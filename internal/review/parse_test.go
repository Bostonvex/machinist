package review

import (
	"strings"
	"testing"
)

const fullReview = `VERDICT: changes-requested
FINDINGS:
- [high] internal/runner/runner.go: leaves the child process group alive on timeout — terminate the tree
- [low] docs/README.md: stale flag name — refresh the example
PROTECTED_PATHS: .github/workflows/ci.yml
HIGH_RISK: yes
NOTE: Never submit a GitHub PR approval.`

func TestParseReadsEveryField(t *testing.T) {
	report, err := Parse(fullReview)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if report.Verdict != VerdictChangesRequested {
		t.Fatalf("verdict = %q", report.Verdict)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	first := report.Findings[0]
	if first.Severity != SeverityHigh || first.Path != "internal/runner/runner.go" {
		t.Fatalf("first finding = %#v", first)
	}
	if first.Issue != "leaves the child process group alive on timeout" || first.Recommendation != "terminate the tree" {
		t.Fatalf("first finding text = %#v", first)
	}
	if len(report.ProtectedPaths) != 1 || report.ProtectedPaths[0] != ".github/workflows/ci.yml" {
		t.Fatalf("protected paths = %#v", report.ProtectedPaths)
	}
	if !report.HighRisk || report.Note == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestParseAcceptsNoneAndAsciiSeparator(t *testing.T) {
	report, err := Parse(`VERDICT: ready-for-human-review
FINDINGS: none
PROTECTED_PATHS: none
HIGH_RISK: no`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if report.Verdict != VerdictReady || len(report.Findings) != 0 || len(report.ProtectedPaths) != 0 || report.HighRisk {
		t.Fatalf("report = %#v", report)
	}

	ascii, err := Parse("VERDICT: ready-for-human-review\nFINDINGS:\n- [info] main.go: naming - rename it")
	if err != nil {
		t.Fatalf("Parse ascii: %v", err)
	}
	if got := ascii.Findings[0]; got.Issue != "naming" || got.Recommendation != "rename it" {
		t.Fatalf("finding = %#v", got)
	}
}

func TestParseFailsClosed(t *testing.T) {
	for name, output := range map[string]string{
		"no verdict":        "FINDINGS: none\nHIGH_RISK: no",
		"unknown verdict":   "VERDICT: approved",
		"unknown key":       "VERDICT: ready-for-human-review\nAPPROVAL: yes",
		"duplicate key":     "VERDICT: ready-for-human-review\nVERDICT: escalate",
		"unknown severity":  "VERDICT: escalate\nFINDINGS:\n- [urgent] a.go: bad — fix",
		"unshaped finding":  "VERDICT: escalate\nFINDINGS:\n- something went wrong",
		"finding path only": "VERDICT: escalate\nFINDINGS:\n- [low] a.go",
		"empty issue":       "VERDICT: escalate\nFINDINGS:\n- [low] a.go:   ",
		"bad high risk":     "VERDICT: escalate\nHIGH_RISK: maybe",
	} {
		if _, err := Parse(output); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestParseIgnoresSurroundingProse(t *testing.T) {
	report, err := Parse("Here is my review.\n\n" + fullReview + "\n\nThanks.")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	if strings.Contains(report.Note, "Thanks") {
		t.Fatalf("note = %q", report.Note)
	}
}
