package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/owainlewis/machinist/internal/factoryrun"
)

// gitHubIssueLabels reads the labels an issue currently carries. It is its own
// interface, separate from the trigger client, because it answers a different
// question at a different moment: intake asks whether work was requested, and
// this asks what the agent said about the work once it was done.
type gitHubIssueLabels interface {
	IssueLabels(ctx context.Context, repository string, number int) ([]string, error)
}

// haltingLabels are the states an agent sets on the issue when it stops and
// hands the work back to a person: a missing decision, or a block it cannot
// clear. Both mean the same thing to a reader of the board -- nobody is working
// on this, and it is waiting on a human -- so both park.
//
// They are trusted over the run's own exit status because of an asymmetry: "I
// am blocked" is a claim against the agent's own interest and "I succeeded" is
// not. The label is also the fact an operator already reads, and the control
// plane already goes to the forge for the facts it will not take an agent's
// word for.
var haltingLabels = map[string]struct{}{
	"machinist:needs-human": {},
	"machinist:blocked":     {},
}

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
	stage, err = s.confirmedStage(ctx, target, stage)
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
	return s.store.RecordPublishedMarker(ctx, target, stage)
}

// confirmedStage checks a run's claim to have finished against what the agent
// left on the issue. An exit status of zero is what a run reports about itself,
// and an agent that stopped to ask a question exits zero having answered
// nothing: the marker then says complete while the issue two lines below it
// says the work is waiting on a person. Those cannot both be true, and the one
// the board acts on is the one that is wrong.
//
// Only a run that says it finished is checked. A running run is not claiming
// anything yet, and a failed one is already reporting against its own interest.
//
// An unreadable label state is an error, never a complete. A stage that cannot
// be confirmed is not confirmed, and this whole function exists because the
// unconfirmed reading is the one that puts finished work in front of nobody.
func (s *Server) confirmedStage(ctx context.Context, target GitHubMarkerTarget, stage factoryrun.Stage) (factoryrun.Stage, error) {
	if stage != factoryrun.StageComplete {
		return stage, nil
	}
	if s.issueLabels == nil {
		return "", errors.New("no GitHub client to confirm the run finished rather than stopped to ask")
	}
	labels, err := s.issueLabels.IssueLabels(ctx, target.Repository, target.IssueNumber)
	if err != nil {
		return "", fmt.Errorf("read issue labels: %w", err)
	}
	for _, label := range labels {
		if _, halting := haltingLabels[strings.ToLower(strings.TrimSpace(label))]; halting {
			return factoryrun.StageParked, nil
		}
	}
	return stage, nil
}
