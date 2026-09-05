package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/owainlewis/machinist/internal/factoryrun"
)

// runStage maps a run state onto the handoff stage a later session reads.
// Operator cancellation parks rather than fails: nothing was demonstrated about
// the work, so the next reader is being told to look, not that the work is
// beyond saving. A state no one recognizes produces no marker at all, because a
// guessed stage is worse than an absent one.
func runStage(state string) (factoryrun.Stage, error) {
	switch state {
	case "queued":
		return factoryrun.StageClaimed, nil
	case "running":
		return factoryrun.StageRunning, nil
	case "succeeded":
		return factoryrun.StageComplete, nil
	case "failed", "timed_out":
		return factoryrun.StageFailed, nil
	case "cancelled":
		return factoryrun.StageParked, nil
	default:
		return "", fmt.Errorf("unknown run state %q", state)
	}
}

// publishFactoryRunMarkers writes the FACTORY:RUN marker for every
// GitHub-triggered run whose issue does not yet describe where the run has got
// to, or what was decided about it. Publication is bookkeeping on the issue and nothing else: it writes no
// label, admits no request, and never merges or deploys.
//
// One issue failing does not stop the rest, because a marker is per-run
// evidence and a repository that rejects one write says nothing about another.
func (s *Server) publishFactoryRunMarkers(ctx context.Context) error {
	if s.markers == nil {
		return nil
	}
	targets, err := s.store.GitHubMarkerTargets(ctx)
	if err != nil {
		return fmt.Errorf("read factory run marker targets: %w", err)
	}
	var failures []error
	for _, target := range targets {
		if err := s.publishFactoryRunMarker(ctx, target); err != nil {
			failures = append(failures, fmt.Errorf("factory run marker %s#%d: %w", target.Repository, target.IssueNumber, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) publishFactoryRunMarker(ctx context.Context, target GitHubMarkerTarget) error {
	stage, err := runStage(target.RunState)
	if err != nil {
		return err
	}
	evidence := factoryrun.Evidence{
		JobID:     target.JobID,
		RunID:     target.RunID,
		AttemptID: target.AttemptID,
		Repo:      target.Repository,
		Stage:     stage,
		Verdict:   target.Verdict,
		Issues:    []string{"#" + strconv.Itoa(target.IssueNumber)},
	}
	if target.PullRequest > 0 {
		evidence.PR = "#" + strconv.Itoa(target.PullRequest)
	}
	if _, err := s.markers.Publish(ctx, target.Repository, target.IssueNumber, evidence); err != nil {
		return err
	}
	return s.store.RecordPublishedMarker(ctx, target)
}
