package controlplane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// reviewerDefinitions writes a definition file and returns its path. Commands
// are given verbatim so a test can say exactly which reviewers exist.
func reviewerDefinitions(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// oneReviewerOnClaude is the ordinary shape: one implementer profile, one
// reviewer profile, and no way for the reviewer to run as the implementer.
const oneReviewerOnClaude = `[commands.implement]
profile = "codex-implementer"
role = "implementer"
timeout = "1m"

[commands.judge]
profile = "claude-reviewer"
role = "reviewer"
timeout = "1m"
`

// assignerFixture is an admitted GitHub-triggered run that has finished its
// work, which is the state review assignment starts from.
type assignerFixture struct {
	server *Server
	store  *Store
	forge  *fakeGitHubPullRequests
	runID  string
}

func newAssignerFixture(t *testing.T, definitions string, linked ...GitHubLinkedPullRequest) *assignerFixture {
	t.Helper()
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	server := admitGitHubJob(t, store, &clock)

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	// The intake command carries no profile, but independence is decided on the
	// profile that actually ran, so the attempt is what has to name one.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE attempts SET profile='codex-implementer' WHERE run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	forge := &fakeGitHubPullRequests{linked: linked}
	server.pullRequests = forge
	server.definitionPath = reviewerDefinitions(t, definitions)
	return &assignerFixture{server: server, store: store, forge: forge, runID: run.ID}
}

// assignedReviews returns the review jobs queued against the reviewed run.
func (f *assignerFixture) assignedReviews(t *testing.T) []struct {
	JobID       string
	Command     string
	PullRequest int
	Prompt      string
	Role        string
	Repository  string
} {
	t.Helper()
	rows, err := f.store.db.QueryContext(t.Context(), `SELECT a.reviewer_job_id,a.reviewer_command,a.pull_request,j.prompt,r.role,r.repository
FROM review_assignments a JOIN jobs j ON j.id=a.reviewer_job_id JOIN runs r ON r.job_id=j.id
WHERE a.reviewed_run_id=? ORDER BY a.pull_request`, f.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var assigned []struct {
		JobID       string
		Command     string
		PullRequest int
		Prompt      string
		Role        string
		Repository  string
	}
	for rows.Next() {
		var row struct {
			JobID       string
			Command     string
			PullRequest int
			Prompt      string
			Role        string
			Repository  string
		}
		if err := rows.Scan(&row.JobID, &row.Command, &row.PullRequest, &row.Prompt, &row.Role, &row.Repository); err != nil {
			t.Fatal(err)
		}
		assigned = append(assigned, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return assigned
}

// Finished work with one open change gets a reviewer that is not its author,
// and gets one only once however often the scheduler ticks.
func TestReviewerAssignerQueuesOneIndependentReview(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude, GitHubLinkedPullRequest{Number: 42, State: "open"})

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	assigned := fixture.assignedReviews(t)
	if len(assigned) != 1 {
		t.Fatalf("assigned %d reviews, want 1", len(assigned))
	}
	if assigned[0].Command != "judge" || assigned[0].Role != review.RoleReviewer {
		t.Fatalf("review was not assigned to a reviewer: %#v", assigned[0])
	}
	if assigned[0].PullRequest != 42 {
		t.Fatalf("review was assigned against pull request %d", assigned[0].PullRequest)
	}
	if assigned[0].Repository != "machinist" {
		t.Fatalf("review sits in repository %q, not the work's", assigned[0].Repository)
	}
	// The reviewer has to be told which change to judge, against which request,
	// what shape the verdict takes, and how to hand it back. A reviewer that
	// cannot deliver its verdict has not reviewed anything.
	for _, want := range []string{
		"owainlewis/machinist/pull/42",
		"owainlewis/machinist/issues/396",
		"VERDICT:",
		"/api/v1/runs/" + fixture.runID + "/review",
	} {
		if !strings.Contains(assigned[0].Prompt, want) {
			t.Fatalf("review prompt does not mention %q:\n%s", want, assigned[0].Prompt)
		}
	}
	// Every credential in the recipe is named as an environment variable the
	// worker sets for reviewer runs. A prompt carrying a literal token would be
	// a secret written into the job table and into the agent's transcript.
	for _, want := range []string{"$MACHINIST_LEASE_TOKEN", "$MACHINIST_WORKER_INSTANCE", "$MACHINIST_RUN_ID"} {
		if !strings.Contains(assigned[0].Prompt, want) {
			t.Fatalf("review prompt does not read %s from the environment:\n%s", want, assigned[0].Prompt)
		}
	}

	calls := fixture.forge.linkedCalls
	for range 3 {
		if err := fixture.server.assignReviewers(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.assignedReviews(t); len(got) != 1 {
		t.Fatalf("assigned %d reviews after repeated passes, want 1", len(got))
	}
	if fixture.forge.linkedCalls != calls {
		t.Fatalf("an already-assigned run was asked about again: %d calls became %d", calls, fixture.forge.linkedCalls)
	}
}

// A reviewer that would run as the agent that wrote the change is not assigned.
// The route would refuse such a review, so queuing it spends an agent to reach
// no verdict.
func TestReviewerAssignerRefusesTheAuthorsOwnProfile(t *testing.T) {
	fixture := newAssignerFixture(t, `[commands.judge]
profile = "codex-implementer"
role = "reviewer"
timeout = "1m"
`, GitHubLinkedPullRequest{Number: 42, State: "open"})

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews to the author's own profile", len(assigned))
	}
}

// A routed reviewer is judged on every profile it could fall back onto, not the
// one it would prefer: a fallback onto the author is still the author.
func TestReviewerAssignerRefusesAReviewerThatCouldFallBackOntoTheAuthor(t *testing.T) {
	fixture := newAssignerFixture(t, `[routes.review]
profiles = ["claude-reviewer", "codex-implementer"]

[commands.judge]
route = "review"
role = "reviewer"
timeout = "1m"
`, GitHubLinkedPullRequest{Number: 42, State: "open"})

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews to a route that can become the author", len(assigned))
	}
}

// Which change to review must be unambiguous. No open change means the work is
// not there yet; several means the control plane would be choosing.
func TestReviewerAssignerWaitsUntilOneOpenChangeIdentifiesTheWork(t *testing.T) {
	for name, linked := range map[string][]GitHubLinkedPullRequest{
		"no change at all":     nil,
		"only a closed change": {{Number: 42, State: "closed"}},
		"two open changes":     {{Number: 42, State: "open"}, {Number: 43, State: "open"}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newAssignerFixture(t, oneReviewerOnClaude, linked...)
			if err := fixture.server.assignReviewers(t.Context()); err != nil {
				t.Fatal(err)
			}
			if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
				t.Fatalf("assigned %d reviews with %s", len(assigned), name)
			}
		})
	}
}

// With no command holding the reviewer role there is nobody to assign, and the
// control plane says so by asking the forge nothing at all.
func TestReviewerAssignerAsksNothingWithoutAReviewer(t *testing.T) {
	fixture := newAssignerFixture(t, `[commands.implement]
profile = "codex-implementer"
role = "implementer"
timeout = "1m"
`, GitHubLinkedPullRequest{Number: 42, State: "open"})

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fixture.forge.linkedCalls != 0 {
		t.Fatalf("the forge was asked about a run nobody can review: %d calls", fixture.forge.linkedCalls)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews with no reviewer configured", len(assigned))
	}
}

// A run that already carries a review is not assigned another. The verdict is
// recorded; asking again would re-review work whose judgement already exists.
func TestReviewerAssignerLeavesReviewedWorkAlone(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude, GitHubLinkedPullRequest{Number: 42, State: "open"})
	if err := fixture.store.RecordReview(t.Context(), RecordedReview{
		RunID: fixture.runID, ReviewerRunID: "run_someone_else", PullRequest: 42,
		Verdict: review.VerdictReady, ReviewedHead: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
	}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fixture.forge.linkedCalls != 0 {
		t.Fatalf("the forge was asked about already-reviewed work: %d calls", fixture.forge.linkedCalls)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews of already-reviewed work", len(assigned))
	}
}

// Work that did not finish is not reviewed. There is no change to judge, and a
// verdict would read as if there had been one.
func TestReviewerAssignerIgnoresWorkThatDidNotSucceed(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude, GitHubLinkedPullRequest{Number: 42, State: "open"})
	if _, err := fixture.store.db.ExecContext(t.Context(), `UPDATE runs SET state='failed' WHERE id=?`, fixture.runID); err != nil {
		t.Fatal(err)
	}

	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews of work that failed", len(assigned))
	}
}

// A forge that will not say which change was made assigns nothing, and reports
// why rather than assigning a guess.
func TestReviewerAssignerReportsAForgeThatWillNotAnswer(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude)
	fixture.forge.linkedErr = errors.New("github is unreachable")

	err := fixture.server.assignReviewers(t.Context())
	if err == nil {
		t.Fatal("an unreadable forge should be reported, not treated as no change")
	}
	if !strings.Contains(err.Error(), fixture.runID) {
		t.Fatalf("failure does not name the run it concerns: %v", err)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews without knowing the change", len(assigned))
	}
}

// AssignReview is the only writer of the assignment record, and it refuses a
// command that is not a reviewer: the record is what stops a second assignment,
// so a wrong one is permanent.
func TestAssignReviewRefusesACommandThatIsNotAReviewer(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude)
	_, err := fixture.store.AssignReview(t.Context(), ReviewAssignmentCandidate{
		RunID: fixture.runID, Repository: "machinist", IssueNumber: 396, Agent: "codex-implementer",
	}, 42, config.ResolvedCommand{Name: "implement", Profile: "claude-reviewer", Role: review.RoleImplementer, Timeout: time.Minute})
	if err == nil {
		t.Fatal("an implementer was accepted as a reviewer")
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 0 {
		t.Fatalf("assigned %d reviews to a non-reviewer", len(assigned))
	}
}

// Review assignment accepts the reviewer role the same way independence
// checking and lease handover do: an oddly-but-validly spelled reviewer is
// still a reviewer, while an implementer never is. The rule lives once in
// internal/review and every call site obeys it.
func TestAssignReviewUsesTheSharedRoleRule(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude)
	reviewer := func(role string) config.ResolvedCommand {
		return config.ResolvedCommand{Name: "judge", Profile: "claude-reviewer", Role: role, Timeout: time.Minute}
	}
	if _, err := fixture.store.AssignReview(t.Context(), ReviewAssignmentCandidate{
		RunID: fixture.runID, Repository: "machinist", IssueNumber: 396, Agent: "codex-implementer",
	}, 42, reviewer("  REVIEWER  ")); err != nil {
		t.Fatalf("oddly-spelled reviewer was refused: %v", err)
	}
	if assigned := fixture.assignedReviews(t); len(assigned) != 1 {
		t.Fatalf("assigned %d reviews to an oddly-spelled reviewer, want 1", len(assigned))
	}
	implementer := reviewer(review.RoleImplementer)
	if _, err := fixture.store.AssignReview(t.Context(), ReviewAssignmentCandidate{
		RunID: fixture.runID, Repository: "machinist", IssueNumber: 396, Agent: "codex-implementer",
	}, 43, implementer); err == nil {
		t.Fatal("an implementer was accepted as a reviewer")
	}
}

// The assigner and the review route have to agree, or assignment queues work
// that is refused when it arrives. A run assigned by one is accepted by the
// other: same repository, reviewer role, and an agent that is not the author.
func TestAnAssignedReviewIsAcceptedByTheReviewRoute(t *testing.T) {
	fixture := newAssignerFixture(t, oneReviewerOnClaude, GitHubLinkedPullRequest{Number: 42, State: "open"})
	if err := fixture.server.assignReviewers(t.Context()); err != nil {
		t.Fatal(err)
	}
	assigned := fixture.assignedReviews(t)
	if len(assigned) != 1 {
		t.Fatalf("assigned %d reviews, want 1", len(assigned))
	}

	reviewer := reviewLease(t, fixture.store, pollRequest("worker-b", []string{"test", "claude-reviewer"}, []string{"machinist"}))
	if reviewer.Role != review.RoleReviewer {
		t.Fatalf("the assigned run holds role %q", reviewer.Role)
	}
	fixture.forge.paths = []string{"internal/controlplane/store.go"}
	route := &reviewFixture{server: fixture.server, store: fixture.store, reviewerInstance: "worker-b", reviewer: reviewer}
	recorder := route.submit(t, fixture.runID, protocol.ReviewSubmission{ReviewerRun: reviewer.ID, PullRequest: 42, Output: readyReview})
	if recorder.Code != 200 {
		t.Fatalf("the review route refused work it was assigned: %d\n%s", recorder.Code, recorder.Body.String())
	}
	verdict, count := route.recordedVerdict(t, fixture.runID)
	if count != 1 || verdict != string(review.VerdictReady) {
		t.Fatalf("assigned review recorded %d review(s) with verdict %q", count, verdict)
	}
}
