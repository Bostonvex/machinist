package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
)

// noRules is what the rules endpoint says about a branch nobody has protected.
const noRules = `[]`

// onePullRequest is the shape gh pr list returns, with the fields merge-owed
// decides on and nothing else.
const onePullRequest = `[{
 "number":41,"title":"feat: something","url":"https://github.com/owner/name/pull/41",
 "isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
 "headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","baseRefName":"main",
 "statusCheckRollup":[
  {"__typename":"CheckRun","name":"Linux checks","conclusion":"SUCCESS","status":"COMPLETED","startedAt":"2026-09-05T10:00:00Z","completedAt":"2026-09-05T10:05:00Z"}
 ]}]`

func readOpenChanges(t *testing.T, results ...scriptedGitHubResult) ([]gatekeeper.Change, *scriptedGitHubRunner, error) {
	t.Helper()
	cli, runner := newScriptedGitHubCLI(results...)
	changes, err := cli.OpenChanges(context.Background(), "owner/name")
	return changes, runner, err
}

func TestOpenChangesAreReadInOneCallForTheWholeRepository(t *testing.T) {
	changes, runner, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want one", changes)
	}
	// The pull requests come back in one request rather than one per change.
	// That is the whole of what pr-drain.sh contributed: its per-change REST
	// walk exhausted the hourly budget on a repository with forty of them.
	listing := strings.Join(runner.calls[0], " ")
	if !strings.Contains(listing, "pr list") || !strings.Contains(listing, "--repo owner/name") {
		t.Fatalf("first call = %q, want one listing of the whole repository", listing)
	}
	for _, field := range []string{"number", "isDraft", "mergeable", "mergeStateStatus", "headRefOid", "baseRefName", "statusCheckRollup"} {
		if !strings.Contains(listing, field) {
			t.Fatalf("first call = %q, want it to ask for %q", listing, field)
		}
	}
}

func TestEveryFieldTheJudgementDecidesOnComesBackFromTheForge(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	change := changes[0]
	if change.Number != 41 || change.Head != "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678" {
		t.Fatalf("change = %#v, want the number and head the forge reported", change)
	}
	if change.Mergeable != gatekeeper.CanMerge || change.MergeState != gatekeeper.MergeStateClean {
		t.Fatalf("change = %#v, want the forge's own words for both states", change)
	}
	if change.Repository != "owner/name" || change.URL == "" || change.Title == "" {
		t.Fatalf("change = %#v, want enough to name it to a person", change)
	}
	if len(change.Checks) != 1 || change.Checks[0].Name != "Linux checks" || !change.Checks[0].Succeeded() {
		t.Fatalf("checks = %#v, want the rollup", change.Checks)
	}
}

func TestTheBranchRulesAreReadOncePerBranchAndNotOncePerChange(t *testing.T) {
	twoOnOneBranch := strings.Replace(onePullRequest, `"number":41`, `"number":41`, 1)
	twoOnOneBranch = `[` + strings.TrimSuffix(strings.TrimPrefix(twoOnOneBranch, `[`), `]`) + `,` +
		strings.TrimSuffix(strings.TrimPrefix(strings.Replace(onePullRequest, `"number":41`, `"number":42`, 1), `[`), `]`) + `]`

	_, runner, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: twoOnOneBranch},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	// Two changes on one branch cost two requests, not three. A rules read per
	// change is the same budget mistake as a details read per change.
	if len(runner.calls) != 2 {
		t.Fatalf("made %d calls, want one listing and one rules read", len(runner.calls))
	}
}

func TestARequiredCheckIsCarriedThroughFromTheBranchRules(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: `[
 {"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"Linux checks"},{"context":"Windows checks"}]}},
 {"type":"merge_queue","parameters":{}}
]`})
	if err != nil {
		t.Fatal(err)
	}
	change := changes[0]
	if len(change.RequiredChecks) != 2 {
		t.Fatalf("required = %v, want both checks the rules name", change.RequiredChecks)
	}
	if !change.QueueRequired {
		t.Fatal("the branch has a merge queue and the change does not say so")
	}
}

func TestRequiredChecksAccumulateAcrossEveryRuleThatNamesThem(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: `[
 {"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"Linux checks"}]}},
 {"type":"pull_request","parameters":{}},
 {"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"Windows checks"},{"context":"Linux checks"}]}}
]`})
	if err != nil {
		t.Fatal(err)
	}
	// Several rulesets can each require checks. The shell's first version took
	// the first rule that matched and enforced a subset of the branch's gates,
	// so a change could be reported as merge-owed with a required check that
	// nothing had looked at.
	required := strings.Join(changes[0].RequiredChecks, ",")
	if required != "Linux checks,Windows checks" {
		t.Fatalf("required = %q, want every rule's checks, each once", required)
	}
}

func TestABranchWithNoRulesRequiresNothingRatherThanFailing(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes[0].RequiredChecks) != 0 || changes[0].QueueRequired {
		t.Fatalf("change = %#v, want nothing required on an unprotected branch", changes[0])
	}
}

func TestARequiredCheckTheRulesDoNotNameIsCarriedThroughUnnamed(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: `[
 {"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"  "}]}}
]`})
	if err != nil {
		t.Fatal(err)
	}
	// Dropping it here would turn a rule this build cannot read into a branch
	// with no rules. It is passed through instead, and the gatekeeper refuses
	// on it, so the unreadable rule is reported rather than resolved.
	if len(changes[0].RequiredChecks) != 1 || strings.TrimSpace(changes[0].RequiredChecks[0]) != "" {
		t.Fatalf("required = %#v, want the unnameable rule carried through", changes[0].RequiredChecks)
	}
}

func TestALegacyCommitStatusIsReadAsACheck(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: `[{
 "number":41,"title":"t","url":"u","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
 "headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","baseRefName":"main",
 "statusCheckRollup":[
  {"__typename":"StatusContext","context":"ci/legacy","state":"SUCCESS","createdAt":"2026-09-05T10:00:00Z"}
 ]}]`},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	// The rollup mixes two shapes: a CheckRun names itself in `name` and
	// concludes in `conclusion`, a StatusContext in `context` and `state`.
	// Reading only the first drops every commit status a repository still uses,
	// and a required check that is dropped reads as one that has not run.
	checks := changes[0].Checks
	if len(checks) != 1 || checks[0].Name != "ci/legacy" || !checks[0].Succeeded() {
		t.Fatalf("checks = %#v, want the legacy status read as a check", checks)
	}
}

func TestACheckThatHasNotFinishedCarriesNoConclusion(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: `[{
 "number":41,"title":"t","url":"u","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
 "headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","baseRefName":"main",
 "statusCheckRollup":[
  {"__typename":"CheckRun","name":"Linux checks","conclusion":"","status":"IN_PROGRESS","startedAt":"2026-09-05T10:00:00Z"}
 ]}]`},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	// The status is IN_PROGRESS, which is not a conclusion. Filling the
	// conclusion in from the status would make a running check look like one
	// that ended in a word nothing recognises, which the judgement reads as a
	// failure rather than as a wait.
	check := changes[0].Checks[0]
	if check.Finished() || check.Succeeded() {
		t.Fatalf("check = %#v, want no conclusion while it is still running", check)
	}
	if check.ReportedAt.IsZero() {
		t.Fatal("a running check has no time, so a rerun cannot be ordered against it")
	}
}

func TestTheTimeACheckReportedPrefersWhenItFinished(t *testing.T) {
	changes, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: onePullRequest},
		scriptedGitHubResult{stdout: noRules})
	if err != nil {
		t.Fatal(err)
	}
	// Ordering two runs of one check is what the time is for, and a run that
	// started earlier can finish later. The completion is what says which
	// answer is current.
	want := time.Date(2026, 9, 5, 10, 5, 0, 0, time.UTC)
	if got := changes[0].Checks[0].ReportedAt; !got.Equal(want) {
		t.Fatalf("reported at %s, want the completion time %s", got, want)
	}
}

func TestATimestampThatIsNotOneIsRefused(t *testing.T) {
	_, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: `[{
 "number":41,"title":"t","url":"u","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
 "headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","baseRefName":"main",
 "statusCheckRollup":[{"__typename":"CheckRun","name":"Linux checks","conclusion":"SUCCESS","completedAt":"last tuesday"}]}]`},
		scriptedGitHubResult{stdout: noRules})
	// Defaulted to zero, a rerun sorts behind the failure it replaced, and the
	// failure is what the judgement then reads.
	if err == nil {
		t.Fatal("an unreadable timestamp was accepted, want it refused")
	}
}

func TestACheckWithNoNameAtAllIsRefused(t *testing.T) {
	_, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: `[{
 "number":41,"title":"t","url":"u","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",
 "headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678","baseRefName":"main",
 "statusCheckRollup":[{"__typename":"CheckRun","conclusion":"SUCCESS","completedAt":"2026-09-05T10:00:00Z"}]}]`},
		scriptedGitHubResult{stdout: noRules})
	// A nameless check cannot be matched against a required one, so it can only
	// ever be silently ignored. That is a required check quietly satisfied by
	// nothing, so the read refuses instead.
	if err == nil {
		t.Fatal("a check with no name was accepted, want it refused")
	}
}

func TestAListingThatIsNotJSONIsRefused(t *testing.T) {
	_, _, err := readOpenChanges(t, scriptedGitHubResult{stdout: "gh: something went wrong"})
	if err == nil {
		t.Fatal("output that is not a listing was accepted, want it refused")
	}
	var forge *GitHubCLIError
	if !errors.As(err, &forge) || forge.Kind != GitHubCLIErrorMalformed {
		t.Fatalf("err = %v, want a malformed-output error", err)
	}
}

func TestAPullRequestWithoutANumberIsRefused(t *testing.T) {
	_, _, err := readOpenChanges(t,
		scriptedGitHubResult{stdout: `[{"number":0,"baseRefName":"main"}]`},
		scriptedGitHubResult{stdout: noRules})
	// The rules read is scripted too, so the only thing left to refuse on is
	// the number itself rather than the read running out of answers.
	//
	// Number is what a judgement is keyed on. A change numbered zero would take
	// whatever judgement was recorded against pull request zero, which is none,
	// and be reported as unreviewed forever.
	if err == nil {
		t.Fatal("a pull request with no number was accepted, want it refused")
	}
}

func TestARateLimitedReadIsRefusedAndNotRetried(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(scriptedGitHubResult{
		stderr: "gh: API rate limit exceeded for user", err: errors.New("exit status 1"),
	})
	_, err := cli.OpenChanges(context.Background(), "owner/name")

	// pr-drain.sh's rule, carried over: never retry into an exhausted bucket,
	// and never decide there is budget by asking gh api rate_limit -- that
	// endpoint's display lagged a real 403 by 23 seconds when it was measured.
	if err == nil {
		t.Fatal("a rate-limited read was answered, want it refused")
	}
	var forge *GitHubCLIError
	if !errors.As(err, &forge) || forge.Kind != GitHubCLIErrorRateLimit {
		t.Fatalf("err = %v, want it classified as a rate limit", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("made %d calls, want exactly one and no retry", len(runner.calls))
	}
}

func TestAnUnnumberedRepositoryIsRefusedBeforeTheForgeIsAsked(t *testing.T) {
	cli, runner := newScriptedGitHubCLI()
	if _, err := cli.OpenChanges(context.Background(), "name-with-no-owner"); err == nil {
		t.Fatal("a repository that is not owner/name was accepted, want it refused")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("made %d calls, want none before the name is known to be one", len(runner.calls))
	}
}

func TestBranchRulesNeedABranch(t *testing.T) {
	cli, runner := newScriptedGitHubCLI()
	if _, _, err := cli.BranchRules(context.Background(), "owner/name", "  "); err == nil {
		t.Fatal("branch rules were read for no branch, want it refused")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("made %d calls, want none", len(runner.calls))
	}
}

func TestMoreOpenChangesThanCanBeReadIsRefusedRatherThanTruncated(t *testing.T) {
	var listing strings.Builder
	listing.WriteString("[")
	for i := 1; i <= maxMergeOwedChanges+1; i++ {
		if i > 1 {
			listing.WriteString(",")
		}
		listing.WriteString(`{"number":`)
		listing.WriteString(strings.TrimSpace(itoa(i)))
		listing.WriteString(`,"baseRefName":"main","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","headRefOid":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"}`)
	}
	listing.WriteString("]")

	_, _, err := readOpenChanges(t, scriptedGitHubResult{stdout: listing.String()})
	// Truncating and answering anyway would report "nothing is owed" about
	// changes nobody looked at, which is the failure this whole command exists
	// to prevent.
	if err == nil {
		t.Fatal("a truncated listing was answered, want it refused")
	}
	if !strings.Contains(err.Error(), "smaller sets") {
		t.Fatalf("err = %q, want it to say what to do instead", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
