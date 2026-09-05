package gatekeeper

import (
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/review"
)

func author() review.Party {
	return review.Party{Role: review.RoleImplementer, Agent: "dgx-deepcode", RunID: "run_author"}
}

func reviewer() review.Party {
	return review.Party{Role: review.RoleReviewer, Agent: "claude-subscription", RunID: "run_reviewer"}
}

const head = "0123456789abcdef0123456789abcdef01234567"

// docsRequest is a change that Green should accept, so each test can spoil
// exactly one condition and see that the spoiling is what refused it.
func docsRequest() Request {
	return Request{
		Repository:  "Bostonvex/machinist",
		PullRequest: 42,
		HeadSHA:     head,
		Files: []ChangedFile{
			{Path: "docs/migration-plan.md", Mode: ModeFile},
			{Path: "README.md", Mode: ModeFile},
		},
		ClosingIssuesMatchIntent: true,
		ClosingIssues:            []ClosingIssue{{Number: 5, Risk: RiskLow}},
		Review: Review{
			Verdict: review.VerdictReady, HeadSHA: head,
			Author: author(), Reviewer: reviewer(),
		},
	}
}

// codeRequest is a change that Reviewed should accept.
func codeRequest() Request {
	request := docsRequest()
	request.Files = []ChangedFile{{Path: "internal/runner/runner.go", Mode: ModeFile}}
	request.Checks = ChecksState{
		Required:         []string{"Linux checks", "macOS checks"},
		Passing:          []string{"Linux checks", "macOS checks"},
		Strict:           true,
		MergeStateStatus: "CLEAN",
	}
	return request
}

func greenEnabled() Enablement {
	return Enablement{Green: true}
}

func reviewedEnabled() Enablement {
	return Enablement{ReviewedRepositories: []string{"Bostonvex/machinist"}}
}

func mustRefuse(t *testing.T, decision Decision, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("merge was authorized on tier %q; expected a refusal mentioning %q", decision.Tier, contains)
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("error %v does not wrap ErrRefused", err)
	}
	if decision.Allowed {
		t.Fatal("a refusal returned an allowed decision")
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("refusal %q does not mention %q", err, contains)
	}
}

// The zero Enablement is the documented default: describing a tier does not
// turn it on, so nothing merges without the owner's word.
func TestNoTierIsEnabledByDefault(t *testing.T) {
	decision, err := Authorize(docsRequest(), Enablement{})
	mustRefuse(t, decision, err, "no tier is enabled")
}

// The zero Decision must never read as an authorization, because it is what a
// caller gets from any path that forgot to fill one in.
func TestTheZeroDecisionAuthorizesNothing(t *testing.T) {
	var decision Decision
	if decision.Allowed {
		t.Fatal("the zero Decision is allowed")
	}
	if decision.Tier != "" {
		t.Fatalf("the zero Decision names tier %q", decision.Tier)
	}
}

func TestGreenMergesDocumentation(t *testing.T) {
	decision, err := Authorize(docsRequest(), greenEnabled())
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.Tier != TierGreen {
		t.Fatalf("decision = %#v", decision)
	}
	// The reasons are the record: several conditions cannot be reconstructed
	// from the forge after the merge, so they must be written down here.
	if len(decision.Reasons) < 4 {
		t.Fatalf("green recorded only %d reasons: %v", len(decision.Reasons), decision.Reasons)
	}
}

// A mode is what separates words from behaviour, and the pull request files API
// does not expose it. A path that looks like a document but is executable or a
// symlink is not Green.
func TestGreenReadsModesNotNames(t *testing.T) {
	for _, mode := range []FileMode{ModeExecutable, ModeSymlink, ModeSubmodule} {
		request := docsRequest()
		request.Files = []ChangedFile{{Path: "docs/tool.md", Mode: mode}}
		decision, err := Authorize(request, greenEnabled())
		mustRefuse(t, decision, err, string(mode))
	}
}

// A mode that was never read is not a plain file. This is the case that matters:
// a caller that skipped the git tree must be refused, not defaulted.
func TestAnUnreadModeIsRefused(t *testing.T) {
	request := docsRequest()
	request.Files = []ChangedFile{{Path: "docs/plan.md"}}
	decision, err := Authorize(request, greenEnabled())
	mustRefuse(t, decision, err, "was not read from the git tree")
}

// Green is defined by what a change cannot reach. Governance documents are
// Markdown under docs/, and are exactly what must not merge unattended.
func TestGreenRefusesGovernanceMarkdown(t *testing.T) {
	for _, path := range []string{
		"docs/governance/roles/gatekeeper.md",
		"docs/governance/protocol/merge-tiers.md",
		"AGENTS.md",
	} {
		request := docsRequest()
		request.Files = []ChangedFile{{Path: path, Mode: ModeFile}}
		decision, err := Authorize(request, greenEnabled())
		if err == nil {
			t.Fatalf("%s merged under green as tier %q", path, decision.Tier)
		}
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v does not wrap ErrRefused", path, err)
		}
	}
}

func TestGreenRefusesNonDocuments(t *testing.T) {
	for _, path := range []string{
		"internal/runner/runner.go",
		"scripts/deploy.sh",
		".github/workflows/ci.yml",
		"notes.md",
	} {
		request := docsRequest()
		request.Files = []ChangedFile{{Path: path, Mode: ModeFile}}
		decision, err := Authorize(request, greenEnabled())
		if err == nil {
			t.Fatalf("%s merged under green as tier %q", path, decision.Tier)
		}
	}
}

// A review of a different commit is not a review of this merge.
func TestAReviewMustNameTheCommitThatMerges(t *testing.T) {
	request := docsRequest()
	request.Review.HeadSHA = "fedcba9876543210fedcba9876543210fedcba98"
	decision, err := Authorize(request, greenEnabled())
	mustRefuse(t, decision, err, "but")
}

// The author cannot bless their own change, whichever tier is asking.
func TestASelfReviewAuthorizesNothing(t *testing.T) {
	for name, enabled := range map[string]Enablement{"green": greenEnabled(), "reviewed": reviewedEnabled()} {
		request := docsRequest()
		if name == "reviewed" {
			request = codeRequest()
		}
		request.Review.Reviewer = review.Party{
			Role: review.RoleReviewer, Agent: request.Review.Author.Agent, RunID: "run_other",
		}
		decision, err := Authorize(request, enabled)
		mustRefuse(t, decision, err, "not independent")
	}
}

func TestReviewedMergesLowRiskCode(t *testing.T) {
	decision, err := Authorize(codeRequest(), reviewedEnabled())
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.Tier != TierReviewed {
		t.Fatalf("decision = %#v", decision)
	}
}

// The Reviewed tier permits nothing until a repository is named, and naming one
// repository does not name another.
func TestReviewedAppliesOnlyWhereTheOwnerNamedTheRepository(t *testing.T) {
	request := codeRequest()
	request.Repository = "Bostonvex/other"
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "no tier is enabled")

	empty := codeRequest()
	decision, err = Authorize(empty, Enablement{ReviewedRepositories: nil})
	mustRefuse(t, decision, err, "no tier is enabled")
}

// Absence is not green. A repository that requires no check cannot satisfy a
// tier whose premise is that checks caught what the reviewer did not.
func TestReviewedIsUnavailableWithoutRequiredChecks(t *testing.T) {
	request := codeRequest()
	request.Checks.Required = nil
	request.Checks.Passing = nil
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "requires no status check")
}

// A green check on a stale head may describe a commit that is not the one that
// lands.
func TestReviewedRequiresStrictChecks(t *testing.T) {
	request := codeRequest()
	request.Checks.Strict = false
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "not strict")
}

func TestReviewedRequiresEveryCheckPassingAndACleanMergeState(t *testing.T) {
	failing := codeRequest()
	failing.Checks.Passing = []string{"Linux checks"}
	decision, err := Authorize(failing, reviewedEnabled())
	mustRefuse(t, decision, err, "macOS checks")

	behind := codeRequest()
	behind.Checks.MergeStateStatus = "BEHIND"
	decision, err = Authorize(behind, reviewedEnabled())
	mustRefuse(t, decision, err, "BEHIND")
}

// Risk is read from every closing issue. An issue with no risk label has not
// been assessed, and unassessed is not low.
func TestAnUnlabelledRiskIsNotALowRisk(t *testing.T) {
	request := codeRequest()
	request.ClosingIssues = []ClosingIssue{{Number: 5, Risk: RiskLow}, {Number: 6}}
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "carries no risk label")
}

func TestReviewedRefusesHighRiskAndHumanRequired(t *testing.T) {
	high := codeRequest()
	high.ClosingIssues = []ClosingIssue{{Number: 5, Risk: RiskHigh}}
	decision, err := Authorize(high, reviewedEnabled())
	mustRefuse(t, decision, err, "risk-high")

	human := codeRequest()
	human.ClosingIssues = []ClosingIssue{{Number: 5, Risk: RiskLow, HumanRequired: true}}
	decision, err = Authorize(human, reviewedEnabled())
	mustRefuse(t, decision, err, "human-required")
}

// Risk cannot be read from a pull request that closes nothing.
func TestReviewedRefusesWhenThereIsNoClosingIssue(t *testing.T) {
	request := codeRequest()
	request.ClosingIssues = nil
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "no closing issue")
}

// A list that could not be read is not an empty list.
func TestAnUnreadProtectedPathListIsNotAnEmptyOne(t *testing.T) {
	request := codeRequest()
	request.ProtectedPathsUnread = true
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "could not be read")
}

// The floor holds in every repository, whatever the repository's own list says,
// and the repository's list adds to it.
func TestTheProtectedFloorHoldsAndTheRepositoryListAdds(t *testing.T) {
	for _, path := range []string{
		".github/workflows/ci.yml", "docs/governance/roles/reviewer.md",
		"AGENTS.md", "scripts/install.sh", "infra/main.tf",
	} {
		request := codeRequest()
		request.Files = []ChangedFile{{Path: path, Mode: ModeFile}}
		decision, err := Authorize(request, reviewedEnabled())
		if err == nil {
			t.Fatalf("%s merged under reviewed as tier %q", path, decision.Tier)
		}
	}

	added := codeRequest()
	added.Files = []ChangedFile{{Path: "internal/secrets/keys.go", Mode: ModeFile}}
	added.ProtectedPaths = []string{"internal/secrets/**"}
	decision, err := Authorize(added, reviewedEnabled())
	mustRefuse(t, decision, err, "protected path")
}

// Condition 6 is a gate. A prompt is not source code that CI can speak to: it
// changes what every future run does.
func TestReviewedRefusesAgentConfiguration(t *testing.T) {
	for _, path := range []string{"CLAUDE.md", "prompts/foreman.md", ".claude/settings.json"} {
		request := codeRequest()
		request.Files = []ChangedFile{{Path: path, Mode: ModeFile}}
		decision, err := Authorize(request, reviewedEnabled())
		if err == nil {
			t.Fatalf("%s merged under reviewed as tier %q", path, decision.Tier)
		}
	}
}

// A findings-free ready verdict is the condition; a ready verdict that still
// carries findings is not it.
func TestReviewedRefusesAReadyVerdictWithFindings(t *testing.T) {
	request := codeRequest()
	request.Review.Findings = []review.Finding{{Severity: review.SeverityLow, Path: "a.go", Issue: "nit"}}
	decision, err := Authorize(request, reviewedEnabled())
	mustRefuse(t, decision, err, "findings")
}

func TestOwnerNamedMergesWhatNoTierWould(t *testing.T) {
	request := codeRequest()
	request.Files = []ChangedFile{{Path: ".github/workflows/ci.yml", Mode: ModeFile}}
	request.OwnerNamed = true

	decision, err := Authorize(request, Enablement{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.Tier != TierOwnerNamed {
		t.Fatalf("decision = %#v", decision)
	}
}

// The owner names a pull request; they do not thereby certify that CI passed.
func TestOwnerNamedStillRequiresTheChecksTheRepositoryRequires(t *testing.T) {
	request := codeRequest()
	request.OwnerNamed = true
	request.Checks.Passing = nil
	decision, err := Authorize(request, Enablement{})
	mustRefuse(t, decision, err, "not passing")
}

// Escalate means no agent may decide this. An owner-named merge is still
// performed by an agent, so it does not overrule one.
func TestAnEscalateVerdictIsNotOverruledByTheOwnersName(t *testing.T) {
	request := codeRequest()
	request.OwnerNamed = true
	request.Review.Verdict = review.VerdictEscalate
	decision, err := Authorize(request, Enablement{})
	mustRefuse(t, decision, err, "escalate")
}

func TestClosingIssuesMustBeConfirmed(t *testing.T) {
	for name, setup := range map[string]func(Request) Request{
		"green":       func(r Request) Request { return r },
		"owner-named": func(r Request) Request { r.OwnerNamed = true; return r },
	} {
		request := setup(docsRequest())
		request.ClosingIssuesMatchIntent = false
		decision, err := Authorize(request, greenEnabled())
		if err == nil {
			t.Fatalf("%s: merged with unconfirmed closing issues as tier %q", name, decision.Tier)
		}
	}
}

// A tier evaluated against an incomplete request would be deciding on absence.
func TestAnIncompleteRequestIsRefusedBeforeAnyTierIsConsidered(t *testing.T) {
	for name, spoil := range map[string]func(Request) Request{
		"no repository": func(r Request) Request { r.Repository = ""; return r },
		"no number":     func(r Request) Request { r.PullRequest = 0; return r },
		"no head":       func(r Request) Request { r.HeadSHA = ""; return r },
		"no files":      func(r Request) Request { r.Files = nil; return r },
	} {
		request := spoil(docsRequest())
		request.OwnerNamed = true
		decision, err := Authorize(request, greenEnabled())
		if err == nil {
			t.Fatalf("%s: merged as tier %q", name, decision.Tier)
		}
		if !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("%s: refusal %q does not say the request was incomplete", name, err)
		}
	}
}
