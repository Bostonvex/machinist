package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// readyReview is a reviewer output block that asks for nothing. Tests that care
// about what the route adds start from a review that adds nothing itself.
const readyReview = `VERDICT: ready-for-human-review
FINDINGS:
PROTECTED_PATHS: none
HIGH_RISK: no
NOTE: reads clean
`

// fakeGitHubPullRequests stands in for the forge: it answers what a change
// touched, and which changes reference an issue.
type fakeGitHubPullRequests struct {
	paths      []string
	err        error
	calls      int
	repository string
	number     int

	linked      []GitHubLinkedPullRequest
	linkedErr   error
	linkedCalls int
}

func (f *fakeGitHubPullRequests) ListPullRequestFiles(_ context.Context, repository string, number int) ([]string, error) {
	f.calls++
	f.repository, f.number = repository, number
	if f.err != nil {
		return nil, f.err
	}
	return f.paths, nil
}

func (f *fakeGitHubPullRequests) LinkedPullRequests(_ context.Context, repository string, number int) ([]GitHubLinkedPullRequest, error) {
	f.linkedCalls++
	f.repository, f.number = repository, number
	if f.linkedErr != nil {
		return nil, f.linkedErr
	}
	return f.linked, nil
}

// reviewFixture is one implementer run to be reviewed and one leased reviewer
// run to review it, in the same repository.
type reviewFixture struct {
	server           *Server
	store            *Store
	files            *fakeGitHubPullRequests
	author           protocol.RunSpec
	reviewer         protocol.RunSpec
	reviewerInstance string
}

func newReviewFixture(t *testing.T, changedPaths ...string) *reviewFixture {
	t.Helper()
	return newReviewFixtureIn(t, "machinist", "machinist", changedPaths...)
}

// newReviewFixtureIn allows the two runs to sit in different repositories, so a
// test can ask what happens when a review does not address its reviewer's work.
func newReviewFixtureIn(t *testing.T, authorRepository, reviewerRepository string, changedPaths ...string) *reviewFixture {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	implementer := reviewTestCommand("implement", "codex-implementer", review.RoleImplementer)
	reviewer := reviewTestCommand("judge", "claude-reviewer", review.RoleReviewer)
	if _, err := store.CreateJob(t.Context(), "write it", authorRepository, "implement", implementer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), "judge it", reviewerRepository, "judge", reviewer); err != nil {
		t.Fatal(err)
	}
	fixture := &reviewFixture{store: store, files: &fakeGitHubPullRequests{paths: changedPaths}}
	// Each run is leased by its own worker, because a worker that polls twice
	// is handed the run it already holds. Which run is which is decided by the
	// role it was given, not by the order the queue handed them out in.
	for _, instance := range []string{"worker-a", "worker-b"} {
		run := reviewLease(t, store, pollRequest(instance, []string{"codex"},
			[]string{authorRepository, reviewerRepository}))
		if run.Role == review.RoleReviewer {
			fixture.reviewer, fixture.reviewerInstance = run, instance
			continue
		}
		fixture.author = run
	}
	if fixture.author.ID == "" || fixture.reviewer.ID == "" {
		t.Fatalf("fixture leased author %q and reviewer %q", fixture.author.ID, fixture.reviewer.ID)
	}
	fixture.server = &Server{store: store, pullRequests: fixture.files, now: time.Now}
	return fixture
}

func reviewTestCommand(name, profile, role string) config.ResolvedCommand {
	return config.ResolvedCommand{
		Name: name, Executor: "codex", Prompt: "{{machinist.prompt}}", Timeout: time.Minute,
		Hash: name + "-hash", Profile: profile, Role: role,
	}
}

func reviewLease(t *testing.T, store *Store, request protocol.PollRequest) protocol.RunSpec {
	t.Helper()
	run, err := store.Poll(t.Context(), request)
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	return *run
}

// submit posts one review of the given run, as the fixture's reviewer.
func (f *reviewFixture) submit(t *testing.T, reviewedRun string, submission protocol.ReviewSubmission) *httptest.ResponseRecorder {
	t.Helper()
	if submission.InstanceID == "" {
		submission.InstanceID = f.reviewerInstance
	}
	if submission.LeaseToken == "" {
		submission.LeaseToken = f.reviewer.LeaseToken
	}
	if submission.ReviewerRun == "" {
		submission.ReviewerRun = f.reviewer.ID
	}
	if submission.PullRequest == 0 {
		submission.PullRequest = 41
	}
	if submission.Output == "" {
		submission.Output = readyReview
	}
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+reviewedRun+"/review", strings.NewReader(string(body)))
	request.SetPathValue("id", reviewedRun)
	recorder := httptest.NewRecorder()
	f.server.submitReview(recorder, request)
	return recorder
}

// recordedVerdict reads back what the control plane stored about a run, which
// is what a later reader — the marker publisher — will see.
func (f *reviewFixture) recordedVerdict(t *testing.T, runID string) (string, int) {
	t.Helper()
	var verdict string
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(verdict),'') FROM run_reviews WHERE run_id=?`, runID).Scan(&count, &verdict); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return "", 0
	}
	return verdict, count
}

// The route decides a verdict and records it against the reviewed run.
func TestReviewRouteRecordsAnIndependentVerdict(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{PullRequest: 41})
	if recorder.Code != http.StatusOK {
		t.Fatalf("submit review = %d: %s", recorder.Code, recorder.Body)
	}
	var outcome protocol.ReviewOutcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Verdict != string(review.VerdictReady) {
		t.Fatalf("verdict = %q", outcome.Verdict)
	}
	verdict, count := fixture.recordedVerdict(t, fixture.author.ID)
	if count != 1 || verdict != string(review.VerdictReady) {
		t.Fatalf("recorded %d review(s) with verdict %q", count, verdict)
	}
	if fixture.files.repository != "machinist" || fixture.files.number != 41 {
		t.Fatalf("read files of %s#%d", fixture.files.repository, fixture.files.number)
	}
}

// The independence rule is the route's, not the reviewer's: an agent that
// reviews its own work is refused whatever its output says. The identity
// compared is the profile that actually ran, so moving the reviewer onto the
// implementer's profile is enough to make the review inadmissible.
func TestReviewRouteRefusesTheAuthorAsItsOwnReviewer(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	if _, err := fixture.store.db.Exec(`UPDATE attempts SET profile=? WHERE run_id=?`, "codex-implementer", fixture.reviewer.ID); err != nil {
		t.Fatal(err)
	}
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("same-agent review = %d: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "not independent") {
		t.Fatalf("refusal does not say why: %s", recorder.Body)
	}
	if _, count := fixture.recordedVerdict(t, fixture.author.ID); count != 0 {
		t.Fatalf("a refused review was recorded anyway (%d rows)", count)
	}
}

// A run cannot review itself even when it holds a valid lease on itself.
func TestReviewRouteRefusesARunReviewingItself(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.reviewer.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("self review = %d: %s", recorder.Code, recorder.Body)
	}
	if _, count := fixture.recordedVerdict(t, fixture.reviewer.ID); count != 0 {
		t.Fatalf("a self review was recorded anyway (%d rows)", count)
	}
}

// A run that does not hold the reviewer role cannot bless anything, however
// well-formed its output.
func TestReviewRouteRefusesAReviewerWithoutTheRole(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	if _, err := fixture.store.db.Exec(`UPDATE runs SET role=? WHERE id=?`, review.RoleImplementer, fixture.reviewer.ID); err != nil {
		t.Fatal(err)
	}
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("review by an implementer = %d: %s", recorder.Code, recorder.Body)
	}
}

// The reviewer says which change it judged; GitHub says what that change
// touched. A protected path in the diff escalates a review that claimed none.
func TestReviewRouteTakesTheChangedPathsFromTheDiff(t *testing.T) {
	fixture := newReviewFixture(t, ".github/workflows/ci.yml")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("submit review = %d: %s", recorder.Code, recorder.Body)
	}
	var outcome protocol.ReviewOutcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Verdict != string(review.VerdictEscalate) {
		t.Fatalf("verdict = %q, want the protected path to escalate", outcome.Verdict)
	}
	if len(outcome.ProtectedPaths) != 1 || outcome.ProtectedPaths[0] != ".github/workflows/ci.yml" {
		t.Fatalf("protected paths = %#v", outcome.ProtectedPaths)
	}
	verdict, _ := fixture.recordedVerdict(t, fixture.author.ID)
	if verdict != string(review.VerdictEscalate) {
		t.Fatalf("recorded verdict = %q", verdict)
	}
}

// Output the parser cannot read is not a review, and leaves no verdict behind.
func TestReviewRouteRefusesUnreadableOutput(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{Output: "looks fine to me\n"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unparsable review = %d: %s", recorder.Code, recorder.Body)
	}
	if _, count := fixture.recordedVerdict(t, fixture.author.ID); count != 0 {
		t.Fatalf("an unreadable review was recorded anyway (%d rows)", count)
	}
}

// The submitting run must hold the lease it claims: a verdict is recorded
// against real work, so it has to come from the run doing it.
func TestReviewRouteRefusesAWrongLease(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{LeaseToken: "not-the-lease"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("wrong lease = %d: %s", recorder.Code, recorder.Body)
	}
	if _, count := fixture.recordedVerdict(t, fixture.author.ID); count != 0 {
		t.Fatalf("a review without a lease was recorded anyway (%d rows)", count)
	}
}

// A reviewer may only judge work in the repository it was leased for.
func TestReviewRouteRefusesWorkInAnotherRepository(t *testing.T) {
	fixture := newReviewFixtureIn(t, "other", "machinist", "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("cross-repository review = %d: %s", recorder.Code, recorder.Body)
	}
}

// Without a way to read the diff the route refuses rather than reviewing blind,
// because a protected path it cannot see would read as an ordinary change.
func TestReviewRouteRefusesWhenTheDiffCannotBeRead(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	fixture.server.pullRequests = nil
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("review without a github client = %d: %s", recorder.Code, recorder.Body)
	}
	if _, count := fixture.recordedVerdict(t, fixture.author.ID); count != 0 {
		t.Fatalf("a blind review was recorded anyway (%d rows)", count)
	}
}

// A review must name the change it judged: without a pull request there is no
// diff to read and nothing to hold the reviewer's claim against.
func TestReviewRouteRequiresThePullRequest(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	body, err := json.Marshal(protocol.ReviewSubmission{
		InstanceID: fixture.reviewerInstance, LeaseToken: fixture.reviewer.LeaseToken,
		ReviewerRun: fixture.reviewer.ID, Output: readyReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+fixture.author.ID+"/review", strings.NewReader(string(body)))
	request.SetPathValue("id", fixture.author.ID)
	recorder := httptest.NewRecorder()
	fixture.server.submitReview(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("review without a pull request = %d: %s", recorder.Code, recorder.Body)
	}
	if fixture.files.calls != 0 {
		t.Fatal("github was asked about a pull request that was never named")
	}
}

// The route is mounted where workers reach it, and behind worker authorization.
func TestReviewRouteIsMountedForAuthorizedWorkers(t *testing.T) {
	_, endpoint := newTestHTTPServer(t)
	response := postJSON(t, endpoint.URL+"/api/v1/runs/run_missing/review", protocol.ReviewSubmission{}, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated review = %d, want the route to exist and refuse it", response.StatusCode)
	}
}
