package notes

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docs/migration-plan.md states each phase's state twice: once in the status
// table near the top, and once on the phase's own heading in the task list.
// Those two are edited by different changes at different times, and the day
// they disagree the document lies to whoever reads only one of them. It has
// already happened -- the header claimed the plan was unexecuted while five of
// its six phases were closed -- so the agreement is checked rather than trusted.
//
// This lives in package notes because the package is the repository's
// durable-record machinery, and the plan is the record's most-read document.
// It is a document check, not a GitHub check: whether an issue is really closed
// cannot be established from a checkout, and a test that pretended otherwise
// would pass while offline and mean nothing.

var (
	phaseHeading   = regexp.MustCompile(`(?m)^### Phase ([A-Z]) — (.*)$`)
	headingState   = regexp.MustCompile(`_\((.*?)\)_\s*$`)
	issueReference = regexp.MustCompile(`^#\d+$`)
	// The issue cell is matched loosely rather than as `#\d+`, so that a row
	// which forgot its issue is reported as a row with no issue instead of not
	// being recognized as a row at all -- which would surface as the phase
	// missing from the table entirely, and send the reader to the wrong place.
	statusRow = regexp.MustCompile(`(?m)^\|\s*([A-Z]) — (.*?)\s*\|\s*(.*?)\s*\|\s*(.*?)\s*\|\s*$`)
)

// knownStates is the closed vocabulary. A state outside it is an error and not
// a state this test has not learned yet: "done", "shipped" and "delivered"
// would each read as fine and compare as different.
var knownStates = map[string]bool{"delivered": true, "in progress": true, "not started": true}

func TestThePlanAgreesWithItselfAboutWhatHasShipped(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "migration-plan.md")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration plan: %v", err)
	}
	// Line endings belong to the checkout, not the document: on Windows git
	// hands this back with CRLF and every pattern anchored to a line end stops
	// matching, which reads here as a plan with no phases in it.
	text := strings.ReplaceAll(string(source), "\r\n", "\n")

	headings := map[string]string{}
	for _, match := range phaseHeading.FindAllStringSubmatch(text, -1) {
		phase, title := match[1], match[2]
		state := headingState.FindStringSubmatch(title)
		if state == nil {
			t.Errorf("phase %s heading does not say what state it is in: %q", phase, match[0])
			continue
		}
		if !knownStates[state[1]] {
			t.Errorf("phase %s heading state %q is not one of %s", phase, state[1], statesList())
			continue
		}
		if previous, repeated := headings[phase]; repeated {
			t.Errorf("phase %s has two headings, %q and %q", phase, previous, state[1])
			continue
		}
		headings[phase] = state[1]
	}

	rows := map[string]string{}
	issues := map[string]string{}
	for _, match := range statusRow.FindAllStringSubmatch(text, -1) {
		phase, state, issue := match[1], match[3], match[4]
		if !knownStates[state] {
			t.Errorf("status table gives phase %s the state %q, which is not one of %s", phase, state, statesList())
			continue
		}
		if previous, repeated := rows[phase]; repeated {
			t.Errorf("phase %s has two status rows, %q and %q", phase, previous, state)
			continue
		}
		rows[phase] = state
		issues[phase] = issue
	}

	// A document with no phases in it passes every comparison below, which is
	// the one way this test can report success by reading nothing.
	if len(headings) == 0 || len(rows) == 0 {
		t.Fatalf("read %d phase headings and %d status rows: the patterns stopped matching", len(headings), len(rows))
	}

	for _, phase := range union(headings, rows) {
		heading, hasHeading := headings[phase]
		row, hasRow := rows[phase]
		switch {
		case !hasRow:
			t.Errorf("phase %s has a heading in the task list and no row in the status table", phase)
		case !hasHeading:
			t.Errorf("phase %s has a row in the status table and no heading in the task list", phase)
		case heading != row:
			t.Errorf("phase %s is %q in the status table and %q on its heading", phase, row, heading)
		}
	}

	for _, phase := range sortedKeys(issues) {
		if !issueReference.MatchString(issues[phase]) {
			t.Errorf("phase %s names %q where an issue like #9 belongs, so a reader has nowhere to check its state", phase, issues[phase])
		}
	}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func statesList() string {
	names := make([]string, 0, len(knownStates))
	for state := range knownStates {
		names = append(names, `"`+state+`"`)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func union(left, right map[string]string) []string {
	seen := map[string]bool{}
	for phase := range left {
		seen[phase] = true
	}
	for phase := range right {
		seen[phase] = true
	}
	phases := make([]string, 0, len(seen))
	for phase := range seen {
		phases = append(phases, phase)
	}
	sort.Strings(phases)
	return phases
}
