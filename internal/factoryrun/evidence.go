// Package factoryrun implements the FACTORY:RUN cross-session handoff marker.
// It records compact evidence so the next role reads only what it needs.
package factoryrun

import (
	"fmt"
	"strings"
	"time"
)

// Evidence is the compact cross-session handoff for one unit of work.
type Evidence struct {
	JobID     string
	RunID     string
	AttemptID string
	Branch    string
	PR        string
	Repo      string
	Verdict   string
	Checks    []Check
	Issues    []string
	UpdatedAt time.Time
}

// Check is one status-check result carried as evidence.
type Check struct {
	Name       string
	State      string
	DetailsURL string
}

// Validate requires the identity fields that anchor a factory run.
func (e Evidence) Validate() error {
	switch {
	case strings.TrimSpace(e.JobID) == "":
		return fmt.Errorf("factoryrun: job id is required")
	case strings.TrimSpace(e.RunID) == "":
		return fmt.Errorf("factoryrun: run id is required")
	case strings.TrimSpace(e.Repo) == "":
		return fmt.Errorf("factoryrun: repo is required")
	}
	return nil
}
