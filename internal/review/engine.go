package review

import (
	"fmt"
	"sort"
)

// Submission is one completed review offered for a decision.
type Submission struct {
	// Author is the party that produced the change.
	Author Party
	// Reviewer is the party that judged it.
	Reviewer Party
	// ChangedPaths are the repository paths the change touches, as reported by
	// the diff rather than by either party.
	ChangedPaths []string
	// Output is the reviewer's raw output block.
	Output string
}

// Outcome is the engine's decision. It is the record a caller writes as run
// evidence.
type Outcome struct {
	Verdict        Verdict
	Findings       []Finding
	ProtectedPaths []string
	HighRisk       bool
	Note           string
	// Reasons explain every constraint the engine applied on top of the
	// reviewer's own verdict, in the order they were applied.
	Reasons []string
}

// Engine decides a verdict from a review submission.
//
// The engine can only make a verdict stricter. It withholds approval the
// reviewer offered; it never grants approval the reviewer withheld. The zero
// value is usable and applies DefaultProtectedPaths and
// DefaultBlockingSeverities.
type Engine struct {
	// ProtectedPaths are glob patterns whose change forces escalation. A "**"
	// segment matches any number of path segments. Empty means the default set.
	ProtectedPaths []string
	// BlockingSeverities are the finding severities that withhold a ready
	// verdict. Empty means the default set.
	BlockingSeverities []Severity
}

// Evaluate checks independence, parses the reviewer's output, and applies
// policy.
//
// It returns an error — and no outcome — when the review cannot stand at all:
// when the parties are not independent, or when the output does not parse. A
// caller that sees an error has no review, which is not the same as a review
// that found nothing.
func (e Engine) Evaluate(submission Submission) (Outcome, error) {
	if err := CheckIndependence(submission.Author, submission.Reviewer); err != nil {
		return Outcome{}, err
	}
	report, err := Parse(submission.Output)
	if err != nil {
		return Outcome{}, fmt.Errorf("parse review output: %w", err)
	}

	outcome := Outcome{
		Verdict:  report.Verdict,
		Findings: report.Findings,
		HighRisk: report.HighRisk,
		Note:     report.Note,
	}

	if blocking := report.Blocking(e.blockingSeverities()); len(blocking) > 0 {
		outcome.Verdict = stricter(outcome.Verdict, VerdictChangesRequested)
		outcome.Reasons = append(outcome.Reasons,
			fmt.Sprintf("%d finding(s) at blocking severity", len(blocking)))
	}

	outcome.ProtectedPaths = mergePaths(
		report.ProtectedPaths,
		protectedMatches(e.protectedPaths(), submission.ChangedPaths),
	)
	if len(outcome.ProtectedPaths) > 0 {
		outcome.Verdict = stricter(outcome.Verdict, VerdictEscalate)
		outcome.HighRisk = true
		outcome.Reasons = append(outcome.Reasons,
			fmt.Sprintf("protected paths changed: %v", outcome.ProtectedPaths))
	}
	if report.HighRisk {
		outcome.Verdict = stricter(outcome.Verdict, VerdictEscalate)
		outcome.Reasons = append(outcome.Reasons, "reviewer marked the change high risk")
	}
	return outcome, nil
}

func (e Engine) protectedPaths() []string {
	if len(e.ProtectedPaths) == 0 {
		return DefaultProtectedPaths
	}
	return e.ProtectedPaths
}

func (e Engine) blockingSeverities() []Severity {
	if len(e.BlockingSeverities) == 0 {
		return DefaultBlockingSeverities
	}
	return e.BlockingSeverities
}

func mergePaths(lists ...[]string) []string {
	seen := make(map[string]struct{})
	for _, list := range lists {
		for _, item := range list {
			if item != "" {
				seen[item] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	merged := make([]string, 0, len(seen))
	for item := range seen {
		merged = append(merged, item)
	}
	sort.Strings(merged)
	return merged
}
