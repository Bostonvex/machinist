package factoryrun

import (
	"testing"
	"time"
)

func TestRenderParseRoundTrip(t *testing.T) {
	e := Evidence{
		JobID: "job-1", RunID: "run-1", AttemptID: "attempt-1",
		Branch: "codex/x", PR: "12", Repo: "o/r",
		Verdict: "ready-for-human-review",
		Issues:  []string{"#4"},
		Checks: []Check{
			{Name: "test", State: "success", DetailsURL: "https://example.test"},
			{Name: "vet", State: "success"},
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
}

func TestParseRejectsMissingMarker(t *testing.T) {
	if _, err := Parse("no marker here"); err == nil {
		t.Fatal("expected error for missing marker")
	}
}
