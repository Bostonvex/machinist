package gatekeeper

import (
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

const (
	commitUnderReview = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	commitPushedSince = "0f9e8d7c6b5a4938271605f4e3d2c1b0a9876543"
)

var reviewedAt = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
var checkedAt = time.Now()

// readyChange is a pull request with nothing wrong with it. Every test below
// starts here and breaks one thing, so that what the test is about is the line
// that changes rather than the twenty fields that do not.
func readyChange() Change {
	return Change{
		Repository:     "Bostonvex/machinist",
		Number:         41,
		Title:          "feat: something",
		URL:            "https://github.com/Bostonvex/machinist/pull/41",
		Head:           commitUnderReview,
		Mergeable:      CanMerge,
		MergeState:     MergeStateClean,
		RequiredChecks: []string{"Linux checks"},
		Checks: []Check{
			{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
		},
	}
}

// approvedJudgement is a recorded verdict that approves readyChange as it
// stands.
func approvedJudgement() *Judgement {
	return &Judgement{
		Verdict:      review.VerdictReady,
		ReviewedHead: commitUnderReview,
		RecordedAt:   reviewedAt,
	}
}

func standing(t *testing.T, change Change, judgement *Judgement) Standing {
	t.Helper()
	return Owed(change, judgement, reviewedAt.Add(3*time.Hour))
}

// assertOwed checks the disposition and the reason together. The reason is
// asserted throughout this file rather than only the disposition, because
// several of these paths reach the same disposition and a check that is only
// ever reached through another check can be deleted without a test noticing.
func assertOwed(t *testing.T, got Standing, want Disposition, says string) {
	t.Helper()
	if got.Disposition != want {
		t.Fatalf("disposition = %q (%s), want %q", got.Disposition, got.Reason, want)
	}
	if !strings.Contains(got.Reason, says) {
		t.Fatalf("reason = %q, want it to mention %q", got.Reason, says)
	}
}

func TestWorkApprovedAtItsCurrentCommitIsOwedAMerge(t *testing.T) {
	got := standing(t, readyChange(), approvedJudgement())
	assertOwed(t, got, DispositionMerge, "nothing outstanding")
	if got.Owed != 3*time.Hour {
		t.Fatalf("owed = %s, want 3h measured from when the verdict was recorded", got.Owed)
	}
	if len(got.OpenFindings) != 0 {
		t.Fatalf("open findings = %v, want none", got.OpenFindings)
	}
}

func TestAnApprovalOfACommitThatHasBeenPushedOverIsOwedAttention(t *testing.T) {
	change := readyChange()
	change.Head = commitPushedSince

	got := standing(t, change, approvedJudgement())

	// The shell dropped this case silently, which is how a stale approval got
	// merged. It is merge-ready in every other respect, so the only thing
	// standing between it and landing on a review of work nobody looked at is
	// somebody being told.
	assertOwed(t, got, DispositionAttention, "reviewed at a1b2c3d")
	if !strings.Contains(got.Reason, "0f9e8d7") {
		t.Fatalf("reason = %q, want it to name the commit that is there now", got.Reason)
	}
}

func TestAnApprovalIsCheckedAgainstTheWholeCommitAndNotAnAbbreviation(t *testing.T) {
	change := readyChange()
	// Same first seven characters, different commit. The shell matched a
	// 7-character prefix, which is what makes this worth a test of its own.
	change.Head = commitUnderReview[:7] + "ffffffffffffffffffffffffffffffffff"

	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "and the head is now")
}

func TestAVerdictRecordedBeforeVerdictsNamedACommitIsOwedAttention(t *testing.T) {
	judgement := approvedJudgement()
	judgement.ReviewedHead = ""

	// Rows written before schema 14 carry no head. They are not approvals of
	// the current commit; they are approvals of an unknown one.
	assertOwed(t, standing(t, readyChange(), judgement), DispositionAttention, "before verdicts named a commit")
}

func TestAChangeWhoseCommitTheForgeDidNotGiveUpIsOwedAttention(t *testing.T) {
	change := readyChange()
	change.Head = "  "

	// An empty head compared against an empty reviewed head matches, and the
	// whole point of the column is that it must not. Both sides are refused.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "did not say which commit")
}

func TestAVerdictThatIsNotAnApprovalIsOwedAttention(t *testing.T) {
	for name, verdict := range map[string]review.Verdict{
		"changes requested": review.VerdictChangesRequested,
		"escalated":         review.VerdictEscalate,
		"a verdict this build has never heard of": review.Verdict("looks-fine-to-me"),
		"no verdict at all":                       review.Verdict(""),
	} {
		t.Run(name, func(t *testing.T) {
			judgement := approvedJudgement()
			judgement.Verdict = verdict

			// Only VerdictReady is an approval. Everything else, including a
			// word this build does not recognise, is somebody's decision to
			// make -- a verdict that cannot be read is not a favourable one.
			got := standing(t, readyChange(), judgement)
			if got.Disposition != DispositionAttention {
				t.Fatalf("disposition = %q (%s), want attention", got.Disposition, got.Reason)
			}
			if !strings.Contains(got.Reason, "verdict is") {
				t.Fatalf("reason = %q, want it to name the verdict", got.Reason)
			}
		})
	}
}

func TestAnApprovalWithOpenFindingsIsOwedAttentionAndNamesTheGrades(t *testing.T) {
	judgement := approvedJudgement()
	judgement.Findings = []review.Finding{
		{Severity: review.SeverityInfo, Issue: "a note"},
		{Severity: review.SeverityHigh, Issue: "a real one"},
		{Severity: review.SeverityInfo, Issue: "another note"},
		{Severity: review.SeverityHigh, Issue: "the same grade again"},
	}

	got := standing(t, readyChange(), judgement)

	assertOwed(t, got, DispositionAttention, "open findings: high")
	if len(got.OpenFindings) != 1 || got.OpenFindings[0] != review.SeverityHigh {
		t.Fatalf("open findings = %v, want [high] once", got.OpenFindings)
	}
}

func TestAnApprovalWithOnlyAdvisoryFindingsIsStillOwedAMerge(t *testing.T) {
	judgement := approvedJudgement()
	judgement.Findings = []review.Finding{{Severity: review.SeverityInfo, Issue: "a note"}}

	assertOwed(t, standing(t, readyChange(), judgement), DispositionMerge, "nothing outstanding")
}

func TestAFindingGradeThisBuildDoesNotRecogniseStandsInTheWay(t *testing.T) {
	judgement := approvedJudgement()
	judgement.Findings = []review.Finding{{Severity: review.Severity("catastrophic"), Issue: "?"}}

	// The blocking set is derived by subtracting the advisory grades, so a
	// grade added to review's ladder blocks until somebody deliberately says it
	// is advisory. The other direction would make an old build cheerful about a
	// new kind of defect.
	assertOwed(t, standing(t, readyChange(), judgement), DispositionAttention, "catastrophic")
}

func TestEveryGradeOnTheLadderExceptInfoStandsInTheWay(t *testing.T) {
	// Derived from the ladder rather than restating it: if review grows a
	// severity, this test covers it without being edited, which is the only way
	// the closed world above stays closed.
	for _, severity := range []review.Severity{
		review.SeverityBlocker, review.SeverityHigh,
		review.SeverityMedium, review.SeverityLow, review.SeverityInfo,
	} {
		t.Run(string(severity), func(t *testing.T) {
			judgement := approvedJudgement()
			judgement.Findings = []review.Finding{{Severity: severity}}

			got := standing(t, readyChange(), judgement)
			want := DispositionAttention
			if severity == review.SeverityInfo {
				want = DispositionMerge
			}
			if got.Disposition != want {
				t.Fatalf("%s: disposition = %q (%s), want %q", severity, got.Disposition, got.Reason, want)
			}
		})
	}
}

func TestADraftIsNotOwedAnythingEvenWhenItIsApproved(t *testing.T) {
	change := readyChange()
	change.Draft = true

	assertOwed(t, standing(t, change, approvedJudgement()), DispositionWaiting, "it is a draft")
}

func TestWorkNobodyHasReviewedIsNotOwedAMergeOrAttention(t *testing.T) {
	got := standing(t, readyChange(), nil)

	// Finding unreviewed work is the review assigner's job. Reporting it here
	// as attention-owed would send somebody to look at a queue that is already
	// being worked, and would drown the changes that actually need a decision.
	assertOwed(t, got, DispositionWaiting, "no verdict has been recorded")
	if got.Owed != 0 {
		t.Fatalf("owed = %s, want zero when there is no verdict to measure from", got.Owed)
	}
}

func TestAnApprovalOnWorkThatNowConflictsIsOwedAttention(t *testing.T) {
	change := readyChange()
	change.Mergeable = Conflicts

	// This is not a wait. Resolving the conflict moves the head, which throws
	// the approval away, so somebody has to know the approval is about to stop
	// meaning anything.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "conflicts with its base")
}

func TestAForgeThatHasNotDecidedYetIsNeverReadUpAsReady(t *testing.T) {
	for name, mergeable := range map[string]Mergeability{
		"still deciding":              StillDeciding,
		"a word this build knows not": Mergeability("BEHIND_MAYBE"),
		"nothing at all":              Mergeability(""),
	} {
		t.Run(name, func(t *testing.T) {
			change := readyChange()
			change.Mergeable = mergeable

			// Bug #553 in the shell: the not-DIRTY test read UNKNOWN as
			// enqueueable, so the forge not having finished became merge-owed.
			// Only the forge saying MERGEABLE is the forge saying mergeable.
			assertOwed(t, standing(t, change, approvedJudgement()), DispositionWaiting, "has not called it mergeable")
		})
	}
}

func TestOnlyACleanBranchLandsWithoutAMergeQueue(t *testing.T) {
	for name, state := range map[string]MergeState{
		"behind its base":              MergeStateBehind,
		"blocked by protections":       MergeStateBlocked,
		"unstable":                     MergeStateUnstable,
		"unknown":                      MergeStateUnknown,
		"a state this build knows not": MergeState("QUEUED"),
		"nothing at all":               MergeState(""),
	} {
		t.Run(name, func(t *testing.T) {
			change := readyChange()
			change.MergeState = state

			// Without a queue, nothing resolves BEHIND or BLOCKED on the way
			// in, so reporting them as merge-owed sends somebody to press a
			// button the forge will refuse.
			assertOwed(t, standing(t, change, approvedJudgement()), DispositionWaiting, "merge state is")
		})
	}
}

func TestAMergeQueueWidensTheStatesWorkMayLeaveIn(t *testing.T) {
	for name, expectation := range map[string]struct {
		state MergeState
		want  Disposition
	}{
		"clean":                        {MergeStateClean, DispositionMerge},
		"behind is the queue's to fix": {MergeStateBehind, DispositionMerge},
		"blocked is too":               {MergeStateBlocked, DispositionMerge},
		"unknown is nobody's":          {MergeStateUnknown, DispositionWaiting},
		"unstable is not the queue's":  {MergeStateUnstable, DispositionWaiting},
	} {
		t.Run(name, func(t *testing.T) {
			change := readyChange()
			change.QueueRequired = true
			change.MergeState = expectation.state

			got := standing(t, change, approvedJudgement())
			if got.Disposition != expectation.want {
				t.Fatalf("disposition = %q (%s), want %q", got.Disposition, got.Reason, expectation.want)
			}
		})
	}
}

func TestARequiredCheckThatHasNotRunIsNotAPass(t *testing.T) {
	change := readyChange()
	change.RequiredChecks = []string{"Linux checks", "Windows checks"}

	// The forge reports a check that never started as an absence rather than a
	// failure, and an absence read as a pass is a gate that never gates.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionWaiting, "Windows checks has not run")
}

func TestARequiredCheckStillRunningIsAWaitAndNotADecision(t *testing.T) {
	change := readyChange()
	change.Checks = []Check{{Name: "Linux checks", Conclusion: "", ReportedAt: checkedAt}}

	assertOwed(t, standing(t, change, approvedJudgement()), DispositionWaiting, "has not finished")
}

func TestApprovedWorkThatFailsItsOwnRequiredCheckIsOwedAttention(t *testing.T) {
	change := readyChange()
	change.Checks = []Check{{Name: "Linux checks", Conclusion: "FAILURE", ReportedAt: checkedAt}}

	// Somebody approved work that does not pass its own gate. That is a
	// decision waiting to be made, not a wait for a result that is coming.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "Linux checks is FAILURE")
}

func TestACheckThatEndedInAnythingOtherThanSuccessIsNotAPass(t *testing.T) {
	for _, conclusion := range []string{"FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "NEUTRAL", "SKIPPED", "STALE"} {
		t.Run(conclusion, func(t *testing.T) {
			change := readyChange()
			change.Checks = []Check{{Name: "Linux checks", Conclusion: conclusion, ReportedAt: checkedAt}}

			assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, conclusion)
		})
	}
}

func TestSuccessIsRecognisedWhateverCaseTheForgeReportsItIn(t *testing.T) {
	for _, conclusion := range []string{"SUCCESS", "success", "Success"} {
		t.Run(conclusion, func(t *testing.T) {
			change := readyChange()
			change.Checks = []Check{{Name: "Linux checks", Conclusion: conclusion, ReportedAt: checkedAt}}

			// The REST and GraphQL surfaces disagree on the case of the same
			// word, and a green check read as pending is a merge never reported.
			assertOwed(t, standing(t, change, approvedJudgement()), DispositionMerge, "nothing outstanding")
		})
	}
}

func TestTheNewestRunOfACheckIsTheOneThatCounts(t *testing.T) {
	for name, order := range map[string][]Check{
		"the rerun listed last": {
			{Name: "Linux checks", Conclusion: "FAILURE", ReportedAt: checkedAt.Add(-time.Hour)},
			{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
		},
		"the rerun listed first": {
			{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
			{Name: "Linux checks", Conclusion: "FAILURE", ReportedAt: checkedAt.Add(-time.Hour)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			change := readyChange()
			change.Checks = order

			// Rollup order is not chronological. Taking whichever run the forge
			// listed last lets a stale failure hide a passing rerun, and the
			// other way round.
			assertOwed(t, standing(t, change, approvedJudgement()), DispositionMerge, "nothing outstanding")
		})
	}
}

func TestTwoRunsOfACheckThatCannotBeToldApartAreRefused(t *testing.T) {
	change := readyChange()
	change.Checks = []Check{
		{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
		{Name: "Linux checks", Conclusion: "FAILURE", ReportedAt: checkedAt},
	}

	// There is no fact here about which run applies. Picking the one the forge
	// happened to list last is a coin toss that reads as a green light half the
	// time, so the disagreement itself is what gets reported.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "at the same instant and disagree")
}

func TestTwoRunsOfACheckThatAgreeAreNotAmbiguous(t *testing.T) {
	change := readyChange()
	change.Checks = []Check{
		{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
		{Name: "Linux checks", Conclusion: "SUCCESS", ReportedAt: checkedAt},
	}

	// The forge reports the same run twice often enough that refusing on the
	// timestamp alone would make a clean branch permanently unmergeable. What
	// is refused is a disagreement nothing can settle, not a duplicate.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionMerge, "nothing outstanding")
}

func TestABranchRuleThatNamesNoCheckIsRefusedRatherThanSkipped(t *testing.T) {
	change := readyChange()
	change.RequiredChecks = []string{"Linux checks", "   "}

	// A required check this build cannot name is a gate it cannot verify.
	// Skipping it would let an unreadable rule quietly stop gating, which is
	// the same failure as reading an absent check as a pass.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionAttention, "require a check with no name")
}

func TestChecksNobodyRequiredDoNotStopAMerge(t *testing.T) {
	change := readyChange()
	change.Checks = append(change.Checks, Check{Name: "optional linting", Conclusion: "FAILURE", ReportedAt: checkedAt})

	// The required set is the branch's, and a check outside it failing is not
	// this detector's business to enforce.
	assertOwed(t, standing(t, change, approvedJudgement()), DispositionMerge, "nothing outstanding")
}

func TestABranchThatRequiresNoChecksIsNotHeldUpByOneThatFailed(t *testing.T) {
	change := readyChange()
	change.RequiredChecks = nil
	change.Checks = []Check{{Name: "Linux checks", Conclusion: "FAILURE", ReportedAt: checkedAt}}

	assertOwed(t, standing(t, change, approvedJudgement()), DispositionMerge, "nothing outstanding")
}

func TestAVerdictRecordedInTheFutureIsNotOwedNegativeTime(t *testing.T) {
	judgement := approvedJudgement()
	judgement.RecordedAt = reviewedAt.Add(9 * time.Hour)

	got := standing(t, readyChange(), judgement)
	if got.Owed != 0 {
		t.Fatalf("owed = %s, want zero rather than a negative age that sorts to the bottom", got.Owed)
	}
}

func TestTheOwingWorkIsListedBeforeTheWaitingWorkAndTheOldestFirst(t *testing.T) {
	oldMerge := readyChange()
	oldMerge.Number = 1
	newMerge := readyChange()
	newMerge.Number = 2
	attention := readyChange()
	attention.Number = 3
	attention.Head = commitPushedSince
	waiting := readyChange()
	waiting.Number = 4

	now := reviewedAt.Add(10 * time.Hour)
	standings := OwedAcross(
		[]Change{waiting, newMerge, attention, oldMerge},
		map[int]Judgement{
			1: {Verdict: review.VerdictReady, ReviewedHead: commitUnderReview, RecordedAt: reviewedAt},
			2: {Verdict: review.VerdictReady, ReviewedHead: commitUnderReview, RecordedAt: reviewedAt.Add(5 * time.Hour)},
			3: {Verdict: review.VerdictReady, ReviewedHead: commitUnderReview, RecordedAt: reviewedAt},
		},
		now,
	)

	var order []int
	for _, s := range standings {
		order = append(order, s.Change.Number)
	}
	if len(order) != 4 || order[0] != 1 || order[1] != 2 || order[2] != 3 || order[3] != 4 {
		t.Fatalf("order = %v, want merge-owed oldest first, then attention, then waiting", order)
	}
}

func TestNothingIsDroppedFromTheListing(t *testing.T) {
	changes := []Change{readyChange(), readyChange(), readyChange()}
	changes[1].Number, changes[2].Number = 42, 43

	standings := OwedAcross(changes, nil, reviewedAt)

	// Every change comes back with a disposition and a reason, including the
	// ones nothing is owed on. A detector that reports only what it found gives
	// a caller no way to tell "I looked and there was nothing" from "I did not
	// look", and the second one is how the shell lost work.
	if len(standings) != len(changes) {
		t.Fatalf("got %d standings for %d changes", len(standings), len(changes))
	}
	for _, got := range standings {
		if got.Disposition == "" || got.Reason == "" {
			t.Fatalf("change %d came back as %q with reason %q", got.Change.Number, got.Disposition, got.Reason)
		}
	}
}

func TestAJudgementForAnotherChangeIsNotAppliedToThisOne(t *testing.T) {
	change := readyChange()
	change.Number = 41

	standings := OwedAcross([]Change{change}, map[int]Judgement{
		99: {Verdict: review.VerdictReady, ReviewedHead: commitUnderReview, RecordedAt: reviewedAt},
	}, reviewedAt)

	if len(standings) != 1 {
		t.Fatalf("got %d standings", len(standings))
	}
	assertOwed(t, standings[0], DispositionWaiting, "no verdict has been recorded")
}
