package gatekeeper

import (
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

// Merge-owed detection answers one question: which pull requests have been
// reviewed and are ready to land, and are still open anyway.
//
// It replaces factory-merge-owed.sh. Most of that script's 601 lines existed
// because the verdict lived in an issue comment, so finding one meant parsing
// Markdown -- blockquotes, code fences, prose restatements -- and matching it
// back to a head SHA by string search. Machinist records the verdict as a row
// that names the commit it judged, so none of that parsing survives the port.
//
// What survives is the judgement the script arrived at by getting it wrong in
// production:
//
//   - Two owing lists, never one. Work that is reviewed and clean is owed a
//     merge; work that is reviewed and stuck is owed attention. Collapsing them
//     loses the second kind, which is the kind somebody has to act on.
//   - Named states only. The forge saying UNKNOWN is the forge not having
//     decided yet, and reading that as ready turned silence into merge-owed.
//   - Newest run per check name. Rollup order is not chronological, so an older
//     run of the same check can otherwise shadow the one that applies.
//   - Closed world on finding grades. A grade that is not explicitly advisory
//     is open, including one this build has never heard of.
//
// Nothing here calls a forge, in keeping with the rest of this package: the
// caller supplies what the forge said and what the control plane recorded, and
// this decides. A judgement with no credential attached is one that can be
// tested exhaustively.

// Disposition is what is owed on one pull request.
type Disposition string

const (
	// DispositionMerge means reviewed, approved at the commit that is actually
	// there, landable, and nothing outstanding. Somebody owes it a merge.
	DispositionMerge Disposition = "merge-owed"
	// DispositionAttention means somebody reviewed it and something now needs a
	// decision: open findings, a verdict that is not an approval, an approval
	// of a commit that is no longer there, or an approval of work that does not
	// pass its own required checks.
	DispositionAttention Disposition = "attention-owed"
	// DispositionWaiting means nothing is owed by this detector yet -- it is a
	// draft, nobody has reviewed it, or the forge has not finished deciding.
	// It is a reported disposition rather than an omission, because "there is
	// nothing to do here" is only useful with the word "because" after it.
	DispositionWaiting Disposition = "waiting"
)

// Mergeability is the forge's answer to whether a change conflicts with its
// base: MERGEABLE, CONFLICTING, or UNKNOWN while it works that out.
type Mergeability string

const (
	CanMerge      Mergeability = "MERGEABLE"
	Conflicts     Mergeability = "CONFLICTING"
	StillDeciding Mergeability = "UNKNOWN"
)

// MergeState is the forge's separate answer to whether the branch protections
// are satisfied: CLEAN, BEHIND, BLOCKED, UNSTABLE, and others it may grow.
//
// It is carried as a string rather than parsed into a bool because the set is
// the forge's to extend, and this package refuses the states it does not know
// rather than guessing which side of the line a new one falls on.
type MergeState string

const (
	MergeStateClean    MergeState = "CLEAN"
	MergeStateBehind   MergeState = "BEHIND"
	MergeStateBlocked  MergeState = "BLOCKED"
	MergeStateUnstable MergeState = "UNSTABLE"
	MergeStateUnknown  MergeState = "UNKNOWN"
)

// landableStates are the states a change may be merged from directly.
var landableStates = map[MergeState]struct{}{
	MergeStateClean: {},
}

// enqueueableStates are the states a change may be added to a merge queue from.
// They are wider than landableStates because the queue is what resolves BEHIND
// and BLOCKED, and they stop well short of "not obviously broken": UNKNOWN is
// deliberately absent. The forge has not reconciled the change yet, and reading
// that as enqueueable is how silence became merge-owed.
var enqueueableStates = map[MergeState]struct{}{
	MergeStateClean: {}, MergeStateBehind: {}, MergeStateBlocked: {},
}

// Check is one status check reported against a commit.
type Check struct {
	Name string
	// Conclusion is the forge's word: SUCCESS, FAILURE, CANCELLED, and so on. A
	// check that has not finished has no conclusion, which is not a failure and
	// is not a success either.
	Conclusion string
	// ReportedAt orders two runs of the same check. The rollup does not arrive
	// in chronological order, so without this an older run can shadow the one
	// that applies.
	ReportedAt time.Time
}

// Succeeded reports whether the check passed. The comparison is
// case-insensitive because the forge's REST and GraphQL surfaces disagree on
// the case of the same word, and a green check read as pending is a merge that
// never gets reported as owed.
func (c Check) Succeeded() bool {
	return strings.EqualFold(strings.TrimSpace(c.Conclusion), "SUCCESS")
}

// Finished reports whether the check reached any conclusion at all.
func (c Check) Finished() bool {
	return strings.TrimSpace(c.Conclusion) != ""
}

// Change is what the forge says about one open pull request.
type Change struct {
	Repository string
	Number     int
	Title      string
	URL        string
	// Head is the commit the change currently points at. A recorded verdict is
	// checked against this and nothing else.
	Head  string
	Draft bool
	// Mergeable is the conflict answer; MergeState is the protections answer.
	// They are separate fields because they are separate questions, and a
	// change can be MERGEABLE and BLOCKED at the same time.
	Mergeable  Mergeability
	MergeState MergeState
	Checks     []Check
	// RequiredChecks are the checks the branch's rules demand, accumulated
	// across every ruleset that matches the base branch rather than the first.
	// An empty set means the branch requires none, which is a fact about the
	// branch and not a licence to ignore the checks it does have.
	RequiredChecks []string
	// QueueRequired reports whether merges into the base branch go through a
	// merge queue, which widens the states a change may leave in.
	QueueRequired bool
}

// Judgement is what the control plane recorded about a change.
type Judgement struct {
	Verdict review.Verdict
	// ReviewedHead is the commit the reviewer actually looked at. An empty
	// value is a row written before verdicts named their commit, and is never
	// read as matching whatever is there now.
	ReviewedHead string
	Findings     []review.Finding
	RecordedAt   time.Time
}

// Standing is what is owed on one change, and why.
type Standing struct {
	Change      Change
	Disposition Disposition
	// Reason is one sentence a person can act on. It is always set, for every
	// disposition including DispositionWaiting.
	Reason string
	// OpenFindings names the grades standing in the way, when that is what is
	// in the way. It is empty otherwise.
	OpenFindings []review.Severity
	// Owed is how long this has stood, measured from when the verdict was
	// recorded. It is zero when no verdict exists, which is also the only case
	// in which no verdict is the reason.
	Owed time.Duration
}

// advisorySeverities are the grades that do not stand in the way of a merge.
//
// This is the only list, and openSeverities derives the blocking set from it by
// subtraction. Naming the blocking grades instead would mean a severity added
// to review's ladder silently became advisory here, which is the direction that
// makes an old build cheerful about a new kind of defect.
var advisorySeverities = map[review.Severity]struct{}{
	review.SeverityInfo: {},
}

// openSeverities returns the grades of finding that stand in the way, in the
// order the reviewer listed them, deduplicated.
//
// The order is the reviewer's rather than a ranking of this package's own: the
// ladder belongs to internal/review, and restating it here would give two
// places to change it and one of them would be missed. A grade this build does
// not recognise at all is open, for the same reason an unreadable head is not a
// match -- an unreadable judgement is not a favourable one.
func openSeverities(findings []review.Finding) []review.Severity {
	var open []review.Severity
	seen := make(map[review.Severity]struct{}, len(findings))
	for _, finding := range findings {
		severity := finding.Severity
		if _, advisory := advisorySeverities[severity]; advisory {
			continue
		}
		if _, already := seen[severity]; already {
			continue
		}
		seen[severity] = struct{}{}
		open = append(open, severity)
	}
	return open
}

// checkOutcome is what the required checks say collectively.
type checkOutcome int

const (
	checksAllPassed checkOutcome = iota
	// checksPending means a required check has not reached a conclusion. Nobody
	// is owed anything yet; the answer is coming.
	checksPending
	// checksFailed means a required check reached a conclusion that is not
	// success, or reached two that cannot be told apart. Somebody approved work
	// that does not pass its own gate, which is a decision, not a wait.
	checksFailed
)

// requiredChecks reports what the branch's required checks say about this
// commit, keeping the newest run of each name.
//
// Two runs of the same required check that report at the same instant and
// disagree are refused rather than resolved. There is no fact here about which
// one applies, and picking whichever the forge happened to list last is a
// coin toss that reads as a green light half the time.
func requiredChecks(change Change) (checkOutcome, string) {
	newest := make(map[string]Check, len(change.Checks))
	ambiguous := make(map[string]bool, len(change.Checks))
	for _, check := range change.Checks {
		name := strings.TrimSpace(check.Name)
		previous, seen := newest[name]
		switch {
		case !seen:
			newest[name] = check
		case check.ReportedAt.After(previous.ReportedAt):
			newest[name] = check
			ambiguous[name] = false
		case check.ReportedAt.Equal(previous.ReportedAt) && check.Succeeded() != previous.Succeeded():
			ambiguous[name] = true
		}
	}
	for _, required := range change.RequiredChecks {
		name := strings.TrimSpace(required)
		if name == "" {
			// The branch rules named a check and this build cannot tell which
			// one. Skipping it would mean a gate silently stopped gating, so
			// the unreadable rule is what gets reported.
			return checksFailed, "the branch rules require a check with no name"
		}
		check, ran := newest[name]
		if !ran {
			return checksPending, "required check " + name + " has not run"
		}
		if ambiguous[name] {
			return checksFailed, "two runs of required check " + name + " report at the same instant and disagree"
		}
		if !check.Finished() {
			return checksPending, "required check " + name + " has not finished"
		}
		if !check.Succeeded() {
			return checksFailed, "required check " + name + " is " + strings.TrimSpace(check.Conclusion)
		}
	}
	return checksAllPassed, ""
}

// Owed decides what is owed on one change, given what the forge says and what
// the control plane recorded. A nil judgement means nothing was recorded.
//
// The review is settled before landability. This detector exists to find work
// that has been reviewed, so the standing of a change nobody has looked at is
// never reported in terms of its merge state -- and, the other way round, an
// approval that has gone stale is reported as such rather than being hidden
// behind a transient BEHIND.
func Owed(change Change, judgement *Judgement, now time.Time) Standing {
	standing := Standing{Change: change, Disposition: DispositionWaiting}
	if change.Draft {
		standing.Reason = "it is a draft"
		return standing
	}
	if judgement == nil {
		standing.Reason = "no verdict has been recorded"
		return standing
	}
	if owed := now.Sub(judgement.RecordedAt); owed > 0 {
		standing.Owed = owed
	}
	// A verdict recorded against another commit is not a verdict on this one.
	// The change is reported rather than dropped -- the shell dropped it --
	// because it looks finished, it is otherwise ready, and the only thing
	// between it and landing on a stale approval is somebody noticing.
	reviewed := strings.TrimSpace(judgement.ReviewedHead)
	head := strings.TrimSpace(change.Head)
	switch {
	case reviewed == "":
		standing.Disposition = DispositionAttention
		standing.Reason = "its verdict was recorded before verdicts named a commit, so it cannot be checked against the head"
		return standing
	case head == "":
		standing.Disposition = DispositionAttention
		standing.Reason = "the forge did not say which commit it is on, so its verdict cannot be checked"
		return standing
	case !strings.EqualFold(reviewed, head):
		standing.Disposition = DispositionAttention
		standing.Reason = "it was reviewed at " + shortCommit(reviewed) + " and the head is now " + shortCommit(head)
		return standing
	}
	if judgement.Verdict != review.VerdictReady {
		verdict := strings.TrimSpace(string(judgement.Verdict))
		if verdict == "" {
			verdict = "not recorded"
		}
		standing.Disposition = DispositionAttention
		standing.Reason = "its verdict is " + verdict
		return standing
	}
	if open := openSeverities(judgement.Findings); len(open) > 0 {
		standing.Disposition = DispositionAttention
		standing.OpenFindings = open
		standing.Reason = "it was approved at " + shortCommit(head) + " with open findings: " + joinSeverities(open)
		return standing
	}
	// Approved at the commit that is there, with nothing outstanding. What is
	// left is whether it can land.
	if change.Mergeable == Conflicts {
		standing.Disposition = DispositionAttention
		standing.Reason = "it was approved at " + shortCommit(head) + " and now conflicts with its base"
		return standing
	}
	if change.Mergeable != CanMerge {
		mergeable := strings.TrimSpace(string(change.Mergeable))
		if mergeable == "" {
			mergeable = "nothing"
		}
		standing.Reason = "the forge has not called it mergeable, it says " + mergeable
		return standing
	}
	switch outcome, why := requiredChecks(change); outcome {
	case checksFailed:
		standing.Disposition = DispositionAttention
		standing.Reason = "it was approved at " + shortCommit(head) + " but " + why
		return standing
	case checksPending:
		standing.Reason = why
		return standing
	}
	allowed := landableStates
	if change.QueueRequired {
		allowed = enqueueableStates
	}
	if _, ok := allowed[change.MergeState]; !ok {
		state := strings.TrimSpace(string(change.MergeState))
		if state == "" {
			state = "nothing"
		}
		standing.Reason = "its merge state is " + state
		return standing
	}
	standing.Disposition = DispositionMerge
	standing.Reason = "reviewed at " + shortCommit(head) + " with nothing outstanding"
	return standing
}

// OwedAcross decides every change and orders the result: what is owed first,
// longest-standing first within that.
//
// Waiting changes stay in the result rather than being filtered out here. A
// caller that wants only the two owing lists can take them; a caller that drops
// the rest does so knowingly, which is not the same as never being told.
func OwedAcross(changes []Change, judgements map[int]Judgement, now time.Time) []Standing {
	standings := make([]Standing, 0, len(changes))
	for _, change := range changes {
		var judgement *Judgement
		if recorded, found := judgements[change.Number]; found {
			recorded := recorded
			judgement = &recorded
		}
		standings = append(standings, Owed(change, judgement, now))
	}
	rank := map[Disposition]int{
		DispositionMerge: 0, DispositionAttention: 1, DispositionWaiting: 2,
	}
	sort.SliceStable(standings, func(i, j int) bool {
		left, right := standings[i], standings[j]
		if rank[left.Disposition] != rank[right.Disposition] {
			return rank[left.Disposition] < rank[right.Disposition]
		}
		if left.Owed != right.Owed {
			return left.Owed > right.Owed
		}
		if left.Change.Repository != right.Change.Repository {
			return left.Change.Repository < right.Change.Repository
		}
		return left.Change.Number < right.Change.Number
	})
	return standings
}

func joinSeverities(severities []review.Severity) string {
	names := make([]string, 0, len(severities))
	for _, severity := range severities {
		name := strings.TrimSpace(string(severity))
		if name == "" {
			name = "an unnamed grade"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// shortCommit abbreviates for a message a person reads. Nothing compares
// abbreviations: the equality above is on the full value, because two commits
// that share seven characters are two commits.
func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) <= 7 {
		return commit
	}
	return commit[:7]
}
