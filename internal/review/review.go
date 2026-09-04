// Package review models the independent review of one unit of software work.
//
// A review is independent when the party that produced the work and the party
// that judges it are different agents in different runs. This package holds the
// verdict contract, the reviewer output parser, and the policy that decides the
// final verdict. It performs no GitHub writes and starts no processes: callers
// record the outcome as run evidence.
package review

import "strings"

// Verdict is the terminal judgement on one unit of reviewed work.
type Verdict string

const (
	// VerdictReady means the work is fit for a human to look at. It is never a
	// merge authorization: merge stays with the gatekeeper and a named human.
	VerdictReady Verdict = "ready-for-human-review"
	// VerdictChangesRequested means the author must act before review resumes.
	VerdictChangesRequested Verdict = "changes-requested"
	// VerdictEscalate means no agent may decide this; a human must.
	VerdictEscalate Verdict = "escalate"
)

var verdictStrictness = map[Verdict]int{
	VerdictReady:            0,
	VerdictChangesRequested: 1,
	VerdictEscalate:         2,
}

// Valid reports whether v is one of the three verdicts the contract defines.
func (v Verdict) Valid() bool {
	_, ok := verdictStrictness[v]
	return ok
}

// stricter returns whichever verdict constrains the work more. The engine only
// ever moves a verdict up this order, so policy can withhold a reviewer's
// approval but can never manufacture one.
func stricter(a, b Verdict) Verdict {
	if verdictStrictness[b] > verdictStrictness[a] {
		return b
	}
	return a
}

// Severity ranks one finding. Blocker and high stop the work by default.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityHigh    Severity = "high"
	SeverityMedium  Severity = "medium"
	SeverityLow     Severity = "low"
	SeverityInfo    Severity = "info"
)

var severities = map[Severity]struct{}{
	SeverityBlocker: {}, SeverityHigh: {}, SeverityMedium: {},
	SeverityLow: {}, SeverityInfo: {},
}

// Valid reports whether s is a recognized severity. Unrecognized severities are
// rejected at parse time rather than silently ranked lowest.
func (s Severity) Valid() bool {
	_, ok := severities[s]
	return ok
}

// Finding is one defect the reviewer observed in the diff.
type Finding struct {
	Severity       Severity
	Path           string
	Issue          string
	Recommendation string
}

// Report is the reviewer's own output, parsed. It is a claim, not a decision:
// Engine.Evaluate turns a Report plus the changed paths into an Outcome.
type Report struct {
	Verdict        Verdict
	Findings       []Finding
	ProtectedPaths []string
	HighRisk       bool
	Note           string
}

// Blocking returns the findings at or above the given severities.
func (r Report) Blocking(severities []Severity) []Finding {
	if len(severities) == 0 {
		return nil
	}
	wanted := make(map[Severity]struct{}, len(severities))
	for _, severity := range severities {
		wanted[severity] = struct{}{}
	}
	var blocking []Finding
	for _, finding := range r.Findings {
		if _, ok := wanted[finding.Severity]; ok {
			blocking = append(blocking, finding)
		}
	}
	return blocking
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
