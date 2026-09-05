package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped reviewer prompt is the only place a reviewer is ever told what to
// emit, and Parse is the only thing that reads what it emits. Nothing else
// connects them, so they drift silently: a prompt that describes the format
// loosely produces prose, Parse refuses it, and the review route records
// nothing — which reads exactly like a change nobody looked at.
//
// The three sets below are read out of this package rather than restated here.
// A key, verdict, or severity added to the parser without a word in the prompt
// fails here, and so does one removed from the parser while the prompt still
// promises it.
func TestShippedReviewerPromptTeachesTheFormatTheParserDemands(t *testing.T) {
	prompt, err := os.ReadFile(filepath.Join("..", "..", "examples", "prompts", "reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(prompt)
	for key := range knownKeys {
		if !strings.Contains(text, key+":") {
			t.Fatalf("reviewer prompt never names the %s: line the parser reads", key)
		}
	}
	for verdict := range verdictStrictness {
		if !strings.Contains(text, string(verdict)) {
			t.Fatalf("reviewer prompt never names the verdict %q", verdict)
		}
	}
	for severity := range severities {
		if !strings.Contains(text, "`"+string(severity)+"`") {
			t.Fatalf("reviewer prompt never names the severity %q", severity)
		}
	}
}

// A prompt can name every key and still teach a finding shape the parser
// refuses, which is the failure that is worst to debug: the verdict is right,
// the review is refused, and the reason is a dash. So the example finding in
// the prompt is parsed, not eyeballed.
func TestTheExampleFindingInTheShippedPromptParses(t *testing.T) {
	prompt, err := os.ReadFile(filepath.Join("..", "..", "examples", "prompts", "reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	var example string
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- [") {
			example = strings.TrimSpace(line)
			break
		}
	}
	if example == "" {
		t.Fatal("reviewer prompt shows no example finding for a reviewer to copy")
	}
	report, err := Parse("VERDICT: changes-requested\nFINDINGS:\n" + example + "\n")
	if err != nil {
		t.Fatalf("the example finding the prompt tells reviewers to copy does not parse: %v", err)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("findings = %#v, want exactly the example", report.Findings)
	}
	finding := report.Findings[0]
	if finding.Path == "" || finding.Issue == "" || finding.Recommendation == "" {
		t.Fatalf("the example finding teaches an incomplete shape: %#v", finding)
	}
}
