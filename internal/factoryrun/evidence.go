// Package factoryrun implements the FACTORY:RUN cross-session handoff marker.
// It records compact evidence so the next role reads only what it needs.
//
// Evidence is read back by a later session to decide what already happened, so
// every field fails closed: a value that cannot be parsed is an error, never a
// permissive default. In particular an unstated check state is not "success"
// and an unrecognized verdict is not a verdict.
package factoryrun

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

// Evidence is the compact cross-session handoff for one unit of work.
type Evidence struct {
	JobID     string
	RunID     string
	AttemptID string
	Branch    string
	PR        string
	Repo      string
	// Stage is where the run has reached, or empty when the writer does not
	// track a stage. Any other value is rejected.
	Stage Stage
	// Verdict is the review engine's terminal judgement, or empty while the
	// run has not been reviewed yet. Any other value is rejected, so the
	// marker can never record a verdict the review engine cannot produce.
	Verdict   review.Verdict
	Checks    []Check
	Issues    []string
	UpdatedAt time.Time
}

// Stage is how far a run has got. It is the field that changes as a run
// progresses, so it is what makes a republished marker material rather than a
// timestamp bump. It is a closed set for the same reason CheckState is: a stage
// a reader does not recognize is an error, not a stage to guess at.
type Stage string

const (
	// StageClaimed means the run exists and owns the issue, but has not started.
	StageClaimed Stage = "claimed"
	// StageRunning means an attempt is executing.
	StageRunning Stage = "running"
	// StageComplete means the run finished on its own terms.
	StageComplete Stage = "complete"
	// StageFailed means the run finished without producing its work.
	StageFailed Stage = "failed"
	// StageParked means the run stopped and is waiting on a human. Operator
	// cancellation parks rather than fails: nothing was proven about the work.
	StageParked Stage = "parked"
)

var stages = map[Stage]struct{}{
	StageClaimed: {}, StageRunning: {}, StageComplete: {}, StageFailed: {}, StageParked: {},
}

// Valid reports whether s is one of the recognized stages.
func (s Stage) Valid() bool {
	_, ok := stages[s]
	return ok
}

// CheckState is the outcome of one status check. It is a closed set: a state
// outside it is a parse error rather than a value a consumer has to interpret.
type CheckState string

const (
	// CheckSuccess means the check completed and passed.
	CheckSuccess CheckState = "success"
	// CheckFailure means the check completed and failed.
	CheckFailure CheckState = "failure"
	// CheckPending means the check has not reported a result yet.
	CheckPending CheckState = "pending"
	// CheckNeutral means the check reported no judgement (skipped, cancelled).
	CheckNeutral CheckState = "neutral"
)

var checkStates = map[CheckState]struct{}{
	CheckSuccess: {}, CheckFailure: {}, CheckPending: {}, CheckNeutral: {},
}

// Valid reports whether s is one of the recognized check states.
func (s CheckState) Valid() bool {
	_, ok := checkStates[s]
	return ok
}

// Passing reports whether s is evidence that the check passed. Only an explicit
// success is: everything else, including an unrecognized state, is not.
func (s CheckState) Passing() bool { return s == CheckSuccess }

// Check is one status-check result carried as evidence.
type Check struct {
	Name       string
	State      CheckState
	DetailsURL string
}

// ChecksPassing reports whether every recorded check passed. Evidence carrying
// no checks is not evidence that checks passed, so it reports false.
func (e Evidence) ChecksPassing() bool {
	if len(e.Checks) == 0 {
		return false
	}
	for _, c := range e.Checks {
		if !c.State.Passing() {
			return false
		}
	}
	return true
}

// Validate requires the identity fields that anchor a factory run, and rejects
// any verdict or check state outside its contract.
func (e Evidence) Validate() error {
	switch {
	case strings.TrimSpace(e.JobID) == "":
		return fmt.Errorf("factoryrun: job id is required")
	case strings.TrimSpace(e.RunID) == "":
		return fmt.Errorf("factoryrun: run id is required")
	case strings.TrimSpace(e.Repo) == "":
		return fmt.Errorf("factoryrun: repo is required")
	}
	if e.Stage != "" && !e.Stage.Valid() {
		return fmt.Errorf("factoryrun: unknown stage %q", e.Stage)
	}
	if e.Verdict != "" && !e.Verdict.Valid() {
		return fmt.Errorf("factoryrun: unknown verdict %q", e.Verdict)
	}
	for _, c := range e.Checks {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("factoryrun: check name is required")
		}
		if !c.State.Valid() {
			return fmt.Errorf("factoryrun: check %q has unknown state %q", c.Name, c.State)
		}
	}
	return nil
}

// SameEvidence reports whether two evidence values say the same thing about a
// run, ignoring UpdatedAt. Republication compares on this rather than on the
// rendered bytes: a marker whose only difference is when it was written is not
// a material change, and rewriting it would churn the issue on every tick.
func (e Evidence) SameEvidence(other Evidence) bool {
	if e.JobID != other.JobID || e.RunID != other.RunID || e.AttemptID != other.AttemptID {
		return false
	}
	if e.Branch != other.Branch || e.PR != other.PR || e.Repo != other.Repo {
		return false
	}
	if e.Stage != other.Stage || e.Verdict != other.Verdict {
		return false
	}
	if !slices.Equal(e.Issues, other.Issues) {
		return false
	}
	return slices.Equal(e.Checks, other.Checks)
}
