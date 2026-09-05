package factoryrun

import (
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

func TestRenderParseRoundTrip(t *testing.T) {
	e := Evidence{
		JobID: "job-1", RunID: "run-1", AttemptID: "attempt-1",
		Branch: "codex/x", PR: "12", Repo: "o/r",
		Verdict: review.VerdictReady,
		Issues:  []string{"#4"},
		Checks: []Check{
			{Name: "test", State: CheckSuccess, DetailsURL: "https://example.test"},
			{Name: "vet", State: CheckSuccess},
		},
		UpdatedAt: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
	}
	body, err := Render(e)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v\nbody:\n%s", err, body)
	}
	if got.JobID != e.JobID || got.RunID != e.RunID || got.Repo != e.Repo || got.Verdict != e.Verdict {
		t.Fatalf("parse mismatch: %+v", got)
	}
	if len(got.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(got.Checks))
	}
	if got.Checks[0].DetailsURL != "https://example.test" {
		t.Fatalf("details url lost: %+v", got.Checks[0])
	}
	if !got.ChecksPassing() {
		t.Fatal("all-success evidence should report checks passing")
	}
}

func TestParseRejectsMissingMarker(t *testing.T) {
	if _, err := Parse("no marker here"); err == nil {
		t.Fatal("expected error for missing marker")
	}
}

// Prose that names the marker is not evidence: only the rendered anchor is.
func TestParseRejectsUnanchoredMention(t *testing.T) {
	body := strings.Join([]string{
		"I ran " + MarkerKey + " by hand.",
		"repo: o/r",
		"job: j",
		"run: r",
	}, "\n")
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error: a comment mentioning the marker is not evidence")
	}
}

// Only lines after the anchor are evidence, so prose above it cannot seed fields.
func TestParseIgnoresTextBeforeAnchor(t *testing.T) {
	body := "job: spoofed\n" + markerAnchor + "\nrepo: o/r\njob: real\nrun: r\n"
	got, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.JobID != "real" {
		t.Fatalf("expected job from marker body, got %q", got.JobID)
	}
}

// A check line without an explicit state must never read as a passing check.
func TestParseRejectsCheckWithoutState(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\ncheck: Linux checks\n"
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error for check line with no state")
	}
}

func TestParseRejectsUnknownCheckState(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\ncheck: test:probably-fine\n"
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error for unrecognized check state")
	}
}

func TestParseRejectsUnknownVerdict(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\nverdict: totally-approved-merge-it\n"
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error for a verdict the review engine cannot produce")
	}
}

func TestParseRejectsUnparsableTimestamp(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\nupdated_at: yesterday\n"
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error for unparsable updated_at")
	}
}

func TestParseAcceptsEvidenceWithoutVerdict(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\n"
	got, err := Parse(body)
	if err != nil {
		t.Fatalf("in-progress evidence rejected: %v", err)
	}
	if got.Verdict != "" {
		t.Fatalf("expected no verdict, got %q", got.Verdict)
	}
}

func TestParseRejectsUnknownStage(t *testing.T) {
	body := markerAnchor + "\nrepo: o/r\njob: j\nrun: r\nstage: nearly-done\n"
	if _, err := Parse(body); err == nil {
		t.Fatal("expected error for a stage outside the closed set")
	}
}

func TestRenderParseRoundTripsStage(t *testing.T) {
	for _, stage := range []Stage{StageClaimed, StageRunning, StageComplete, StageFailed, StageParked} {
		body, err := Render(Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: stage})
		if err != nil {
			t.Fatalf("render %q: %v", stage, err)
		}
		got, err := Parse(body)
		if err != nil {
			t.Fatalf("parse %q: %v", stage, err)
		}
		if got.Stage != stage {
			t.Fatalf("stage %q round tripped as %q", stage, got.Stage)
		}
	}
}

func TestIsMarkerRequiresTheAnchor(t *testing.T) {
	if IsMarker("I ran " + MarkerKey + " by hand") {
		t.Fatal("prose naming the marker is not a marker")
	}
	body, err := Render(Evidence{JobID: "j", RunID: "r", Repo: "o/r"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !IsMarker("Here is the state.\n\n" + body) {
		t.Fatal("a comment containing a rendered marker is a marker")
	}
}
