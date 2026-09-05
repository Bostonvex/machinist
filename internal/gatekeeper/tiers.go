package gatekeeper

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/owainlewis/machinist/internal/review"
)

// ProtectedFloor are the paths no tier may ever touch, in any repository,
// whatever the repository's own list says. They are the workflows that gate
// merges, the governance contracts the roles execute from, and the scripts and
// infrastructure a merge could otherwise reach.
//
// The floor is a floor: a repository list adds to it and can never subtract.
var ProtectedFloor = []string{
	".github/**",
	"docs/governance/**",
	"AGENTS.md",
	"scripts/**",
	"infra/**",
}

// greenDocumentPaths are the only paths the Green tier admits: words, and
// nothing that is executed.
func isGreenPath(file ChangedFile) (string, bool) {
	clean := strings.TrimPrefix(strings.TrimSpace(file.Path), "./")
	if clean == "" {
		return "path is empty", false
	}
	if file.Mode != ModeFile {
		return fmt.Sprintf("%s is mode %s, not a plain file", clean, file.Mode), false
	}
	if strings.ToLower(path.Ext(clean)) != ".md" {
		return fmt.Sprintf("%s is not a Markdown document", clean), false
	}
	if clean == "README.md" || strings.HasPrefix(clean, "docs/") {
		return "", true
	}
	// A Markdown file outside docs/ is still a document, but Green is defined by
	// what a change cannot reach, and a .md anywhere else has not been shown to
	// be unreachable from anything that runs.
	return fmt.Sprintf("%s is Markdown but sits outside docs/ and is not README.md", clean), false
}

// evaluateGreen applies the five Green conditions.
func evaluateGreen(request Request, enabled Enablement) (Decision, error) {
	if !enabled.Green {
		return refuse("the green tier is not enabled for %s", request.Repository)
	}
	reasons := []string{}

	for _, file := range request.Files {
		if why, ok := isGreenPath(file); !ok {
			return refuse("not green: %s", why)
		}
	}
	reasons = append(reasons, fmt.Sprintf("paths: %d Markdown documents, all mode %s", len(request.Files), ModeFile))

	// Conditions 2 and 3 are the floor. docs/governance/** and .github/** are
	// Markdown-shaped often enough that the extension check above does not catch
	// them, and they are exactly what must not merge unattended.
	if hits := protectedHits(request, ProtectedFloor); len(hits) > 0 {
		return refuse("not green: %s is a governance or workflow source", hits[0])
	}
	reasons = append(reasons, "no governance, workflow, or agent-instruction source")

	if err := checkReview(request, reasons); err != nil {
		return refuse("not green: %s", err)
	}
	reasons = append(reasons, reviewReason(request))

	if !request.ClosingIssuesMatchIntent {
		return refuse("not green: closing issues have not been confirmed to match intent")
	}
	reasons = append(reasons, closingIssuesReason(request))

	return Decision{Allowed: true, Tier: TierGreen, Reasons: reasons}, nil
}

// evaluateReviewed applies the six Reviewed conditions. Condition 6 is a gate,
// not a note, and is checked with the rest.
func evaluateReviewed(request Request, enabled Enablement) (Decision, error) {
	if !enabled.reviewedAllows(request.Repository) {
		return refuse("the reviewed tier is not enabled for %s", request.Repository)
	}
	reasons := []string{}

	if err := checkReview(request, reasons); err != nil {
		return refuse("not reviewed: %s", err)
	}
	if len(request.Review.Findings) > 0 {
		return refuse("not reviewed: the review returned %d findings", len(request.Review.Findings))
	}
	reasons = append(reasons, reviewReason(request))

	// Condition 2. Absence is not green: a repository that requires no check
	// cannot satisfy a tier whose whole premise is that checks caught what the
	// reviewer did not.
	if len(request.Checks.Required) == 0 {
		return refuse("not reviewed: %s requires no status check, so this tier is unavailable", request.Repository)
	}
	if !request.Checks.Strict {
		return refuse("not reviewed: required checks are not strict on the default branch, so a green check may not describe the commit that lands")
	}
	if missing := missingChecks(request.Checks); len(missing) > 0 {
		return refuse("not reviewed: required check %q is not passing on %s", missing[0], short(request.HeadSHA))
	}
	if status := strings.ToUpper(strings.TrimSpace(request.Checks.MergeStateStatus)); status != "CLEAN" {
		return refuse("not reviewed: merge state is %q, not CLEAN", request.Checks.MergeStateStatus)
	}
	reasons = append(reasons, fmt.Sprintf("checks: %d required, all passing on %s, strict, merge state CLEAN",
		len(request.Checks.Required), short(request.HeadSHA)))

	// Condition 3. Risk is read from every closing issue, never from the pull
	// request, and never inferred when a label is absent.
	if len(request.ClosingIssues) == 0 {
		return refuse("not reviewed: no closing issue, so risk cannot be read")
	}
	for _, issue := range request.ClosingIssues {
		if !issue.Risk.Valid() {
			return refuse("not reviewed: issue #%d carries no risk label", issue.Number)
		}
		if issue.Risk == RiskHigh {
			return refuse("not reviewed: issue #%d is %s", issue.Number, RiskHigh)
		}
	}
	reasons = append(reasons, fmt.Sprintf("risk: %s", risksReason(request.ClosingIssues)))

	// Condition 4. The floor, the repository's list, and the review engine's own
	// defaults, together.
	if request.ProtectedPathsUnread {
		return refuse("not reviewed: the repository's protected-path list could not be read")
	}
	patterns := mergePatterns(ProtectedFloor, request.ProtectedPaths, review.DefaultProtectedPaths)
	if hits := protectedHits(request, patterns); len(hits) > 0 {
		return refuse("not reviewed: %s is a protected path", hits[0])
	}
	reasons = append(reasons, fmt.Sprintf("protected paths: none of %d changed paths matched %d patterns",
		len(request.Files), len(patterns)))

	// Condition 5.
	for _, issue := range request.ClosingIssues {
		if issue.HumanRequired {
			return refuse("not reviewed: issue #%d is marked human-required", issue.Number)
		}
	}
	if !request.ClosingIssuesMatchIntent {
		return refuse("not reviewed: closing issues have not been confirmed to match intent")
	}
	reasons = append(reasons, closingIssuesReason(request))

	// Condition 6. Agent configuration is inside the floor already; this states
	// it as its own condition because it is one, and because the floor may be
	// widened without anyone remembering why this case mattered.
	if hits := protectedHits(request, agentConfigurationPatterns); len(hits) > 0 {
		return refuse("not reviewed: %s configures or instructs an agent", hits[0])
	}
	reasons = append(reasons, "no agent-configuration change")

	return Decision{Allowed: true, Tier: TierReviewed, Reasons: reasons}, nil
}

// agentConfigurationPatterns are the files that configure or instruct an agent.
// Changing one changes what every future run does, which is not a code change
// that CI can speak to.
var agentConfigurationPatterns = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"docs/governance/**",
	"**/prompts/**",
	".claude/**",
	".github/agents/**",
}

// checkReview applies the review condition that Green and Reviewed share: an
// independent reviewer returned a ready verdict naming this exact head.
func checkReview(request Request, _ []string) error {
	if !request.Review.Verdict.Valid() {
		return fmt.Errorf("the review verdict %q is not one this contract defines", request.Review.Verdict)
	}
	if request.Review.Verdict != review.VerdictReady {
		return fmt.Errorf("the review verdict is %q", request.Review.Verdict)
	}
	if err := review.CheckIndependence(request.Review.Author, request.Review.Reviewer); err != nil {
		return fmt.Errorf("the review was not independent: %w", err)
	}
	reviewed := strings.TrimSpace(request.Review.HeadSHA)
	if reviewed == "" {
		return fmt.Errorf("the review names no head commit")
	}
	if !strings.EqualFold(reviewed, strings.TrimSpace(request.HeadSHA)) {
		return fmt.Errorf("the review names head %s but %s is merging", short(reviewed), short(request.HeadSHA))
	}
	return nil
}

func reviewReason(request Request) string {
	return fmt.Sprintf("review: %s from %s on %s, independent of %s",
		request.Review.Verdict, request.Review.Reviewer.Agent,
		short(request.Review.HeadSHA), request.Review.Author.Agent)
}

func closingIssuesReason(request Request) string {
	if len(request.ClosingIssues) == 0 {
		return "closing issues: none, confirmed deliberate"
	}
	numbers := make([]string, 0, len(request.ClosingIssues))
	for _, issue := range request.ClosingIssues {
		numbers = append(numbers, fmt.Sprintf("#%d", issue.Number))
	}
	return fmt.Sprintf("closing issues: %s, confirmed to match intent", strings.Join(numbers, ", "))
}

func risksReason(issues []ClosingIssue) string {
	seen := map[Risk]struct{}{}
	labels := []string{}
	for _, issue := range issues {
		if _, ok := seen[issue.Risk]; ok {
			continue
		}
		seen[issue.Risk] = struct{}{}
		labels = append(labels, string(issue.Risk))
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// missingChecks returns the required checks that are not passing on the head.
func missingChecks(checks ChecksState) []string {
	passing := make(map[string]struct{}, len(checks.Passing))
	for _, name := range checks.Passing {
		passing[normalize(name)] = struct{}{}
	}
	var missing []string
	for _, name := range checks.Required {
		if _, ok := passing[normalize(name)]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// protectedHits returns the changed paths matching any pattern, sorted.
func protectedHits(request Request, patterns []string) []string {
	var hits []string
	for _, candidate := range request.paths() {
		if candidate == "" {
			continue
		}
		for _, pattern := range patterns {
			if matchPath(pattern, candidate) {
				hits = append(hits, candidate)
				break
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func mergePatterns(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var merged []string
	for _, list := range lists {
		for _, pattern := range list {
			clean := strings.TrimSpace(pattern)
			if clean == "" {
				continue
			}
			if _, ok := seen[clean]; ok {
				continue
			}
			seen[clean] = struct{}{}
			merged = append(merged, clean)
		}
	}
	sort.Strings(merged)
	return merged
}

// matchPath reports whether a slash-separated path matches a glob pattern. A
// "**" segment matches any number of remaining segments; every other segment is
// matched by path.Match, so "*" stops at a separator.
func matchPath(pattern, candidate string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(candidate, "/"))
}

func matchSegments(pattern, candidate []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return true
			}
			for index := range candidate {
				if matchSegments(pattern[1:], candidate[index:]) {
					return true
				}
			}
			return false
		}
		if len(candidate) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], candidate[0]); err != nil || !ok {
			return false
		}
		pattern, candidate = pattern[1:], candidate[1:]
	}
	return len(candidate) == 0
}

func short(sha string) string {
	clean := strings.TrimSpace(sha)
	if len(clean) > 12 {
		return clean[:12]
	}
	return clean
}
