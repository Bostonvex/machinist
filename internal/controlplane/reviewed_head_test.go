package controlplane

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

const (
	headUnderReview = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	headMovedOn     = "0f9e8d7c6b5a4938271605f4e3d2c1b0a9876543"
)

// recordedHead reads back the commit stored against a run's review.
func recordedHead(t *testing.T, store *Store, runID string) string {
	t.Helper()
	var head string
	if err := store.db.QueryRow(
		`SELECT reviewed_head FROM run_reviews WHERE run_id=?`, runID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	return head
}

func TestAReviewRecordsTheCommitItJudged(t *testing.T) {
	fixture := newReviewFixture(t, "internal/agent/loop.go")
	fixture.files.head = headUnderReview
	if recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{}); recorder.Code != http.StatusOK {
		t.Fatalf("submit = %d: %s", recorder.Code, recorder.Body.String())
	}
	if head := recordedHead(t, fixture.store, fixture.author.ID); head != headUnderReview {
		t.Fatalf("reviewed_head = %q, want the commit the change pointed at", head)
	}
}

func TestTheCommitUnderReviewIsReadFromTheForgeNotTheSubmission(t *testing.T) {
	fixture := newReviewFixture(t, "internal/agent/loop.go")
	fixture.files.head = headUnderReview
	if recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{}); recorder.Code != http.StatusOK {
		t.Fatalf("submit = %d: %s", recorder.Code, recorder.Body.String())
	}
	// The point of asking the forge is that nobody else is asked. A reviewer
	// that could name its own head could name one it had already approved.
	if fixture.files.headCalls == 0 {
		t.Fatal("the control plane recorded a head without asking the forge for one")
	}
	if fixture.files.number != 41 {
		t.Fatalf("the head was read for pull request %d, want the one under review", fixture.files.number)
	}
}

func TestAReviewIsRefusedWhenTheCommitUnderReviewCannotBeRead(t *testing.T) {
	fixture := newReviewFixture(t, "internal/agent/loop.go")
	fixture.files.headErr = errors.New("the forge said no")
	recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{})
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("submit = %d: %s", recorder.Code, recorder.Body.String())
	}
	// Nothing recorded. A verdict that cannot be bound to a commit cannot be
	// re-checked when the branch moves, so storing it would leave an approval
	// that no later reader could invalidate.
	var stored int
	if err := fixture.store.db.QueryRow(
		`SELECT COUNT(*) FROM run_reviews WHERE run_id=?`, fixture.author.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatal("a verdict was recorded that says nothing about what it judged")
	}
}

func TestAVerdictThatNamesNoCommitIsRefusedByTheStore(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	err := store.RecordReview(t.Context(), RecordedReview{
		RunID: "run_a", ReviewerRunID: "run_b", PullRequest: 7, Verdict: review.VerdictReady,
	})
	if err == nil {
		t.Fatal("a verdict with no commit was recorded")
	}
	if !strings.Contains(err.Error(), "reviewed_head") {
		t.Fatalf("err = %q, want it to name the field that was missing", err)
	}
}

func TestAVerdictThatNamesSomethingOtherThanACommitIsRefused(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	for name, head := range map[string]string{
		// Every one of these compares unequal to a real head, so a reader would
		// call the review stale rather than wrong -- which is why the refusal
		// has to happen at the write. A stale-looking review nobody can explain
		// is indistinguishable from work that genuinely moved on.
		"a branch name":      "main",
		"an abbreviated sha": "a1b2c3d",
		"upper case":         strings.ToUpper(headUnderReview),
		"too long":           headUnderReview + "9",
		"not hex":            strings.Repeat("z", 40),
	} {
		t.Run(name, func(t *testing.T) {
			err := store.RecordReview(t.Context(), RecordedReview{
				RunID: "run_a", ReviewerRunID: "run_b", PullRequest: 7,
				Verdict: review.VerdictReady, ReviewedHead: head,
			})
			if err == nil {
				t.Fatalf("%q was accepted as the commit under review", head)
			}
			// Assert the message, not just that something failed. These runs do
			// not exist, so the write would be refused by a foreign key even if
			// nothing looked at the head at all -- and a check that is only ever
			// reached through another check can be deleted without a test
			// noticing. Naming the field is what separates the two refusals.
			if !strings.Contains(err.Error(), "reviewed_head") {
				t.Fatalf("err = %q, want it to refuse the commit rather than something else", err)
			}
		})
	}
}

func TestARereviewRecordsTheCommitItActuallyLookedAt(t *testing.T) {
	fixture := newReviewFixture(t, "internal/agent/loop.go")
	fixture.files.head = headUnderReview
	if recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{}); recorder.Code != http.StatusOK {
		t.Fatalf("first submit = %d: %s", recorder.Code, recorder.Body.String())
	}
	// The branch moves and the same reviewer looks again. Replacing its own
	// earlier review must move the commit with the verdict: a row that kept the
	// old head would describe a judgement of work that is no longer there.
	fixture.files.head = headMovedOn
	if recorder := fixture.submit(t, fixture.author.ID, protocol.ReviewSubmission{}); recorder.Code != http.StatusOK {
		t.Fatalf("second submit = %d: %s", recorder.Code, recorder.Body.String())
	}
	if head := recordedHead(t, fixture.store, fixture.author.ID); head != headMovedOn {
		t.Fatalf("reviewed_head = %q, want the commit the second review saw", head)
	}
}

func TestADatabaseWrittenBeforeTheColumnExistedIsUpgradedNotRejected(t *testing.T) {
	// The column is added to a run_reviews that is already there. The schema
	// block only creates tables that are absent, so without the upgrade an
	// existing deployment would keep the old shape and every write would fail
	// on a column that is not there.
	path := filepath.Join(t.TempDir(), "machinist.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), `CREATE TABLE run_reviews (
 run_id TEXT NOT NULL, reviewer_run_id TEXT NOT NULL, pull_request INTEGER NOT NULL,
 verdict TEXT NOT NULL, high_risk INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '',
 findings TEXT NOT NULL DEFAULT '[]', protected_paths TEXT NOT NULL DEFAULT '[]',
 reasons TEXT NOT NULL DEFAULT '[]', recorded_at TEXT NOT NULL,
 PRIMARY KEY(run_id,reviewer_run_id));
INSERT INTO run_reviews(run_id,reviewer_run_id,pull_request,verdict,recorded_at)
 VALUES('run_old','run_reviewer',7,'ready-for-human-review','2026-01-01T00:00:00Z');
PRAGMA user_version=13;`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, path)
	// The row survives, and it carries no head. That is the truth about it: it
	// was recorded when nothing observed which commit was under review. An
	// upgrade that filled the blank in -- with the current head, say -- would
	// manufacture exactly the approval this column exists to make impossible.
	if head := recordedHead(t, store, "run_old"); head != "" {
		t.Fatalf("an old row came back claiming to have reviewed %q", head)
	}
	var surviving int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM run_reviews WHERE run_id='run_old'`).Scan(&surviving); err != nil {
		t.Fatal(err)
	}
	if surviving != 1 {
		t.Fatalf("the upgrade left %d of the old reviews", surviving)
	}
}

func TestUpgradingADatabaseThatAlreadyHasTheColumnChangesNothing(t *testing.T) {
	// Opening twice is the ordinary case -- every restart does it -- and an
	// ALTER TABLE that runs a second time is an error, not a no-op.
	path := filepath.Join(t.TempDir(), "machinist.db")
	first := openTestStore(t, path)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTestStore(t, path)
	var columns int
	if err := second.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM pragma_table_info('run_reviews') WHERE name='reviewed_head'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 1 {
		t.Fatalf("run_reviews has %d reviewed_head columns", columns)
	}
}
