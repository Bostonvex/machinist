package controlplane

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/factoryrun"
)

// publishedStage reads back what a job's marker was recorded as describing.
func publishedStage(t *testing.T, store *Store, jobID string) string {
	t.Helper()
	var stage string
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT stage FROM github_run_markers WHERE job_id=?`, jobID).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	return stage
}

func TestADatabaseWrittenBeforeTheStageColumnExistedIsUpgradedNotRejected(t *testing.T) {
	// The column is added to a github_run_markers that is already there. The
	// schema block only creates tables that are absent, so without the upgrade
	// an existing deployment would keep the old shape and every publication
	// would fail on a column that is not there.
	path := filepath.Join(t.TempDir(), "machinist.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), `CREATE TABLE github_run_markers (
 job_id TEXT PRIMARY KEY, run_state TEXT NOT NULL, attempt_id TEXT NOT NULL,
 verdict TEXT NOT NULL DEFAULT '', pull_request INTEGER NOT NULL DEFAULT 0, published_at TEXT NOT NULL);
INSERT INTO github_run_markers(job_id,run_state,attempt_id,published_at)
 VALUES('job_old','succeeded','attempt_one','2026-01-01T00:00:00Z');
PRAGMA user_version=14;`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, path)
	// The row survives carrying no stage. That is the truth about it: it was
	// published when nothing checked a completion claim against the issue, so
	// nothing here knows whether that run finished or stopped to ask. An
	// upgrade that filled the blank in with "complete" would restate the very
	// claim the column exists to stop taking on trust.
	if stage := publishedStage(t, store, "job_old"); stage != "" {
		t.Fatalf("an old marker came back claiming stage %q", stage)
	}
}

func TestUpgradingADatabaseThatAlreadyHasTheStageColumnChangesNothing(t *testing.T) {
	// Opening twice is the ordinary case -- every restart does it -- and an
	// ALTER TABLE that runs a second time is an error, not a no-op.
	path := filepath.Join(t.TempDir(), "machinist.db")
	first := openTestStore(t, path)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTestStore(t, path)
	var columns int
	if err := second.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM pragma_table_info('github_run_markers') WHERE name='stage'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 1 {
		t.Fatalf("github_run_markers has %d stage columns", columns)
	}
}

// An empty stage is what a row written before this column carries, and it reads
// as "nobody knows". A publication that just happened does know, so it is not
// allowed to record the same silence.
func TestAPublicationMustNameTheStageItDescribed(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	err := store.RecordPublishedMarker(t.Context(),
		GitHubMarkerTarget{JobID: id, RunState: "succeeded", AttemptID: "attempt_one"}, "")
	if err == nil {
		t.Fatal("expected a publication with no stage to be refused")
	}
	if !strings.Contains(err.Error(), "stage") {
		t.Fatalf("error does not say what was missing: %v", err)
	}
	var rows int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM github_run_markers WHERE job_id=?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a refused publication wrote %d rows", rows)
	}
}

// The stage recorded is the one that was published, not the one the run state
// would have implied on its own. Those differ exactly when it matters.
func TestTheRecordedStageIsTheOneThatWasPublished(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	finish(t, store, dispatch(t, store), "succeeded")
	if err := store.RecordPublishedMarker(t.Context(),
		GitHubMarkerTarget{JobID: id, RunState: "succeeded", AttemptID: "attempt_one"}, factoryrun.StageParked); err != nil {
		t.Fatal(err)
	}
	if stage := publishedStage(t, store, id); stage != string(factoryrun.StageParked) {
		t.Fatalf("stage = %q, want %q", stage, factoryrun.StageParked)
	}
}

// terminalLabelSentence is where the shipped foreman prompt names the labels an
// agent may leave on an issue when it stops. Reading them from there is what
// keeps the publisher and the prompt describing the same thing: a label the
// prompt tells an agent to set and this package has never heard of parks
// nothing, and the run is shown as finished exactly as before.
var (
	terminalLabelSentence = "Before any terminal stop or handoff using"
	promptLabelPattern    = regexp.MustCompile("`(machinist:[a-z-]+)`")
)

// TestEveryTerminalLabelTheForemanSetsIsAccountedFor pins the halting set from
// both sides. The prompt's terminal labels must each be either halting or the
// one that is not a stop at all, and every halting label must be one the prompt
// actually sets.
func TestEveryTerminalLabelTheForemanSetsIsAccountedFor(t *testing.T) {
	body, err := machinistexamples.Files.ReadFile("prompts/foreman.md")
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(body)
	start := strings.Index(prompt, terminalLabelSentence)
	if start < 0 {
		t.Fatalf("the shipped foreman prompt no longer says %q, so nothing here knows which labels are terminal", terminalLabelSentence)
	}
	rest := prompt[start:]
	end := strings.Index(rest, "persist")
	if end < 0 {
		t.Fatal("the terminal-label sentence in the shipped foreman prompt has no end this test recognises")
	}
	var named []string
	for _, match := range promptLabelPattern.FindAllStringSubmatch(rest[:end], -1) {
		named = append(named, match[1])
	}
	if len(named) == 0 {
		t.Fatal("the terminal-label sentence names no labels")
	}

	// Handing the change to a reviewer is the work continuing, not the agent
	// giving it back. Every other terminal label is the agent stopping, and a
	// run that stopped is not a run that finished.
	const handoff = "machinist:ready-for-review"
	if !slices.Contains(named, handoff) {
		t.Fatalf("the prompt no longer names %s as a terminal label, so this test's one exception is stale", handoff)
	}
	for _, label := range named {
		if label == handoff {
			continue
		}
		if _, halting := haltingLabels[label]; !halting {
			t.Fatalf("the foreman prompt tells an agent to stop by setting %s, which parks nothing: a run that sets it is still shown as finished", label)
		}
	}
	for label := range haltingLabels {
		if !slices.Contains(named, label) {
			t.Fatalf("%s parks a run but no shipped prompt sets it: it can only be reached by a hand-labelled issue", label)
		}
	}
}
