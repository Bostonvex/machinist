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
	// Verdict is the review engine's terminal judgement, or empty while the
	// run has not been reviewed yet. Any other value is rejected, so the
	// marker can never record a verdict the review engine cannot produce.
	Verdict   review.Verdict
	Checks    []Check
	Issues    []string
	UpdatedAt time.Time
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
