package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
	"github.com/owainlewis/machinist/internal/review"
)

// fakeOpenChanges stands in for the forge. It records the name it was asked
// under, because asking the forge under the logical name is a bug that returns
// an empty list rather than an error.
type fakeOpenChanges struct {
	changes    []gatekeeper.Change
	err        error
	askedAbout []string
}

func (f *fakeOpenChanges) OpenChanges(_ context.Context, repository string) ([]gatekeeper.Change, error) {
	f.askedAbout = append(f.askedAbout, repository)
	if f.err != nil {
		return nil, f.err
	}
	return f.changes, nil
}

type mergeOwedFixture struct {
	server *Server
	store  *Store
	forge  *fakeOpenChanges
	run    string
}

const (
	mergeOwedRepository = "machinist"
	mergeOwedSlug       = "Bostonvex/machinist"
	mergeOwedHead       = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
)

func newMergeOwedFixture(t *testing.T) *mergeOwedFixture {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "write it", mergeOwedRepository, "implement",
		reviewTestCommand("implement", "codex-implementer", review.RoleImplementer)); err != nil {
		t.Fatal(err)
	}
	run := reviewLease(t, store, pollRequest("worker-a", []string{"codex"}, []string{mergeOwedRepository}))
	if run.ID == "" {
		t.Fatal("the job produced no run to review")
	}
	forge := &fakeOpenChanges{}
	return &mergeOwedFixture{
		store: store, forge: forge, run: run.ID,
		server: &Server{
			store: store, changes: forge, now: time.Now,
			githubRepositories: map[string]string{mergeOwedRepository: mergeOwedSlug},
		},
	}
}

func (f *mergeOwedFixture) approve(t *testing.T, pullRequest int, head string) {
	t.Helper()
	err := f.store.RecordReview(t.Context(), RecordedReview{
		RunID: f.run, ReviewerRunID: f.run,
		Verdict: review.VerdictReady, PullRequest: pullRequest,
		ReviewedHead: head,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *mergeOwedFixture) read(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	f.server.readMergeOwed(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/merge-owed"+query, nil))
	return recorder
}

func (f *mergeOwedFixture) readOK(t *testing.T, query string) map[string]any {
	t.Helper()
	recorder := f.read(t, query)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func mergeableChange(number int, head string) gatekeeper.Change {
	return gatekeeper.Change{
		Repository: mergeOwedSlug, Number: number, Title: "feat: a thing",
		URL:       "https://github.com/Bostonvex/machinist/pull/1",
		Head:      head,
		Mergeable: gatekeeper.CanMerge, MergeState: gatekeeper.MergeStateClean,
	}
}

func TestApprovedWorkIsReportedAsOwedAMerge(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.changes = []gatekeeper.Change{mergeableChange(1, mergeOwedHead)}
	fixture.approve(t, 1, mergeOwedHead)

	body := fixture.readOK(t, "?repository=machinist")
	changes := body["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want the one open change", changes)
	}
	change := changes[0].(map[string]any)
	if change["disposition"] != string(gatekeeper.DispositionMerge) {
		t.Fatalf("change = %#v, want it owed a merge", change)
	}
	if change["number"].(float64) != 1 || change["head"] != mergeOwedHead {
		t.Fatalf("change = %#v, want the forge's own number and head", change)
	}
}

func TestTheStoreIsAskedUnderTheLogicalNameAndTheForgeUnderTheSlug(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.changes = []gatekeeper.Change{mergeableChange(1, mergeOwedHead)}
	fixture.approve(t, 1, mergeOwedHead)

	body := fixture.readOK(t, "?repository=machinist")
	// The two names are different names for one repository. Asking the forge
	// under "machinist" gets a 404 that reads like an empty repository, and
	// asking the store under "Bostonvex/machinist" finds no reviews, so the
	// answer would be a confident "nothing is owed" either way.
	if len(fixture.forge.askedAbout) != 1 || fixture.forge.askedAbout[0] != mergeOwedSlug {
		t.Fatalf("forge asked about %v, want the slug", fixture.forge.askedAbout)
	}
	changes := body["changes"].([]any)
	if changes[0].(map[string]any)["disposition"] != string(gatekeeper.DispositionMerge) {
		t.Fatalf("change = %#v, want the review found under the logical name", changes[0])
	}
	if body["repository"] != mergeOwedSlug {
		t.Fatalf("repository = %v, want the slug the changes were read from", body["repository"])
	}
}

func TestAChangeNobodyReviewedIsReportedRatherThanDropped(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.changes = []gatekeeper.Change{mergeableChange(1, mergeOwedHead)}

	body := fixture.readOK(t, "?repository=machinist")
	changes := body["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want unreviewed work listed too", changes)
	}
	if changes[0].(map[string]any)["disposition"] != string(gatekeeper.DispositionWaiting) {
		t.Fatalf("change = %#v, want it waiting rather than owed", changes[0])
	}
}

func TestTheGradesInTheWayAreNamedInTheAnswer(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.changes = []gatekeeper.Change{mergeableChange(1, mergeOwedHead)}
	err := fixture.store.RecordReview(t.Context(), RecordedReview{
		RunID: fixture.run, ReviewerRunID: fixture.run,
		Verdict: review.VerdictReady, PullRequest: 1, ReviewedHead: mergeOwedHead,
		Findings: []review.Finding{{
			Severity: review.SeverityHigh, Path: "a.go",
			Issue: "it is wrong", Recommendation: "make it right",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := fixture.readOK(t, "?repository=machinist")
	change := body["changes"].([]any)[0].(map[string]any)
	// "Attention is owed" without saying what kind sends a person back to the
	// review to find out. The grade is the part they act on.
	findings, listed := change["open_findings"].([]any)
	if !listed || len(findings) != 1 || findings[0] != string(review.SeverityHigh) {
		t.Fatalf("change = %#v, want the open grade named", change)
	}
	if change["disposition"] != string(gatekeeper.DispositionAttention) {
		t.Fatalf("change = %#v, want it owed attention", change)
	}
}

func TestAReadWithNoRepositoryNamesTheOnesThatCouldBeAsked(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.approve(t, 1, mergeOwedHead)

	recorder := fixture.read(t, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", recorder.Code)
	}
	// Refusing without saying what could have been asked leaves the caller
	// guessing at a name the store keeps and the flag does not default to.
	if body := recorder.Body.String(); !strings.Contains(body, mergeOwedRepository) {
		t.Fatalf("body = %q, want it to name the repository with reviews", body)
	}
	if len(fixture.forge.askedAbout) != 0 {
		t.Fatalf("forge asked about %v, want nothing before a repository is named", fixture.forge.askedAbout)
	}
}

func TestARepositoryTheControlPlaneCannotMapIsRefused(t *testing.T) {
	fixture := newMergeOwedFixture(t)

	recorder := fixture.read(t, "?repository=somewhere-else")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	// Passed through under its logical name the forge answers 404, which reads
	// as a repository with nothing open in it.
	if len(fixture.forge.askedAbout) != 0 {
		t.Fatalf("forge asked about %v, want nothing for an unmapped name", fixture.forge.askedAbout)
	}
}

func TestARateLimitedForgeIsReportedAsARateLimitAndNotAsEmpty(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.err = &GitHubCLIError{
		Kind: GitHubCLIErrorRateLimit, Operation: "list open pull requests",
		Detail: "API rate limit exceeded", Err: errors.New("exit status 1"),
	}

	recorder := fixture.read(t, "?repository=machinist")
	// 200 with an empty list says nothing is owed. The budget being spent is
	// not evidence about what is owed, and this is the one caller that would
	// act on the difference.
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAForgeThatFailsForAnyOtherReasonIsABadGateway(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.err = &GitHubCLIError{
		Kind: GitHubCLIErrorCommand, Operation: "list open pull requests",
		Detail: "not found", Err: errors.New("exit status 1"),
	}

	recorder := fixture.read(t, "?repository=machinist")
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAForgeFailureIsNotReportedAsNothingOwed(t *testing.T) {
	fixture := newMergeOwedFixture(t)
	fixture.forge.err = errors.New("gh exploded")

	recorder := fixture.read(t, "?repository=machinist")
	if recorder.Code == http.StatusOK {
		t.Fatalf("status 200 on a failed read: %s", recorder.Body.String())
	}
}

func TestARepositoryWithNothingOpenIsAnEmptyListAndNotARefusal(t *testing.T) {
	fixture := newMergeOwedFixture(t)

	body := fixture.readOK(t, "?repository=machinist")
	// Nothing open is a true answer, unlike nothing read.
	if changes := body["changes"].([]any); len(changes) != 0 {
		t.Fatalf("changes = %#v, want none", changes)
	}
	// A listing with no time on it cannot be told from one taken an hour ago,
	// and merge-owed is a report a person leaves open on a second screen.
	readAt, said := body["read_at"].(string)
	if !said {
		t.Fatalf("body = %#v, want it to say when the forge was read", body)
	}
	when, err := time.Parse(time.RFC3339, readAt)
	if err != nil {
		t.Fatalf("read_at = %q, want a time: %v", readAt, err)
	}
	if when.IsZero() {
		t.Fatalf("read_at = %q, want when it was actually read", readAt)
	}
}

func TestNoRepositoriesToOfferIsSaidInWords(t *testing.T) {
	// An empty join reads as a bug in the message rather than as the absence
	// it is: "the control plane has reviews for ".
	if describeRepositories(nil) == "" {
		t.Fatal("an empty list of repositories describes itself as nothing at all")
	}
}
