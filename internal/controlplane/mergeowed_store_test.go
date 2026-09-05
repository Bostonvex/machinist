package controlplane

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
	"github.com/owainlewis/machinist/internal/review"
)

const (
	judgedHead   = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	rejudgedHead = "0f9e8d7c6b5a4938271605f4e3d2c1b0a9876543"
)

// judgementFixture is one repository with runs to record reviews against. The
// reviews are what the merge-owed read folds, so what matters is that the runs
// exist and sit in a known repository.
type judgementFixture struct {
	store *Store
	runs  []string
}

func newJudgementFixture(t *testing.T, repository string, runs int) *judgementFixture {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	fixture := &judgementFixture{store: store}
	command := reviewTestCommand("implement", "codex-implementer", review.RoleImplementer)
	for i := 0; i < runs; i++ {
		if _, err := store.CreateJob(t.Context(), "write it", repository, "implement", command); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < runs; i++ {
		run := reviewLease(t, store, pollRequest("worker-"+string(rune('a'+i)),
			[]string{"codex"}, []string{repository}))
		fixture.runs = append(fixture.runs, run.ID)
	}
	return fixture
}

func (f *judgementFixture) record(t *testing.T, run int, recorded RecordedReview) {
	t.Helper()
	recorded.RunID = f.runs[run]
	if recorded.ReviewerRunID == "" {
		recorded.ReviewerRunID = "reviewer-" + recorded.RunID
	}
	if recorded.PullRequest == 0 {
		recorded.PullRequest = 41
	}
	if recorded.Verdict == "" {
		recorded.Verdict = review.VerdictReady
	}
	if recorded.ReviewedHead == "" {
		recorded.ReviewedHead = judgedHead
	}
	if err := f.store.RecordReview(t.Context(), recorded); err != nil {
		t.Fatal(err)
	}
}

func (f *judgementFixture) read(t *testing.T, repository string) map[int]gatekeeper.Judgement {
	t.Helper()
	judgements, err := f.store.RecordedJudgements(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	return judgements
}

func TestOneReviewIsReadBackAsTheJudgementOnItsPullRequest(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{PullRequest: 41})

	judgement, found := fixture.read(t, "machinist")[41]
	if !found {
		t.Fatal("pull request 41 has no judgement")
	}
	if judgement.Verdict != review.VerdictReady {
		t.Fatalf("verdict = %q, want the recorded one", judgement.Verdict)
	}
	if judgement.ReviewedHead != judgedHead {
		t.Fatalf("head = %q, want the commit the review judged", judgement.ReviewedHead)
	}
	if judgement.RecordedAt.IsZero() {
		t.Fatal("the judgement carries no time, so nothing can say how long it has stood")
	}
}

func TestAPullRequestNobodyReviewedHasNoJudgementRatherThanAnEmptyOne(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{PullRequest: 41})

	// An empty judgement would carry an empty verdict, which reads as a verdict
	// that is not an approval -- attention owed on a change nobody has touched.
	// Absence is the fact, so absence is what is returned.
	if judgement, found := fixture.read(t, "machinist")[99]; found {
		t.Fatalf("pull request 99 came back with %#v, want no judgement at all", judgement)
	}
}

func TestOneReviewersObjectionIsNotClearedByAnothersApproval(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-a", Verdict: review.VerdictReady,
	})
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-b", Verdict: review.VerdictChangesRequested,
	})

	// The same rule the run marker publishes under. A second opinion can
	// tighten the result and never loosen it, so the order the rows arrive in
	// cannot decide whether work merges.
	if verdict := fixture.read(t, "machinist")[41].Verdict; verdict != review.VerdictChangesRequested {
		t.Fatalf("verdict = %q, want the strictest of the two", verdict)
	}
}

func TestAnObjectionIsStrictestWhicheverOrderItWasRecordedIn(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-a", Verdict: review.VerdictEscalate,
	})
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-b", Verdict: review.VerdictReady,
	})

	if verdict := fixture.read(t, "machinist")[41].Verdict; verdict != review.VerdictEscalate {
		t.Fatalf("verdict = %q, want escalate however late the approval arrived", verdict)
	}
}

func TestEveryReviewersFindingsAreOpenAndNotOnlyTheLastReviewers(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-a",
		Findings: []review.Finding{{Severity: review.SeverityHigh, Issue: "the first one"}},
	})
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-b",
		Findings: []review.Finding{{Severity: review.SeverityLow, Issue: "the second one"}},
	})

	findings := fixture.read(t, "machinist")[41].Findings
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want both reviewers' findings", findings)
	}
}

func TestTheCommitAJudgementIsBoundToComesFromTheNewestReview(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-a", ReviewedHead: judgedHead,
	})
	fixture.store.now = func() time.Time { return time.Now().Add(time.Hour) }
	fixture.record(t, 0, RecordedReview{
		PullRequest: 41, ReviewerRunID: "reviewer-b", ReviewedHead: rejudgedHead,
	})

	// A reviewer that looked at a newer commit supersedes one that looked at an
	// older one. The other direction -- a newer review of an older commit --
	// leaves the judgement bound to that older commit, which reads as stale,
	// which is the safe way to be wrong.
	if head := fixture.read(t, "machinist")[41].ReviewedHead; head != rejudgedHead {
		t.Fatalf("head = %q, want the commit the newest review judged", head)
	}
}

func TestReviewsOfOtherRepositoriesAreNotFoldedIn(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{PullRequest: 41})

	// Pull request numbers are per repository, so a judgement leaking across
	// repositories would put one repository's approval on another's change.
	if judgements := fixture.read(t, "somewhere-else"); len(judgements) != 0 {
		t.Fatalf("judgements = %#v, want none for a repository with no reviews", judgements)
	}
}

func TestTwoPullRequestsAreFoldedSeparately(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 2)
	fixture.record(t, 0, RecordedReview{PullRequest: 41, Verdict: review.VerdictReady})
	fixture.record(t, 1, RecordedReview{PullRequest: 42, Verdict: review.VerdictEscalate})

	judgements := fixture.read(t, "machinist")
	if judgements[41].Verdict != review.VerdictReady || judgements[42].Verdict != review.VerdictEscalate {
		t.Fatalf("judgements = %#v, want each pull request folded on its own rows", judgements)
	}
}

func TestAReadWithNoRepositoryIsRefusedRatherThanAnsweredWithEverything(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{PullRequest: 41})

	if _, err := fixture.store.RecordedJudgements(t.Context(), "   "); err == nil {
		t.Fatal("a read with no repository was answered, want it refused")
	}
}

func TestTheRepositoriesWithReviewsAreNamedSoACallerCanNameOne(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.record(t, 0, RecordedReview{PullRequest: 41})

	repositories, err := fixture.store.MergeOwedRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != "machinist" {
		t.Fatalf("repositories = %v, want the one that has reviews", repositories)
	}
}

func TestARepositoryWithNoReviewsIsNotOffered(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)

	repositories, err := fixture.store.MergeOwedRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 0 {
		t.Fatalf("repositories = %v, want none until something has been reviewed", repositories)
	}
}

// writeRawReview puts a row into run_reviews without going through
// RecordReview. Every check RecordReview makes is bypassed on purpose: these
// tests are about what the read does with a row that check would have refused,
// which is the shape of every row written by a build older than that check.
func (f *judgementFixture) writeRawReview(t *testing.T, verdict, findings string) {
	t.Helper()
	if _, err := f.store.db.ExecContext(t.Context(),
		`INSERT INTO run_reviews(run_id,reviewer_run_id,pull_request,verdict,findings,reviewed_head,recorded_at)
VALUES(?,?,?,?,?,?,?)`,
		f.runs[0], "reviewer-raw", 41, verdict, findings, judgedHead, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func TestAVerdictThisBuildCannotRankIsRefusedRatherThanDropped(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.writeRawReview(t, "looks-fine-to-me", "[]")

	// Dropping it would leave the pull request with no verdict at all, which
	// reads as unreviewed, and an unreadable objection must never become an
	// approval by omission. Refusing the whole read is the only answer that
	// cannot be mistaken for one.
	_, err := fixture.store.RecordedJudgements(t.Context(), "machinist")
	if err == nil {
		t.Fatal("a verdict this build cannot rank was folded, want the read refused")
	}
	if !strings.Contains(err.Error(), "verdict") {
		t.Fatalf("err = %q, want it to name the verdict as the problem", err)
	}
}

func TestFindingsThatWillNotDecodeAreRefusedRatherThanReadAsNone(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	fixture.writeRawReview(t, string(review.VerdictReady), "not json at all")

	// Read as none, this is an approval with nothing outstanding -- which is
	// exactly the merge-owed answer, arrived at because the findings could not
	// be read.
	_, err := fixture.store.RecordedJudgements(t.Context(), "machinist")
	if err == nil {
		t.Fatal("findings that will not decode were read as none, want the read refused")
	}
	if !strings.Contains(err.Error(), "findings") {
		t.Fatalf("err = %q, want it to name the findings as the problem", err)
	}
}

func TestATimeThatWillNotDecodeIsRefused(t *testing.T) {
	fixture := newJudgementFixture(t, "machinist", 1)
	if _, err := fixture.store.db.ExecContext(t.Context(),
		`INSERT INTO run_reviews(run_id,reviewer_run_id,pull_request,verdict,findings,reviewed_head,recorded_at)
VALUES(?,?,?,?,?,?,?)`,
		fixture.runs[0], "reviewer-raw", 41, string(review.VerdictReady), "[]", judgedHead, "last tuesday"); err != nil {
		t.Fatal(err)
	}

	// The time is what "standing for 3 days" is measured from. Defaulted to
	// zero it would make every such row the oldest thing on the list, which
	// puts unreadable rows at the top of the queue forever.
	if _, err := fixture.store.RecordedJudgements(t.Context(), "machinist"); err == nil {
		t.Fatal("an unreadable time was defaulted, want the read refused")
	}
}
