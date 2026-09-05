package cli

import (
	"bytes"
	"database/sql"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

// boardControlPlane stands up a control plane the board command can read and
// hands back both the config that reaches it and the store behind it, so a test
// can put work on the board and then look at it the way an operator would.
func boardControlPlane(t *testing.T) (string, *controlplane.Store, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("Plan:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath,
		[]byte("[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "machinist.db")
	store, err := controlplane.OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, err := controlplane.NewServerWithOptions(store, definitionPath, "secret", 0, controlplane.ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	workerDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(workerDirectory, "token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(workerDirectory, "worker.toml")
	body := "[control_plane]\nurl = " + strconv.Quote(web.URL) + "\ntoken_file = \"token\"\n"
	if err := os.WriteFile(workerPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return workerPath, store, databasePath
}

// editDatabase reaches past the control plane to put the database into a state
// nothing in machinist would produce: a job state from a build that does not
// exist yet, or a table that has gone missing. Both are conditions the board
// claims to survive, and neither can be reached through the API that is being
// tested.
func editDatabase(t *testing.T, path, statement string, arguments ...any) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(t.Context(), statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

func runBoard(t *testing.T, workerPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), append([]string{"board", "--config", workerPath}, args...),
		strings.NewReader(""), &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), code
}

func boardJob(t *testing.T, store *controlplane.Store, prompt string) string {
	t.Helper()
	id, err := store.CreateJob(t.Context(), prompt, "machinist", "implement",
		config.ResolvedCommand{Name: "implement", Executor: "codex", Prompt: prompt, Timeout: time.Minute, Hash: "implement-hash"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTheBoardShowsQueuedWorkUnderItsLane(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	stdout, stderr, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "QUEUED (1)") {
		t.Fatalf("board = %q, want a queued lane with one card", stdout)
	}
	if !strings.Contains(stdout, "Fix the flaky proxy test") {
		t.Fatalf("board = %q, want the card titled with what was asked for", stdout)
	}
}

func TestTheBoardShowsEmptyLanesRatherThanHidingThem(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	stdout, _, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	// An operator scanning for "nothing is waiting on review" needs to read a
	// zero, not notice that a heading they were not looking for is missing.
	if !strings.Contains(stdout, "REVIEW (0)") {
		t.Fatalf("board = %q, want an empty review lane shown with its count", stdout)
	}
}

func TestAnEmptyBoardSaysSoRatherThanPrintingHeadings(t *testing.T) {
	workerPath, _, _ := boardControlPlane(t)
	stdout, _, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	if !strings.Contains(stdout, "no work on the board") {
		t.Fatalf("board = %q, want it to say there is no work", stdout)
	}
}

func TestAJobInAnUnknownStateIsShownWithTheStateItIsActuallyIn(t *testing.T) {
	workerPath, store, databasePath := boardControlPlane(t)
	id := boardJob(t, store, "Fix the flaky proxy test")
	editDatabase(t, databasePath, "UPDATE jobs SET state='marinating' WHERE id=?", id)
	stdout, _, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	if !strings.Contains(stdout, "OTHER (1)") {
		t.Fatalf("board = %q, want the job in the other lane rather than gone", stdout)
	}
	if !strings.Contains(stdout, `unrecognised state "marinating"`) {
		t.Fatalf("board = %q, want it to say which state it did not recognise", stdout)
	}
}

func TestAskingForALaneThatDoesNotExistIsRefusedNotAnsweredWithNothing(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	stdout, stderr, code := runBoard(t, workerPath, "--lane", "doign")
	if code == 0 {
		t.Fatalf("a misspelled lane exited 0 with %q; an empty board and a typo must not look the same", stdout)
	}
	if !strings.Contains(stderr, "doign") || !strings.Contains(stderr, "queued") {
		t.Fatalf("stderr = %q, want the bad name and the lanes that do exist", stderr)
	}
}

func TestALaneFilterShowsOnlyThatLane(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	stdout, _, code := runBoard(t, workerPath, "--lane", "queued")
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	if !strings.Contains(stdout, "QUEUED (1)") {
		t.Fatalf("board = %q, want the requested lane", stdout)
	}
	if strings.Contains(stdout, "DONE") {
		t.Fatalf("board = %q, want only the requested lane", stdout)
	}
}

func TestTheJSONBoardCarriesTheFullJobIdentifier(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	id := boardJob(t, store, "Fix the flaky proxy test")
	stdout, _, code := runBoard(t, workerPath, "--json")
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	// The table shortens identifiers to stay readable, so JSON has to carry the
	// whole one or nothing can be scripted against the board.
	if !strings.Contains(stdout, id) {
		t.Fatalf("json board = %q, want the full job id %q", stdout, id)
	}
}

func TestARunningJobNamesTheWorkerHoldingIt(t *testing.T) {
	workerPath, store, _ := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	run, err := store.Poll(t.Context(), protocol.PollRequest{
		InstanceID: "worker-a", Name: "shop-floor",
		Executors: []string{"codex"}, Repositories: []string{"machinist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("expected the job to be dispatched")
	}
	stdout, _, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d", code)
	}
	if !strings.Contains(stdout, "RUNNING (1)") || !strings.Contains(stdout, "shop-floor") {
		t.Fatalf("board = %q, want the running lane to name the worker", stdout)
	}
}

func TestABoardTheControlPlaneCannotServeIsAnErrorNotAnEmptyBoard(t *testing.T) {
	workerPath, store, databasePath := boardControlPlane(t)
	boardJob(t, store, "Fix the flaky proxy test")
	editDatabase(t, databasePath, "DROP TABLE review_assignments")
	stdout, stderr, code := runBoard(t, workerPath)
	if code == 0 {
		t.Fatalf("board exited 0 with %q; a board that could not be read must not print as a quiet fleet", stdout)
	}
	if strings.Contains(stdout, "no work on the board") {
		t.Fatalf("board = %q, want no claim that there is no work", stdout)
	}
	if !strings.Contains(stderr, "read board") {
		t.Fatalf("stderr = %q, want it to say the board could not be read", stderr)
	}
}

// The one lane where nothing will happen until a person acts says so on the
// card. A parked card that reads like a running one -- a worker name and
// nothing else -- is the failure this lane was added to fix, moved from the
// lane heading down into the row.
func TestTheBoardSaysWhyAParkedCardIsParked(t *testing.T) {
	workerPath, store, databasePath := boardControlPlane(t)
	id := boardJob(t, store, "Rewrite the observability document")
	run, err := store.Poll(t.Context(), protocol.PollRequest{
		InstanceID: "worker-a", Name: "shop-floor",
		Executors: []string{"codex"}, Repositories: []string{"machinist"},
	})
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	// What the marker publisher writes once it has checked the run's claim to
	// have finished against the issue it was working.
	editDatabase(t, databasePath,
		`INSERT INTO github_run_markers(job_id,run_state,attempt_id,stage,published_at) VALUES(?,'succeeded','attempt_one','parked','2026-09-04T12:00:00Z')`, id)

	stdout, stderr, code := runBoard(t, workerPath)
	if code != 0 {
		t.Fatalf("board exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "PARKED (1)") {
		t.Fatalf("board = %q, want the run in a parked lane", stdout)
	}
	if strings.Contains(stdout, "DONE (1)") {
		t.Fatalf("board = %q, want a run waiting on a person not to be counted as done", stdout)
	}
	if !strings.Contains(stdout, "stopped, waiting on a person") {
		t.Fatalf("board = %q, want the card to say why it is parked", stdout)
	}
}
