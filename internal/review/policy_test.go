package review

import (
	"slices"
	"testing"
)

func TestMatchPathHandlesDoubleStar(t *testing.T) {
	cases := map[string]struct {
		pattern, candidate string
		want               bool
	}{
		"double star spans segments":   {".github/workflows/**", ".github/workflows/nested/ci.yml", true},
		"double star matches one":      {".github/workflows/**", ".github/workflows/ci.yml", true},
		"double star needs the prefix": {".github/workflows/**", ".github/dependabot.yml", false},
		"single star stops at slash":   {"docs/*.md", "docs/governance/roles/reviewer.md", false},
		"single star matches a name":   {"docs/*.md", "docs/README.md", true},
		"exact file":                   {"LICENSE", "LICENSE", true},
		"exact file is not a prefix":   {"LICENSE", "LICENSE.md", false},
		"pattern longer than path":     {"a/b/c", "a/b", false},
	}
	for name, tc := range cases {
		if got := matchPath(tc.pattern, tc.candidate); got != tc.want {
			t.Fatalf("%s: matchPath(%q, %q) = %v", name, tc.pattern, tc.candidate, got)
		}
	}
}

func TestProtectedMatchesDeduplicatesAndSorts(t *testing.T) {
	changed := []string{
		"./.github/workflows/ci.yml",
		".github/workflows/ci.yml",
		"internal/review/engine.go",
		"docs/governance/roles/reviewer.md",
		"",
	}
	got := protectedMatches(DefaultProtectedPaths, changed)
	want := []string{".github/workflows/ci.yml", "docs/governance/roles/reviewer.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("protectedMatches = %#v, want %#v", got, want)
	}
}
