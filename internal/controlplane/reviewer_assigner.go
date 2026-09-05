package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/review"
)

const (
	// reviewAssignmentWindow bounds how long after a run finishes the control
	// plane keeps asking the forge whether a change appeared for it to review.
	reviewAssignmentWindow = 24 * time.Hour
	// maxReviewAssignments bounds one pass, so a backlog costs a bounded number
	// of forge reads per tick rather than one read per run that ever finished.
	maxReviewAssignments = 20
)

// assignReviewers pairs finished GitHub-triggered work with a reviewer.
//
// It closes the gap between the review route and the runs it judges: the route
// has always been able to record an independent verdict, but nothing asked for
// one. Assignment queues a review job; it decides nothing about the work, and
// it never merges or deploys.
//
// One candidate failing does not stop the rest, for the same reason one issue
// failing does not stop marker publication: assignment is per-run, and a
// repository that will not answer about one run says nothing about another.
func (s *Server) assignReviewers(ctx context.Context) error {
	if s.pullRequests == nil {
		return nil
	}
	reviewers, err := s.reviewerCommands()
	if err != nil {
		return err
	}
	if len(reviewers) == 0 {
		// No command holds the reviewer role, so there is nobody to assign.
		// Work then carries no verdict, which is what its marker says.
		return nil
	}
	candidates, err := s.store.ReviewAssignmentCandidates(ctx, s.store.now().UTC().Add(-reviewAssignmentWindow), maxReviewAssignments)
	if err != nil {
		return fmt.Errorf("read review assignment candidates: %w", err)
	}
	var failures []error
	for _, candidate := range candidates {
		if err := s.assignReviewer(ctx, reviewers, candidate); err != nil {
			failures = append(failures, fmt.Errorf("review assignment for run %s: %w", candidate.RunID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) assignReviewer(ctx context.Context, reviewers []config.ResolvedCommand, candidate ReviewAssignmentCandidate) error {
	linked, err := s.pullRequests.LinkedPullRequests(ctx, candidate.GitHubRepository, candidate.IssueNumber)
	if err != nil {
		return err
	}
	pullRequest, ok := soleOpenPullRequest(linked)
	if !ok {
		// No open change, or more than one. Neither is an error: a run may not
		// have produced a pull request yet, and where several are open the
		// control plane cannot tell which one is the work without guessing.
		return nil
	}
	command, ok := independentReviewer(reviewers, candidate.Agent)
	if !ok {
		// Every configured reviewer could run as the agent that wrote the
		// change. Assigning one would queue a review the route is bound to
		// refuse, which spends an agent to arrive at no verdict.
		return nil
	}
	command, err = config.RenderPrompt(command, reviewAssignmentPrompt(candidate, pullRequest))
	if err != nil {
		return err
	}
	if _, err := s.store.AssignReview(ctx, candidate, pullRequest, command); err != nil {
		return err
	}
	return nil
}

// soleOpenPullRequest returns the one open pull request among the linked ones.
//
// Exactly one is the only case that can be acted on. Zero means there is
// nothing to review yet. More than one means the control plane would be
// choosing which change the run made, and a review of the wrong change is
// worse than no review, because it produces a verdict that reads as if it
// covered the work.
func soleOpenPullRequest(linked []GitHubLinkedPullRequest) (int, bool) {
	found := 0
	number := 0
	for _, pullRequest := range linked {
		if !strings.EqualFold(pullRequest.State, "open") {
			continue
		}
		found++
		number = pullRequest.Number
	}
	if found != 1 || number <= 0 {
		return 0, false
	}
	return number, true
}

// reviewerCommands resolves every configured command that holds the reviewer
// role, in the definition's own order so assignment is deterministic.
func (s *Server) reviewerCommands() ([]config.ResolvedCommand, error) {
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		return nil, fmt.Errorf("load definitions for review assignment: %w", err)
	}
	var reviewers []config.ResolvedCommand
	for _, name := range definition.CommandNames() {
		command, err := definition.ResolveCommand(name)
		if err != nil {
			return nil, err
		}
		if normalizeRole(command.Role) == review.RoleReviewer {
			reviewers = append(reviewers, command)
		}
	}
	return reviewers, nil
}

// independentReviewer returns the first configured reviewer that cannot run as
// the agent that wrote the change.
//
// The test is on every profile the command could land on, not the one it would
// prefer: a route falls back, and a reviewer that falls back onto the author's
// profile is a review the route will refuse after the work is already done. A
// command that names no profile at all is skipped, because an identity that
// cannot be read cannot be shown to be independent.
func independentReviewer(reviewers []config.ResolvedCommand, agent string) (config.ResolvedCommand, bool) {
	author := normalizeRole(agent)
	if author == "" {
		return config.ResolvedCommand{}, false
	}
	for _, command := range reviewers {
		profiles := commandProfiles(command)
		if len(profiles) == 0 {
			continue
		}
		independent := true
		for _, profile := range profiles {
			if normalizeRole(profile) == author {
				independent = false
				break
			}
		}
		if independent {
			return command, true
		}
	}
	return config.ResolvedCommand{}, false
}

// commandProfiles is every profile a command could run as: the one it names, or
// the route's candidates.
func commandProfiles(command config.ResolvedCommand) []string {
	if profile := strings.TrimSpace(command.Profile); profile != "" {
		return []string{profile}
	}
	return command.Candidates
}
