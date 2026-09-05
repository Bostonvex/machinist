package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
)

// Reading the forge for merge-owed detection is one request for every open pull
// request at once, not one per pull request.
//
// That is the whole of what pr-drain.sh contributed. Its per-pull-request REST
// walk exhausted the hourly budget on a repository with forty open changes, and
// the fix was to ask GraphQL for the fields in a batch. `gh pr list --json` is
// that batch, so the port gets it by asking properly rather than by carrying
// the script's own pagination.
//
// The script's other rule is carried verbatim: a rate-limited read refuses and
// names the limit. It never retries into an exhausted bucket, and it never
// consults `gh api rate_limit` to decide -- that endpoint's display lagged a
// real 403 by 23 seconds when it was measured on 2026-08-27, so a probe against
// it says "there is budget" at the moment there is not.

const (
	// maxMergeOwedChanges bounds one read. A repository with more open changes
	// than this is not silently truncated: the caller is told, because a merge
	// that is owed and outside the window is exactly the work this looks for.
	maxMergeOwedChanges = 200
)

type githubRollupEntry struct {
	Typename    string `json:"__typename"`
	Name        string `json:"name"`
	Context     string `json:"context"`
	Conclusion  string `json:"conclusion"`
	State       string `json:"state"`
	Status      string `json:"status"`
	CompletedAt string `json:"completedAt"`
	StartedAt   string `json:"startedAt"`
	CreatedAt   string `json:"createdAt"`
}

type githubOpenChange struct {
	Number            int                 `json:"number"`
	Title             string              `json:"title"`
	URL               string              `json:"url"`
	IsDraft           bool                `json:"isDraft"`
	Mergeable         string              `json:"mergeable"`
	MergeStateStatus  string              `json:"mergeStateStatus"`
	HeadRefOid        string              `json:"headRefOid"`
	BaseRefName       string              `json:"baseRefName"`
	StatusCheckRollup []githubRollupEntry `json:"statusCheckRollup"`
}

// OpenChanges reads every open pull request in a repository, with the fields
// merge-owed detection decides on.
//
// The branch rules are read once per base branch and attached to the changes on
// that branch, rather than once per change. A repository whose changes all
// target one branch therefore costs two requests however many changes it has.
func (g *GitHubCLI) OpenChanges(ctx context.Context, repository string) ([]gatekeeper.Change, error) {
	repository, err := normalizeGitHubRepository(repository)
	if err != nil {
		return nil, err
	}
	stdout, err := g.run(ctx, "list open pull requests", []string{
		"pr", "list", "--repo", repository, "--state", "open",
		"--limit", fmt.Sprint(maxMergeOwedChanges + 1),
		"--json", "number,title,url,isDraft,mergeable,mergeStateStatus,headRefOid,baseRefName,statusCheckRollup",
	})
	if err != nil {
		return nil, err
	}
	var raw []githubOpenChange
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, malformedGitHubOutput("list open pull requests", err, stdout)
	}
	if len(raw) > maxMergeOwedChanges {
		// Truncating and reporting the rest as though they were everything
		// would answer "nothing is owed" about changes nobody looked at.
		return nil, fmt.Errorf("%s has more than %d open pull requests: read them in smaller sets rather than trusting a truncated answer",
			repository, maxMergeOwedChanges)
	}

	rules := map[string]gatekeeper.Change{}
	changes := make([]gatekeeper.Change, 0, len(raw))
	for _, entry := range raw {
		if entry.Number <= 0 {
			return nil, malformedGitHubOutput("list open pull requests",
				fmt.Errorf("pull request number %d is not one", entry.Number), stdout)
		}
		base := strings.TrimSpace(entry.BaseRefName)
		branch, read := rules[base]
		if !read {
			required, queue, err := g.BranchRules(ctx, repository, base)
			if err != nil {
				return nil, err
			}
			branch = gatekeeper.Change{RequiredChecks: required, QueueRequired: queue}
			rules[base] = branch
		}
		checks, err := parseGitHubRollup(entry.StatusCheckRollup)
		if err != nil {
			return nil, malformedGitHubOutput("list open pull requests", err, stdout)
		}
		changes = append(changes, gatekeeper.Change{
			Repository:     repository,
			Number:         entry.Number,
			Title:          entry.Title,
			URL:            entry.URL,
			Head:           strings.TrimSpace(entry.HeadRefOid),
			Draft:          entry.IsDraft,
			Mergeable:      gatekeeper.Mergeability(strings.TrimSpace(entry.Mergeable)),
			MergeState:     gatekeeper.MergeState(strings.TrimSpace(entry.MergeStateStatus)),
			Checks:         checks,
			RequiredChecks: branch.RequiredChecks,
			QueueRequired:  branch.QueueRequired,
		})
	}
	return changes, nil
}

// parseGitHubRollup turns the status rollup into checks.
//
// The rollup mixes two shapes: a CheckRun names itself in `name` and concludes
// in `conclusion`, while a legacy StatusContext names itself in `context` and
// concludes in `state`. Reading only the first shape would drop every commit
// status a repository still uses, and a required check that is dropped reads as
// a check that has not run, which is the safe direction but the wrong answer.
func parseGitHubRollup(entries []githubRollupEntry) ([]gatekeeper.Check, error) {
	checks := make([]gatekeeper.Check, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(entry.Context)
		}
		if name == "" {
			return nil, errors.New("a status check in the rollup has no name")
		}
		conclusion := strings.TrimSpace(entry.Conclusion)
		if conclusion == "" {
			conclusion = strings.TrimSpace(entry.State)
		}
		// A check run that has not completed reports an empty conclusion and a
		// status of QUEUED or IN_PROGRESS. That is a check with no conclusion,
		// which is what an empty Conclusion means to the gatekeeper, so it is
		// left empty rather than filled in with the status.
		reportedAt, err := firstGitHubTime(entry.CompletedAt, entry.StartedAt, entry.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("read when status check %q reported: %w", name, err)
		}
		checks = append(checks, gatekeeper.Check{Name: name, Conclusion: conclusion, ReportedAt: reportedAt})
	}
	return checks, nil
}

// firstGitHubTime takes the first timestamp that is present, and refuses one
// that is present and unreadable.
//
// Ordering two runs of the same check is what these are for, and a timestamp
// silently defaulted to zero puts a rerun behind the failure it replaced.
func firstGitHubTime(candidates ...string) (time.Time, error) {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		when, err := time.Parse(time.RFC3339, candidate)
		if err != nil {
			return time.Time{}, err
		}
		return when, nil
	}
	return time.Time{}, nil
}

type githubBranchRule struct {
	Type       string `json:"type"`
	Parameters struct {
		RequiredStatusChecks []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

// BranchRules reads which checks a branch requires and whether merges into it
// go through a queue.
//
// It asks for the rules that apply to the branch rather than listing the
// repository's rulesets, which is what makes the accumulation right: several
// rulesets can each require checks, and the shell's first version took the
// first matching ruleset and missed the rest. This endpoint returns the union
// GitHub itself will enforce, so there is nothing left to accumulate wrongly.
func (g *GitHubCLI) BranchRules(ctx context.Context, repository, branch string) ([]string, bool, error) {
	repository, err := normalizeGitHubRepository(repository)
	if err != nil {
		return nil, false, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, false, errors.New("github branch rules need a branch")
	}
	stdout, err := g.run(ctx, "read branch rules", []string{
		"api", "--method", "GET", "-H", "Accept: application/vnd.github+json",
		fmt.Sprintf("repos/%s/rules/branches/%s", repository, branch),
	})
	if err != nil {
		return nil, false, err
	}
	var rules []githubBranchRule
	if err := json.Unmarshal(stdout, &rules); err != nil {
		return nil, false, malformedGitHubOutput("read branch rules", err, stdout)
	}
	var required []string
	seen := map[string]struct{}{}
	queue := false
	for _, rule := range rules {
		switch strings.TrimSpace(rule.Type) {
		case "merge_queue":
			queue = true
		case "required_status_checks":
			for _, check := range rule.Parameters.RequiredStatusChecks {
				context := strings.TrimSpace(check.Context)
				if context == "" {
					// An unnameable required check is passed through rather
					// than dropped. The gatekeeper refuses on it, which is the
					// point: a rule this build cannot read is not a rule it may
					// decide has been satisfied.
					required = append(required, "")
					continue
				}
				if _, already := seen[context]; already {
					continue
				}
				seen[context] = struct{}{}
				required = append(required, context)
			}
		}
	}
	return required, queue, nil
}
