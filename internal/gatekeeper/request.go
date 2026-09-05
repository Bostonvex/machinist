package gatekeeper

import (
	"fmt"
	"strings"

	"github.com/owainlewis/machinist/internal/review"
)

// FileMode is a git tree mode for a changed path.
//
// It is a distinct type because the mode decides whether a change is words or
// behaviour, and the pull request files API does not expose it. A caller that
// has not read the git tree cannot fill this in, which is the point: it must
// not be able to guess.
type FileMode string

const (
	// ModeFile is a regular non-executable file — 100644.
	ModeFile FileMode = "100644"
	// ModeExecutable is a regular executable file — 100755.
	ModeExecutable FileMode = "100755"
	// ModeSymlink is a symbolic link — 120000.
	ModeSymlink FileMode = "120000"
	// ModeSubmodule is a gitlink — 160000.
	ModeSubmodule FileMode = "160000"
)

var fileModes = map[FileMode]struct{}{
	ModeFile: {}, ModeExecutable: {}, ModeSymlink: {}, ModeSubmodule: {},
}

// Valid reports whether m is a mode this package understands. An unread or
// unrecognized mode is invalid, never assumed to be a plain file.
func (m FileMode) Valid() bool {
	_, ok := fileModes[m]
	return ok
}

// ChangedFile is one path in the diff, with the mode it has in the tree that
// will merge.
type ChangedFile struct {
	Path string
	Mode FileMode
}

// Risk is the risk label carried by a closing issue.
type Risk string

const (
	RiskLow    Risk = "risk-low"
	RiskMedium Risk = "risk-medium"
	RiskHigh   Risk = "risk-high"
)

var risks = map[Risk]struct{}{RiskLow: {}, RiskMedium: {}, RiskHigh: {}}

// Valid reports whether r is one of the three risk labels. An issue with no
// risk label has no valid Risk, and no tier reads that as low.
func (r Risk) Valid() bool {
	_, ok := risks[r]
	return ok
}

// ClosingIssue is one issue the pull request closes, with the labels that were
// on it when it was read.
type ClosingIssue struct {
	Number int
	// Risk is the issue's risk label. An issue with none leaves this empty,
	// which is a refusal under Reviewed rather than a low risk.
	Risk Risk
	// HumanRequired reports the machinist:human-required label.
	HumanRequired bool
}

// ChecksState is the state of the required checks on the head that will merge.
type ChecksState struct {
	// Required lists the checks the default branch requires. An empty list means
	// the repository requires none, which makes the Reviewed tier unavailable:
	// absence is not green.
	Required []string
	// Passing lists the required checks that are green on this exact head.
	Passing []string
	// Strict reports whether the branch protection requires branches to be up to
	// date before merging. Without it a green check may describe a commit that
	// is not the one that lands.
	Strict bool
	// MergeStateStatus is the forge's own view, such as "CLEAN" or "BEHIND".
	MergeStateStatus string
}

// Review is the independent review of the head that will merge.
type Review struct {
	// Verdict is the reviewer's verdict as the review engine settled it.
	Verdict review.Verdict
	// Findings are the findings that came with it.
	Findings []review.Finding
	// HeadSHA is the commit the reviewer actually judged. A verdict that names a
	// different commit than the one merging is not a review of this merge.
	HeadSHA string
	// Author and Reviewer are the two parties, for the independence check.
	Author   review.Party
	Reviewer review.Party
}

// Request is everything the gatekeeper needs to decide one merge. Every field
// is a fact read from the forge or the git tree; none is a conclusion.
type Request struct {
	// Repository is the "owner/name" the pull request lives in.
	Repository string
	// PullRequest is its number.
	PullRequest int
	// HeadSHA is the exact commit that will merge.
	HeadSHA string
	// Files are the changed paths with their tree modes.
	Files []ChangedFile
	// ClosingIssues are the issues the pull request closes, re-read immediately
	// before merging.
	ClosingIssues []ClosingIssue
	// ClosingIssuesMatchIntent records that a human or the orchestrator has
	// confirmed the closing issues are the ones this change should close — or
	// that it deliberately closes none.
	ClosingIssuesMatchIntent bool
	// Checks is the state of the required checks on HeadSHA.
	Checks ChecksState
	// Review is the independent review of HeadSHA.
	Review Review
	// OwnerNamed records that the repository owner named this pull request for
	// merge. It authorizes this pull request and no other.
	OwnerNamed bool
	// ProtectedPaths are the repository's own protected paths, on top of the
	// floor this package always applies. A caller that could not read the
	// repository's list must say so through ProtectedPathsUnread rather than
	// passing an empty list.
	ProtectedPaths []string
	// ProtectedPathsUnread reports that the repository's protected-path list
	// could not be read. A list that could not be read is not an empty list, and
	// every tier refuses when it is set.
	ProtectedPathsUnread bool
}

// Valid reports the first fact that is missing or self-contradictory.
//
// It is checked before any tier is considered, because a tier evaluated against
// an incomplete Request would be deciding on absence.
func (r Request) Valid() error {
	if strings.TrimSpace(r.Repository) == "" {
		return fmt.Errorf("repository is unknown")
	}
	if r.PullRequest <= 0 {
		return fmt.Errorf("pull request number is unknown")
	}
	if strings.TrimSpace(r.HeadSHA) == "" {
		return fmt.Errorf("head commit is unknown")
	}
	if len(r.Files) == 0 {
		return fmt.Errorf("changed files are unknown")
	}
	for _, file := range r.Files {
		if strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("a changed file has no path")
		}
		if !file.Mode.Valid() {
			return fmt.Errorf("changed file %q has mode %q, which was not read from the git tree", file.Path, file.Mode)
		}
	}
	return nil
}

// paths returns the changed paths alone.
func (r Request) paths() []string {
	paths := make([]string, 0, len(r.Files))
	for _, file := range r.Files {
		paths = append(paths, strings.TrimPrefix(strings.TrimSpace(file.Path), "./"))
	}
	return paths
}
