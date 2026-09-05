package controlplane

import (
	"errors"
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
		now: func() time.Time { return *clock },
	}
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	return server
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
