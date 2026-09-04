package review

import (
	"path"
	"sort"
	"strings"
)

// DefaultProtectedPaths are the Machinist paths whose change always needs a
// human: the workflows that gate merges, the governance contracts the roles
// read, and the generated frontend bundle that is embedded into the binary.
var DefaultProtectedPaths = []string{
	".github/workflows/**",
	".github/dependabot.yml",
	"docs/governance/**",
	"internal/controlplane/web/dist/**",
	"LICENSE",
	"SECURITY.md",
}

// DefaultBlockingSeverities are the finding severities that withhold a ready
// verdict on their own.
var DefaultBlockingSeverities = []Severity{SeverityBlocker, SeverityHigh}

// protectedMatches returns the changed paths covered by any pattern, sorted and
// deduplicated.
func protectedMatches(patterns, changed []string) []string {
	seen := make(map[string]struct{}, len(changed))
	for _, candidate := range changed {
		clean := strings.TrimPrefix(strings.TrimSpace(candidate), "./")
		if clean == "" {
			continue
		}
		for _, pattern := range patterns {
			if matchPath(pattern, clean) {
				seen[clean] = struct{}{}
				break
			}
		}
	}
	matches := make([]string, 0, len(seen))
	for match := range seen {
		matches = append(matches, match)
	}
	sort.Strings(matches)
	return matches
}

// matchPath reports whether a slash-separated path matches a glob pattern.
// A "**" segment matches any number of remaining segments; every other segment
// is matched by path.Match, so "*" stops at a separator.
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
