package review

import (
	"fmt"
	"regexp"
	"strings"
)

// Reviewer output is a small line-oriented block so that any harness can emit
// it without a serializer:
//
//	VERDICT: ready-for-human-review
//	FINDINGS:
//	- [high] internal/runner/runner.go: leaks the process group — terminate the tree
//	PROTECTED_PATHS: none
//	HIGH_RISK: no
//	NOTE: ...
//
// Parsing fails closed. A block with no verdict, an unknown key, or an
// unparsable finding is an error, not a lenient partial report: a malformed
// review must not read as an approval.

const (
	keyVerdict        = "VERDICT"
	keyFindings       = "FINDINGS"
	keyProtectedPaths = "PROTECTED_PATHS"
	keyHighRisk       = "HIGH_RISK"
	keyNote           = "NOTE"
)

var knownKeys = map[string]struct{}{
	keyVerdict: {}, keyFindings: {}, keyProtectedPaths: {}, keyHighRisk: {}, keyNote: {},
}

var (
	headerPattern  = regexp.MustCompile(`^([A-Z][A-Z_]*):\s*(.*)$`)
	findingPattern = regexp.MustCompile(`^-\s*\[([^\]]+)\]\s*(.+)$`)
)

// findingSeparators split a finding's issue from its recommendation. The em
// dash is the documented form; the ASCII fallback keeps hand-typed reviews and
// harnesses that transliterate punctuation from being rejected.
var findingSeparators = []string{"—", " -- ", " - "}

// Parse reads one reviewer output block.
func Parse(output string) (Report, error) {
	var report Report
	seen := make(map[string]struct{}, len(knownKeys))
	inFindings := false
	var findingLines []string

	for index, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		match := headerPattern.FindStringSubmatch(line)
		if match == nil {
			// Inside the findings block only bulleted lines are findings; any
			// other line is the reviewer's prose and closes the block.
			if inFindings && strings.HasPrefix(line, "-") {
				findingLines = append(findingLines, line)
				continue
			}
			inFindings = false
			continue
		}
		key, value := match[1], strings.TrimSpace(match[2])
		if _, ok := knownKeys[key]; !ok {
			return Report{}, fmt.Errorf("line %d: unknown review key %q", index+1, key)
		}
		if _, ok := seen[key]; ok {
			return Report{}, fmt.Errorf("line %d: duplicate review key %q", index+1, key)
		}
		seen[key] = struct{}{}
		inFindings = key == keyFindings

		switch key {
		case keyVerdict:
			verdict := Verdict(normalize(value))
			if !verdict.Valid() {
				return Report{}, fmt.Errorf("line %d: unknown verdict %q", index+1, value)
			}
			report.Verdict = verdict
		case keyFindings:
			if value != "" {
				findingLines = append(findingLines, "- "+value)
			}
		case keyProtectedPaths:
			report.ProtectedPaths = parseList(value)
		case keyHighRisk:
			highRisk, err := parseBool(value)
			if err != nil {
				return Report{}, fmt.Errorf("line %d: %w", index+1, err)
			}
			report.HighRisk = highRisk
		case keyNote:
			report.Note = value
		}
	}

	if _, ok := seen[keyVerdict]; !ok {
		return Report{}, fmt.Errorf("review output has no %s line", keyVerdict)
	}
	findings, err := parseFindings(findingLines)
	if err != nil {
		return Report{}, err
	}
	report.Findings = findings
	return report, nil
}

func parseFindings(lines []string) ([]Finding, error) {
	var findings []Finding
	for _, line := range lines {
		if isNone(strings.TrimPrefix(line, "-")) {
			continue
		}
		match := findingPattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("finding %q must read: - [severity] path: issue — recommendation", line)
		}
		severity := Severity(normalize(match[1]))
		if !severity.Valid() {
			return nil, fmt.Errorf("finding %q has unknown severity %q", line, match[1])
		}
		path, rest, found := strings.Cut(match[2], ":")
		if !found {
			return nil, fmt.Errorf("finding %q must name a path before the first colon", line)
		}
		finding := Finding{Severity: severity, Path: strings.TrimSpace(path)}
		if finding.Path == "" {
			return nil, fmt.Errorf("finding %q must name a path before the first colon", line)
		}
		issue, recommendation := splitRecommendation(rest)
		finding.Issue, finding.Recommendation = issue, recommendation
		if finding.Issue == "" {
			return nil, fmt.Errorf("finding %q must describe the issue", line)
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func splitRecommendation(rest string) (string, string) {
	for _, separator := range findingSeparators {
		if issue, recommendation, found := strings.Cut(rest, separator); found {
			return strings.TrimSpace(issue), strings.TrimSpace(recommendation)
		}
	}
	return strings.TrimSpace(rest), ""
}

func parseList(value string) []string {
	if isNone(value) {
		return nil
	}
	var items []string
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		if item := strings.TrimSpace(field); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseBool(value string) (bool, error) {
	switch normalize(value) {
	case "yes", "true":
		return true, nil
	case "no", "false":
		return false, nil
	}
	return false, fmt.Errorf("%s must be yes or no, got %q", keyHighRisk, value)
}

func isNone(value string) bool {
	trimmed := normalize(value)
	return trimmed == "" || trimmed == "none"
}
