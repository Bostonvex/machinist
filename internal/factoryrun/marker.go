package factoryrun

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

// MarkerKey identifies the FACTORY:RUN handoff marker in an issue comment.
const MarkerKey = "FACTORY:RUN"

// markerAnchor is the HTML comment that opens a rendered marker. Parsing is
// anchored on it so prose that merely mentions FACTORY:RUN is never read back
// as machine evidence.
const markerAnchor = "<!-- machinist:" + MarkerKey + " -->"

// Render produces the marker comment body. It is deterministic (checks are
// sorted) so parse(Render(e)) is a stable round trip.
func Render(e Evidence) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	checks := make([]Check, len(e.Checks))
	copy(checks, e.Checks)
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name != checks[j].Name {
			return checks[i].Name < checks[j].Name
		}
		return checks[i].State < checks[j].State
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", markerAnchor)
	fmt.Fprintf(&b, "repo: %s\n", e.Repo)
	fmt.Fprintf(&b, "job: %s\n", e.JobID)
	fmt.Fprintf(&b, "run: %s\n", e.RunID)
	if e.AttemptID != "" {
		fmt.Fprintf(&b, "attempt: %s\n", e.AttemptID)
	}
	if e.Branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", e.Branch)
	}
	if e.PR != "" {
		fmt.Fprintf(&b, "pr: %s\n", e.PR)
	}
	if e.Verdict != "" {
		fmt.Fprintf(&b, "verdict: %s\n", e.Verdict)
	}
	if len(e.Issues) > 0 {
		fmt.Fprintf(&b, "issues: %s\n", strings.Join(e.Issues, ","))
	}
	for _, c := range checks {
		line := c.Name + ":" + string(c.State)
		if c.DetailsURL != "" {
			line += ":" + c.DetailsURL
		}
		fmt.Fprintf(&b, "check: %s\n", line)
	}
	if !e.UpdatedAt.IsZero() {
		fmt.Fprintf(&b, "updated_at: %s\n", e.UpdatedAt.UTC().Format(time.RFC3339))
	}
	return b.String(), nil
}

// Parse reads a marker body back into an Evidence. It tolerates a whole-comment
// body: it reads only the lines that follow the marker anchor, so a comment
// that merely names the marker carries no evidence.
func Parse(body string) (Evidence, error) {
	var e Evidence
	_, rest, ok := strings.Cut(body, markerAnchor)
	if !ok {
		return e, fmt.Errorf("factoryrun: no %s marker", MarkerKey)
	}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "repo":
			e.Repo = value
		case "job":
			e.JobID = value
		case "run":
			e.RunID = value
		case "attempt":
			e.AttemptID = value
		case "branch":
			e.Branch = value
		case "pr":
			e.PR = value
		case "verdict":
			e.Verdict = review.Verdict(value)
		case "issues":
			e.Issues = splitCSV(value)
		case "check":
			c, err := parseCheck(value)
			if err != nil {
				return e, err
			}
			e.Checks = append(e.Checks, c)
		case "updated_at":
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return e, fmt.Errorf("factoryrun: updated_at %q: %w", value, err)
			}
			e.UpdatedAt = t
		}
	}
	if err := e.Validate(); err != nil {
		return e, err
	}
	return e, nil
}

// parseCheck reads one "name:state[:url]" check line. The state is required:
// a truncated or hand-edited line is an error, never a passing check.
func parseCheck(value string) (Check, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) < 2 {
		return Check{}, fmt.Errorf("factoryrun: check %q has no state", value)
	}
	c := Check{
		Name:  strings.TrimSpace(parts[0]),
		State: CheckState(strings.TrimSpace(parts[1])),
	}
	if len(parts) > 2 {
		c.DetailsURL = strings.TrimSpace(parts[2])
	}
	if c.Name == "" {
		return Check{}, fmt.Errorf("factoryrun: check %q has no name", value)
	}
	if !c.State.Valid() {
		return Check{}, fmt.Errorf("factoryrun: check %q has unknown state %q", c.Name, c.State)
	}
	return c, nil
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
