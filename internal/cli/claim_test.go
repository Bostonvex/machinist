package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/controlplane"
)

func claimControlPlane(t *testing.T) (string, *controlplane.Store) {
	t.Helper()
	workerPath, store, _ := boardControlPlane(t)
	return workerPath, store
}

func runClaim(t *testing.T, workerPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), append([]string{"claim", "--config", workerPath}, args...),
		strings.NewReader(""), &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), code
}

func TestTakingAnIssueIsVisibleInTheListing(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	stdout, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting the claim concept", "--for", "4h")
	if code != 0 {
		t.Fatalf("take exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "claimed by seat-a") {
		t.Fatalf("take said %q", stdout)
	}
	listed, stderr, code := runClaim(t, workerPath, "list")
	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(listed, "Bostonvex/machinist#7") || !strings.Contains(listed, "seat-a") {
		t.Fatalf("listing = %q", listed)
	}
	// The column an operator reads is whether the claim still stops anyone.
	for _, line := range strings.Split(listed, "\n") {
		if strings.HasPrefix(line, "Bostonvex/machinist#7") && !strings.Contains(line, "yes") {
			t.Fatalf("a live claim listed as not live: %q", line)
		}
	}
}

func TestASecondSeatIsToldWhoHasTheIssue(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	if _, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting the claim concept", "--for", "4h"); code != 0 {
		t.Fatalf("first take exit = %d, stderr = %q", code, stderr)
	}
	stdout, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-b",
		"--reason", "I also want it", "--for", "4h")
	if code == 0 {
		t.Fatalf("a second seat took a claimed issue: %q", stdout)
	}
	if !strings.Contains(stderr, "seat-a") {
		t.Fatalf("stderr = %q, want it to name who actually holds the issue", stderr)
	}
}

func TestReleasingAnIssueMakesItFreeAgain(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	if _, _, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting", "--for", "4h"); code != 0 {
		t.Fatal("take failed")
	}
	if _, stderr, code := runClaim(t, workerPath, "release",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a", "--reason", "handed back"); code != 0 {
		t.Fatalf("release exit = %d, stderr = %q", code, stderr)
	}
	if _, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-b",
		"--reason", "picking it up", "--for", "4h"); code != 0 {
		t.Fatalf("the issue was not free after release: exit %d, stderr %q", code, stderr)
	}
}

func TestAHeldIssueIsReportedAsNotFreeWork(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	if _, _, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting", "--for", "4h"); code != 0 {
		t.Fatal("take failed")
	}
	stdout, stderr, code := runClaim(t, workerPath, "hold",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "redirected to the incident", "--for", "2h",
		"--transfer", "Bostonvex/machinist#9")
	if code != 0 {
		t.Fatalf("hold exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "not free work") {
		t.Fatalf("hold said %q, want it to say the issue is not free work", stdout)
	}
	if _, _, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-b",
		"--reason", "looks free to me", "--for", "4h"); code == 0 {
		t.Fatal("a held issue was taken by another seat")
	}
}

func TestReleasingAnIssueNobodyHoldsFails(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	stdout, stderr, code := runClaim(t, workerPath, "release",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a", "--reason", "done")
	if code == 0 {
		t.Fatalf("releasing nothing succeeded with %q; that is how a stale read gets confirmed", stdout)
	}
	if !strings.Contains(stderr, "no live claim") {
		t.Fatalf("stderr = %q, want it to say there is nothing to release", stderr)
	}
}

func TestAnEmptyClaimListingIsNotAnEmptyTable(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	stdout, _, code := runClaim(t, workerPath, "list")
	if code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if !strings.Contains(stdout, "no issue claims") {
		t.Fatalf("listing = %q, want it to say so in words", stdout)
	}
	if strings.Contains(stdout, "ISSUE") {
		t.Fatalf("listing = %q, want no header row over nothing", stdout)
	}
}

func TestAnIssueReferenceThatIsNotOneIsRefused(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	// Each of these is a different mistake, and the message is asserted so they
	// stay different. Several of the checks in parseIssue would otherwise be
	// covered incidentally by a later one -- an empty issue number parses as a
	// bad integer, for instance -- and a check that only ever fires through
	// another check can be deleted without any test noticing.
	for name, expectation := range map[string]struct {
		reference string
		says      string
	}{
		"no issue number": {"Bostonvex/machinist", "must be owner/repo#n"},
		"no repository":   {"7", "must be owner/repo#n"},
		"no owner":        {"machinist#7", "must name a repository as owner/repo"},
		"not a number":    {"Bostonvex/machinist#seven", "must end in an issue number"},
		"issue zero":      {"Bostonvex/machinist#0", "must name a positive issue number"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runClaim(t, workerPath, "take",
				"--issue", expectation.reference, "--holder", "seat-a", "--reason", "why", "--for", "4h")
			if code == 0 {
				t.Fatalf("%q was accepted as an issue: %q", expectation.reference, stdout)
			}
			if !strings.Contains(stderr, expectation.says) {
				t.Fatalf("stderr = %q, want it to say %q", stderr, expectation.says)
			}
		})
	}
}

func TestEveryClaimDecisionNeedsAnIssueAHolderAndAReason(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	full := []string{
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting", "--for", "4h",
	}
	for _, flag := range []string{"issue", "holder", "reason", "for"} {
		t.Run(flag, func(t *testing.T) {
			args := []string{"take"}
			for index := 0; index < len(full); index += 2 {
				if strings.TrimPrefix(full[index], "--") == flag {
					continue
				}
				args = append(args, full[index], full[index+1])
			}
			_, stderr, code := runClaim(t, workerPath, args...)
			if code == 0 {
				t.Fatalf("take succeeded without --%s", flag)
			}
			// The control plane also refuses most of these, so the test insists
			// the refusal came from the flag itself: otherwise it cannot tell
			// whether the command is checking anything at all.
			if !strings.Contains(stderr, "required flag") || !strings.Contains(stderr, flag) {
				t.Fatalf("stderr = %q, want cobra to refuse the missing --%s", stderr, flag)
			}
		})
	}
}

func TestAClaimWindowThatHasAlreadyRunOutIsRefused(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	_, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting", "--for", "-1h")
	if code == 0 {
		t.Fatal("a negative window was accepted")
	}
	if !strings.Contains(stderr, "--for must be positive") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestTheCLIReportsTheStoredClaimRatherThanWhatItSent(t *testing.T) {
	workerPath, _ := claimControlPlane(t)
	// The store trims. If the command echoed its own request the padding would
	// come back, and an operator checking they claimed the issue they meant to
	// would be reading their own typing.
	stdout, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "  Bostonvex/machinist#7  ", "--holder", "  seat-a  ",
		"--reason", "  porting  ", "--for", "4h")
	if code != 0 {
		t.Fatalf("take exit = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "  seat-a  ") || strings.Contains(stdout, "  porting  ") {
		t.Fatalf("take echoed its own request: %q", stdout)
	}
	if !strings.Contains(stdout, "claimed by seat-a") {
		t.Fatalf("take said %q", stdout)
	}
}

func TestAClaimTakenByTheCLIIsTheOneTheStoreHolds(t *testing.T) {
	workerPath, store := claimControlPlane(t)
	if _, stderr, code := runClaim(t, workerPath, "take",
		"--issue", "Bostonvex/machinist#7", "--holder", "seat-a",
		"--reason", "porting", "--for", "4h"); code != 0 {
		t.Fatalf("take exit = %d, stderr = %q", code, stderr)
	}
	claim, found, err := store.Claim(t.Context(), "Bostonvex/machinist", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the CLI reported a claim the store does not hold")
	}
	if claim.Holder != "seat-a" || !claim.Live(time.Now()) {
		t.Fatalf("stored claim = %#v", claim)
	}
}
