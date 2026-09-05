package controlplane

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factoryrun"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// admitGitHubJob runs the intake path until an issue has been admitted and its
// run exists, which is the state marker publication starts from.
func admitGitHubJob(t *testing.T, store *Store, clock *time.Time) *Server {
	t.Helper()
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: *clock,
	}}); err != nil {
		t.Fatal(err)
	}
	candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: *clock}
	triggerClient := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{candidate},
		details: GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{
			ID: "123", Actor: "owner", CreatedAt: *clock, OccurrenceKey: "github.com:123",
		}},
		permission: "write",
	}
	server := &Server{
		store: store, triggers: []config.ResolvedTrigger{trigger}, github: triggerClient,
		// The real server loads this from the same [github.repositories] table
		// the trigger resolved, so the fixture reads it back off the trigger
		// rather than restating the mapping a second time.
		githubRepositories: maps.Clone(trigger.GitHubRepositories),
		// An issue carrying none of the halting labels, which is what an issue
		// looks like when the work on it really did finish. A test about a run
		// that stopped instead replaces this.
		issueLabels: &fakeIssueLabels{},
		now:         func() time.Time { return *clock },
	}
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

// fakeIssueLabels is what the issue says about the work. It counts its calls,
// because how often the publisher asks the forge anything is part of the
// contract: a control plane with nothing new to say makes no GitHub calls.
type fakeIssueLabels struct {
	labels map[int][]string
	err    error
	calls  int
}

func (f *fakeIssueLabels) IssueLabels(_ context.Context, _ string, number int) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.labels[number], nil
}

func publishedMarker(t *testing.T, client *fakeCommentClient) factoryrun.Evidence {
	t.Helper()
	var body string
	for _, comment := range client.comments {
		if factoryrun.IsMarker(comment.Body) {
			body = comment.Body
		}
	}
	if body == "" {
		t.Fatal("no marker was published on the issue")
	}
	evidence, err := factoryrun.Parse(body)
	if err != nil {
		t.Fatalf("published marker did not parse: %v\n%s", err, body)
	}
	return evidence
}

// A GitHub-triggered run publishes evidence a later session can read back, and
// keeps it current as the run's stage changes.
func TestMarkerPublisherTracksARunToCompletion(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))

	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	claimed := publishedMarker(t, comments)
	if claimed.Stage != factoryrun.StageClaimed {
		t.Fatalf("queued run published stage %q", claimed.Stage)
	}
	if claimed.Repo != "owainlewis/machinist" || claimed.JobID == "" || claimed.RunID == "" {
		t.Fatalf("marker does not identify the run: %#v", claimed)
	}
	if len(claimed.Issues) != 1 || claimed.Issues[0] != "#396" {
		t.Fatalf("marker does not point back at the issue: %#v", claimed.Issues)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	running := publishedMarker(t, comments)
	if running.Stage != factoryrun.StageRunning {
		t.Fatalf("started run published stage %q", running.Stage)
	}
	if running.AttemptID == "" {
		t.Fatal("a started run should name the attempt doing the work")
	}
	if len(comments.created) != 1 {
		t.Fatalf("the marker was duplicated rather than updated: %d comments created", len(comments.created))
	}

	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	complete := publishedMarker(t, comments)
	if complete.Stage != factoryrun.StageComplete {
		t.Fatalf("finished run published stage %q", complete.Stage)
	}
	if complete.AttemptID != running.AttemptID {
		t.Fatalf("finished marker lost the attempt that produced the result: %q", complete.AttemptID)
	}
	if len(comments.created) != 1 {
		t.Fatalf("the marker was duplicated rather than updated: %d comments created", len(comments.created))
	}
}

// A control plane with nothing new to say makes no GitHub calls at all, however
// often the scheduler ticks.
func TestMarkerPublisherIsSilentWhenNothingChanged(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))

	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	comments.listErr = errors.New("github must not be called again")
	for range 5 {
		if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
			t.Fatalf("an unchanged run should not reach GitHub: %v", err)
		}
	}
}

// A marker only ever describes a state the mapping recognizes. An unknown run
// state stops that one issue and leaves the rest of the pass to run.
func TestMarkerPublisherRefusesToGuessAnUnknownStage(t *testing.T) {
	if _, err := runStage("halfway"); err == nil {
		t.Fatal("expected an unknown run state to be refused")
	}
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	if _, err := store.db.ExecContext(t.Context(), `UPDATE runs SET state='halfway'`); err != nil {
		t.Fatal(err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err == nil {
		t.Fatal("expected an unknown run state to be reported")
	}
	if len(comments.created) != 0 {
		t.Fatal("no marker should be written for a state nobody can name")
	}
}

// A publisher with no GitHub client configured is inert rather than a crash.
func TestMarkerPublisherWithoutAClientDoesNothing(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A failed write is not recorded as published, so the next pass tries again.
func TestMarkerPublisherRetriesAfterAFailedWrite(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	comments.listErr = errors.New("github is down")
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	if err := server.publishFactoryRunMarkers(t.Context()); err == nil {
		t.Fatal("expected the failed publication to be reported")
	}
	comments.listErr = nil
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stage := publishedMarker(t, comments).Stage; stage != factoryrun.StageClaimed {
		t.Fatalf("recovered publication wrote stage %q", stage)
	}
}

// A verdict recorded against a run reaches the issue the run was asked for,
// and a second reviewer can tighten it but never loosen it.
func TestMarkerPublisherPublishesTheStrictestRecordedVerdict(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	claimed := publishedMarker(t, comments)
	if claimed.Verdict != "" || claimed.PR != "" {
		t.Fatalf("an unreviewed run published a judgement: %#v", claimed)
	}

	if err := store.RecordReview(t.Context(), RecordedReview{
		RunID: claimed.RunID, ReviewerRunID: "run_reviewer_one", PullRequest: 7, Verdict: review.VerdictReady,
		ReviewedHead: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	reviewed := publishedMarker(t, comments)
	if reviewed.Verdict != review.VerdictReady || reviewed.PR != "#7" {
		t.Fatalf("reviewed marker = verdict %q, pr %q", reviewed.Verdict, reviewed.PR)
	}

	clock = clock.Add(time.Minute)
	if err := store.RecordReview(t.Context(), RecordedReview{
		RunID: claimed.RunID, ReviewerRunID: "run_reviewer_two", PullRequest: 7, Verdict: review.VerdictEscalate,
		ReviewedHead: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	escalated := publishedMarker(t, comments)
	if escalated.Verdict != review.VerdictEscalate {
		t.Fatalf("a second reviewer did not tighten the published verdict: %q", escalated.Verdict)
	}
	if len(comments.created) != 1 {
		t.Fatalf("the marker was duplicated rather than updated: %d comments created", len(comments.created))
	}

	// Nothing has changed since, so nothing is asked of GitHub again.
	comments.listErr = errors.New("github must not be called again")
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatalf("republished an unchanged verdict: %v", err)
	}
}

// finishGitHubRun takes the admitted run to a zero exit, which is what an agent
// that finished the work and an agent that gave up and asked a question both
// report about themselves.
func finishGitHubRun(t *testing.T, store *Store) {
	t.Helper()
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
}

// An agent that stops and hands the work back to a person still exits zero. The
// marker describes where the work actually got to, not what the process said
// about itself on the way out.
//
// Every halting label is exercised, read from the set the publisher uses rather
// than listed again here: a label added to that set and to nothing else would
// otherwise ship as a label that quietly means nothing.
func TestMarkerPublisherParksARunThatStoppedToAskAHuman(t *testing.T) {
	if len(haltingLabels) == 0 {
		t.Fatal("no label means an agent stopped, so no run can ever park")
	}
	for label := range haltingLabels {
		t.Run(label, func(t *testing.T) {
			clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
			store := openManagedTriggerTestStore(t, &clock)
			server := admitGitHubJob(t, store, &clock)
			comments := newFakeCommentClient()
			server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
			server.issueLabels = &fakeIssueLabels{labels: map[int][]string{396: {"machinist:queued", label}}}
			finishGitHubRun(t, store)
			if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
				t.Fatal(err)
			}
			if stage := publishedMarker(t, comments).Stage; stage != factoryrun.StageParked {
				t.Fatalf("a run that exited zero carrying %q published stage %q", label, stage)
			}

			board, err := store.Board(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, column := range board.Columns {
				switch column.Lane {
				case LaneParked:
					if len(column.Cards) != 1 {
						t.Fatalf("the parked lane holds %d cards", len(column.Cards))
					}
					if state := column.Cards[0].State; state != "succeeded" {
						t.Fatalf("parked card lost the state the store holds: %q", state)
					}
				case LaneDone:
					if len(column.Cards) != 0 {
						t.Fatalf("a run waiting on a human is shown as done: %#v", column.Cards)
					}
				}
			}
		})
	}
}

// GitHub hands a label back in the case it was created with, and an operator
// adding one by hand does not always match the prompt.
func TestMarkerPublisherParksWhateverCaseTheLabelArrivesIn(t *testing.T) {
	labels := slices.Sorted(maps.Keys(haltingLabels))
	if len(labels) == 0 {
		t.Fatal("no label means an agent stopped, so no run can ever park")
	}
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	server.issueLabels = &fakeIssueLabels{labels: map[int][]string{396: {"  " + strings.ToUpper(labels[0]) + "  "}}}
	finishGitHubRun(t, store)
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stage := publishedMarker(t, comments).Stage; stage != factoryrun.StageParked {
		t.Fatalf("%q did not park the run: stage %q", strings.ToUpper(labels[0]), stage)
	}
}

// A completion that could not be confirmed is not published as a completion.
// The unconfirmed reading is the one this check exists to prevent, so it is not
// the one taken when the check itself fails.
func TestMarkerPublisherRefusesACompletionItCannotConfirm(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	labels := &fakeIssueLabels{err: errors.New("the forge said no")}
	server.issueLabels = labels
	finishGitHubRun(t, store)

	if err := server.publishFactoryRunMarkers(t.Context()); err == nil {
		t.Fatal("expected an unreadable label state to be reported")
	}
	if len(comments.created) != 0 {
		t.Fatalf("a stage nobody could confirm was published anyway: %#v", comments.comments)
	}

	// Nothing was recorded, so the next pass asks again rather than leaving the
	// run described by whatever the last readable pass said.
	labels.err = nil
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stage := publishedMarker(t, comments).Stage; stage != factoryrun.StageComplete {
		t.Fatalf("recovered publication wrote stage %q", stage)
	}
}

// A run in flight is not claiming to have finished, so nothing about it needs
// confirming and no API call is spent on it.
func TestMarkerPublisherAsksTheForgeOnlyAboutRunsThatClaimToHaveFinished(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	labels := &fakeIssueLabels{}
	server.issueLabels = labels

	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if labels.calls != 0 {
		t.Fatalf("a run still in flight was checked against the issue %d times", labels.calls)
	}

	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.publishFactoryRunMarkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if labels.calls != 1 {
		t.Fatalf("a finished run was checked against the issue %d times, want once", labels.calls)
	}
}

// A build wired without the client that answers the question refuses to answer
// it, rather than falling back on the claim it was built to check.
func TestMarkerPublisherRefusesToConfirmACompletionWithNoClient(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)
	comments := newFakeCommentClient()
	server.markers = factoryrun.NewUpdater(newGitHubMarkerStore(comments))
	server.issueLabels = nil
	finishGitHubRun(t, store)
	if err := server.publishFactoryRunMarkers(t.Context()); err == nil {
		t.Fatal("expected an unconfirmable completion to be reported")
	}
	if len(comments.created) != 0 {
		t.Fatalf("a stage nobody could confirm was published anyway: %#v", comments.comments)
	}
}
