package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// changesRequestedReview is a reviewer output block that withholds approval.
const changesRequestedReview = `VERDICT: changes-requested
FINDINGS:
- [high] internal/runner/runner.go: the lease is released before the attempt is recorded — record the attempt first
PROTECTED_PATHS: none
HIGH_RISK: no
NOTE: one thing to fix
`

// unreadableReview names no verdict at all. It is what a reviewer that ran out
// of context, crashed mid-sentence, or answered in prose actually produces --
// not a wrong verdict, an absent one.
const unreadableReview = `I looked at this and it seems mostly fine to me.
`

// promotedNumbers is what the forge was asked to take out of draft.
func promotedNumbers(fixture *reviewFixture) []int { return fixture.files.promoted }

// submitAndDecodeOutcome posts a review and reads the outcome the route
// returned, failing if the route did not accept the submission.
func submitAndDecodeOutcome(t *testing.T, fixture *reviewFixture, output string) protocol.ReviewOutcome {
	t.Helper()
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{PullRequest: 41, Output: output})
	if recorder.Code != http.StatusOK {
		t.Fatalf("submit review = %d: %s", recorder.Code, recorder.Body)
	}
	var outcome protocol.ReviewOutcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	return outcome
}

// The first of the three standings the draft exists to separate: work a
// reviewer objected to stays a draft, so nobody reads it as vouched for.
func TestWorkAReviewerObjectedToStaysADraft(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	outcome := submitAndDecodeOutcome(t, fixture, changesRequestedReview)
	if outcome.Promoted {
		t.Fatal("changes-requested reported the change as promoted")
	}
	if got := promotedNumbers(fixture); len(got) != 0 {
		t.Fatalf("the forge was asked to promote %v after changes were requested", got)
	}
}

// The second: approved work is taken out of draft, on the change that was
// judged and in the repository the forge knows it by.
func TestApprovedWorkIsTakenOutOfDraft(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	outcome := submitAndDecodeOutcome(t, fixture, readyReview)
	if !outcome.Promoted {
		t.Fatal("an approval did not take the change out of draft")
	}
	if got := promotedNumbers(fixture); len(got) != 1 || got[0] != 41 {
		t.Fatalf("promoted %v, want exactly pull request 41", got)
	}
	// Promoting under the logical name would ask the forge about a repository
	// owned by nobody, and the 404 would read as a missing pull request.
	if got := fixture.files.promotedRepos; len(got) != 1 || got[0] != authorSlug {
		t.Fatalf("promoted in %v, want the forge slug %q", got, authorSlug)
	}
}

// The third: a verdict that cannot be read is refused, and a refusal to review
// must never travel the same path as an approval.
func TestAVerdictThatCannotBeReadPromotesNothing(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{PullRequest: 41, Output: unreadableReview})
	if recorder.Code == http.StatusOK {
		t.Fatalf("an unreadable verdict was accepted: %s", recorder.Body)
	}
	// Nothing was recorded, so there is nothing to promote on -- but the check
	// that matters is that the forge was never asked, not that the fold said no.
	if fixture.files.promoteCalls != 0 {
		t.Fatalf("the forge was asked to promote %d time(s) on a refused review", fixture.files.promoteCalls)
	}
	if verdict, count := fixture.recordedVerdict(t, fixture.author.ID); count != 0 {
		t.Fatalf("a refused review recorded verdict %q", verdict)
	}
}

// An objection already on the record is not undone by a later approval. This is
// the two-reviewer case, and promoting here would present objected-to work as
// ready to read -- the failure the draft prevents, from the other direction.
func TestAnApprovalDoesNotPromoteOverAnObjectionAlreadyRecorded(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	if outcome := submitAndDecodeOutcome(t, fixture, changesRequestedReview); outcome.Promoted {
		t.Fatal("changes-requested promoted")
	}
	// A second reviewer, on the same change, approving what the first objected
	// to. The route records both; the fold is what decides.
	if _, err := fixture.store.CreateJob(t.Context(), "judge it again", "machinist", "judge",
		reviewTestCommand("judge", "claude-reviewer", review.RoleReviewer)); err != nil {
		t.Fatal(err)
	}
	second := reviewLease(t, fixture.store, pollRequest("worker-c", []string{"codex"}, []string{"machinist"}))
	if second.Role != review.RoleReviewer {
		t.Fatalf("second run has role %q, want a reviewer", second.Role)
	}
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{
		InstanceID: "worker-c", LeaseToken: second.LeaseToken, ReviewerRun: second.ID,
		PullRequest: 41, Output: readyReview,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("second review = %d: %s", recorder.Code, recorder.Body)
	}
	if got := promotedNumbers(fixture); len(got) != 0 {
		t.Fatalf("promoted %v while an objection stood", got)
	}
}

// A promotion the forge refuses leaves a draft and does not fail the review.
// The verdict is already recorded; failing here would invite a reviewer retry,
// and a retry would record the review twice.
func TestAPromotionTheForgeRefusesDoesNotFailTheReview(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	fixture.files.promoteErr = errors.New("gh: pull request is already ready for review")
	outcome := submitAndDecodeOutcome(t, fixture, readyReview)
	if outcome.Promoted {
		t.Fatal("a refused promotion was reported as promoted")
	}
	if fixture.files.promoteCalls != 1 {
		t.Fatalf("promote called %d time(s), want exactly one attempt", fixture.files.promoteCalls)
	}
	if verdict, count := fixture.recordedVerdict(t, fixture.author.ID); count != 1 || verdict != "ready-for-human-review" {
		t.Fatalf("recorded %q x%d, want the approval kept despite the failed promotion", verdict, count)
	}
}

// A verdict the control plane cannot read back is not an approval. This is the
// one place where treating it as one would be invisible: the change would
// simply be ready, with nothing on it saying why.
func TestVerdictsThatCannotBeReadBackPromoteNothing(t *testing.T) {
	fixture := newReviewFixture(t, "internal/runner/runner.go")
	if outcome := submitAndDecodeOutcome(t, fixture, readyReview); !outcome.Promoted {
		t.Fatal("the first approval did not promote; the test cannot tell the two reads apart")
	}
	// Corrupt what was recorded, the way a half-written row or a findings
	// encoding this build does not understand would look.
	if _, err := fixture.store.db.ExecContext(t.Context(),
		`UPDATE run_reviews SET findings='{not json'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateJob(t.Context(), "judge it again", "machinist", "judge",
		reviewTestCommand("judge", "claude-reviewer", review.RoleReviewer)); err != nil {
		t.Fatal(err)
	}
	second := reviewLease(t, fixture.store, pollRequest("worker-c", []string{"codex"}, []string{"machinist"}))
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{
		InstanceID: "worker-c", LeaseToken: second.LeaseToken, ReviewerRun: second.ID,
		PullRequest: 41, Output: readyReview,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("second review = %d: %s", recorder.Code, recorder.Body)
	}
	var outcome protocol.ReviewOutcome
	if err := json.Unmarshal(recorder.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Promoted {
		t.Fatal("an unreadable record of the verdicts was reported as an approval")
	}
	if fixture.files.promoteCalls != 1 {
		t.Fatalf("promote called %d time(s); the unreadable read asked the forge anyway", fixture.files.promoteCalls)
	}
}

// Promotion is reported, not inferred. An approval that could not be promoted
// and one that was are the same verdict, and the difference is the only thing a
// reader cannot work out for themselves.
func TestWhetherTheChangeWasPromotedIsSaidInTheAnswer(t *testing.T) {
	promoted := newReviewFixture(t, "internal/runner/runner.go")
	refused := newReviewFixture(t, "internal/runner/runner.go")
	refused.files.promoteErr = errors.New("forge unavailable")
	first := submitAndDecodeOutcome(t, promoted, readyReview)
	second := submitAndDecodeOutcome(t, refused, readyReview)
	if first.Verdict != second.Verdict {
		t.Fatalf("verdicts differ (%q, %q); the test no longer isolates promotion", first.Verdict, second.Verdict)
	}
	if !first.Promoted || second.Promoted {
		t.Fatalf("promoted = %v and %v, want the answer to tell the two apart", first.Promoted, second.Promoted)
	}
}
