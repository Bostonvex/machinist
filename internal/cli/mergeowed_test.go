package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mergeOwedControlPlane answers the merge-owed route with whatever the test
// wants it to say. The judgement itself is tested where it lives; what is
// tested here is that a person reading the output is told the truth about it.
func mergeOwedControlPlane(t *testing.T, answer string, status ...int) string {
	t.Helper()
	code := http.StatusOK
	if len(status) == 1 {
		code = status[0]
	}
	web := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/merge-owed" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(code)
		_, _ = response.Write([]byte(answer))
	}))
	t.Cleanup(web.Close)

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(directory, "worker.toml")
	body := "[control_plane]\nurl = " + strconv.Quote(web.URL) + "\ntoken_file = \"token\"\n"
	if err := os.WriteFile(workerPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return workerPath
}

func runMergeOwed(t *testing.T, answer string, args ...string) (string, string, int) {
	t.Helper()
	return runMergeOwedAgainst(t, mergeOwedControlPlane(t, answer), args...)
}

func runMergeOwedAgainst(t *testing.T, workerPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(),
		append([]string{"merge-owed", "--config", workerPath, "--repository", "machinist"}, args...),
		strings.NewReader(""), &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), code
}

func mergeOwedAnswer(changes ...string) string {
	return `{"repository":"Bostonvex/machinist","read_at":"2026-09-05T12:00:00Z","changes":[` +
		strings.Join(changes, ",") + `]}`
}

func owedChange(number int, disposition, reason string, owedSeconds int) string {
	return `{"repository":"Bostonvex/machinist","number":` + strconv.Itoa(number) +
		`,"title":"feat: a thing","url":"u","head":"a1b2c3d4","disposition":"` + disposition +
		`","reason":"` + reason + `","owed_seconds":` + strconv.Itoa(owedSeconds) + `}`
}

func TestWorkOwedAMergeIsPrintedAndSaidInTheExitCode(t *testing.T) {
	stdout, stderr, code := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "merge-owed", "approved at its current commit", 7200)))

	// The exit code is what the cron entry reads: 0 nothing owed, 1 something
	// owed. It is carried over from the shell so those entries keep working.
	if code != 1 {
		t.Fatalf("exit %d, want 1 when a merge is owed: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "#41") || !strings.Contains(stdout, "approved at its current commit") {
		t.Fatalf("stdout = %q, want the change and why it is owed", stdout)
	}
	if !strings.Contains(stdout, "OWED A MERGE (1)") {
		t.Fatalf("stdout = %q, want the section and its count", stdout)
	}
}

func TestNothingOwedIsAZeroExitAndNotSilence(t *testing.T) {
	stdout, stderr, code := runMergeOwed(t, mergeOwedAnswer())

	if code != 0 {
		t.Fatalf("exit %d, want 0 when nothing is owed: %s%s", code, stdout, stderr)
	}
	// Every section prints, including the empty ones. A section that vanishes
	// when it empties makes an operator scanning for "nothing needs attention"
	// read an absence, and an absence is what a broken query looks like too.
	for _, heading := range []string{"OWED A MERGE (0)", "OWED ATTENTION (0)", "NOTHING OWED YET (0)"} {
		if !strings.Contains(stdout, heading) {
			t.Fatalf("stdout = %q, want the empty section %q", stdout, heading)
		}
	}
}

func TestWorkOwedOnlyAttentionIsNotAMergeOwedExit(t *testing.T) {
	stdout, stderr, code := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "attention-owed", "the branch has moved since it was approved", 3600)))

	// Attention is a person's job, not a merge. Exiting 1 here would have the
	// cron entry report a merge is owed on work that must not be merged.
	if code != 0 {
		t.Fatalf("exit %d, want 0 when only attention is owed: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "OWED ATTENTION (1)") {
		t.Fatalf("stdout = %q, want it under attention", stdout)
	}
}

func TestADispositionThisBuildDoesNotKnowIsPrintedRatherThanDropped(t *testing.T) {
	stdout, _, code := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "held-for-release", "a newer control plane said so", 60)))

	// A control plane ahead of this binary is the normal case during a rollout.
	// Dropping the row would report "nothing to do" about work that has
	// something to do, which is the failure the whole command exists to catch.
	if !strings.Contains(stdout, "HELD-FOR-RELEASE (1)") || !strings.Contains(stdout, "#41") {
		t.Fatalf("stdout = %q, want the unknown standing printed under its own heading", stdout)
	}
	if code != 0 {
		t.Fatalf("exit %d, want 0: this build cannot say a merge is owed on a standing it cannot read", code)
	}
}

func TestAChangeWithNothingToMeasureFromShowsADashAndNotZero(t *testing.T) {
	stdout, _, _ := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "waiting", "nobody has reviewed it", 0)))

	// "0m" reads as "it just happened", which is the opposite of "nobody has
	// looked at this yet".
	if strings.Contains(stdout, "0m") {
		t.Fatalf("stdout = %q, want a dash rather than a zero age", stdout)
	}
	if !strings.Contains(stdout, "-") {
		t.Fatalf("stdout = %q, want a dash for an unmeasurable age", stdout)
	}
}

func TestHowLongWorkHasStoodIsPrintedInUnitsAPersonReads(t *testing.T) {
	for _, unit := range []struct {
		seconds int
		want    string
	}{
		{seconds: 1800, want: "30m"},
		{seconds: 7200, want: "2h"},
		{seconds: 3 * 24 * 3600, want: "3d"},
	} {
		stdout, _, _ := runMergeOwed(t,
			mergeOwedAnswer(owedChange(41, "attention-owed", "why", unit.seconds)))
		if !strings.Contains(stdout, unit.want) {
			t.Fatalf("%d seconds printed as %q, want %q", unit.seconds, stdout, unit.want)
		}
	}
}

func TestTheJSONAnswerIsThePlainOneAndStillSaysItInTheExitCode(t *testing.T) {
	stdout, _, code := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "merge-owed", "approved at its current commit", 7200)), "--json")

	if code != 1 {
		t.Fatalf("exit %d, want 1: --json changes how it reads, not what it found", code)
	}
	if !strings.Contains(stdout, `"disposition": "merge-owed"`) {
		t.Fatalf("stdout = %q, want the standing in the JSON", stdout)
	}
	if strings.Contains(stdout, "OWED A MERGE") {
		t.Fatalf("stdout = %q, want JSON only", stdout)
	}
}

func TestQuietPrintsNothingAndStillSaysItInTheExitCode(t *testing.T) {
	stdout, _, code := runMergeOwed(t,
		mergeOwedAnswer(owedChange(41, "merge-owed", "approved at its current commit", 7200)), "--quiet")

	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}
	if code != 1 {
		t.Fatalf("exit %d, want 1: --quiet is how a cron entry asks", code)
	}
}

func TestAControlPlaneThatCannotAnswerIsAFailureAndNotAnEmptyReport(t *testing.T) {
	workerPath := mergeOwedControlPlane(t,
		`{"error":"github CLI failed (rate_limit): budget spent"}`, http.StatusTooManyRequests)
	stdout, stderr, code := runMergeOwedAgainst(t, workerPath)

	// "Nothing is owed" and "I could not find out" are the two answers this
	// command exists to keep apart. A rate-limited read printed as the first
	// one is the shell's worst failure ported forward.
	if code == 0 || code == 1 {
		t.Fatalf("exit %d, want neither of the two answers about what is owed: %s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "OWED A MERGE") {
		t.Fatalf("stdout = %q, want no report at all", stdout)
	}
}

func TestAnAnswerThatDoesNotSayWhenItWasReadIsRefused(t *testing.T) {
	stdout, stderr, code := runMergeOwed(t, `{"changes":[]}`)

	// A 200 carrying nothing prints as three empty sections, which is what a
	// repository with no outstanding work looks like. The two must not be the
	// same output.
	if code == 0 || code == 1 {
		t.Fatalf("exit %d, want a failure rather than a report: %s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "OWED A MERGE") {
		t.Fatalf("stdout = %q, want no report at all", stdout)
	}
}

func TestARepositoryMustBeNamed(t *testing.T) {
	workerPath := mergeOwedControlPlane(t, mergeOwedAnswer())
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"merge-owed", "--config", workerPath},
		strings.NewReader(""), &stdout, &stderr, "test")

	// Defaulting to every repository the store knows would read the forge once
	// per repository on a command a cron entry runs every few minutes.
	if code == 0 {
		t.Fatalf("exit 0 with no repository named: %s%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "repository") {
		t.Fatalf("stderr = %q, want it to name the missing flag", stderr.String())
	}
}
