package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factoryrun"
	"github.com/owainlewis/machinist/internal/protocol"
)

func boardStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
}

// queueJob puts one job on the board in its starting state.
func queueJob(t *testing.T, store *Store, prompt string) string {
	t.Helper()
	id, err := store.CreateJob(t.Context(), prompt, "machinist", "implement",
		testAgent("implement", "implement request"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// dispatch hands the next queued job to a worker and returns the run.
func dispatch(t *testing.T, store *Store) *protocol.RunSpec {
	t.Helper()
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("expected a run to be dispatched")
	}
	return run
}

func finish(t *testing.T, store *Store, run *protocol.RunSpec, state string) {
	t.Helper()
	completion := protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: state}
	switch state {
	case "succeeded":
	case "cancelled":
		// The store holds cancellation to the exit code a signalled process
		// actually leaves behind, so a test may not invent a different one.
		completion.ExitCode = 130
		completion.Error = "the operator stopped it"
	default:
		// The store refuses a failure that exited zero, which is the same
		// fail-closed rule the board depends on: an outcome has to say what it
		// was.
		completion.ExitCode = 1
		completion.Error = "the agent gave up"
	}
	if err := store.Complete(t.Context(), run.ID, completion); err != nil {
		t.Fatal(err)
	}
}

func readBoard(t *testing.T, store *Store) Board {
	t.Helper()
	board, err := store.Board(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return board
}

// lane returns the cards in one lane, failing if the board has no such lane at
// all: a test that silently reads an absent lane as empty proves nothing.
func lane(t *testing.T, board Board, name Lane) []Card {
	t.Helper()
	for _, column := range board.Columns {
		if column.Lane == name {
			return column.Cards
		}
	}
	t.Fatalf("board has no %s lane; lanes = %v", name, laneNames(board))
	return nil
}

func laneNames(board Board) []string {
	names := make([]string, 0, len(board.Columns))
	for _, column := range board.Columns {
		names = append(names, string(column.Lane))
	}
	return names
}

// findCard locates a job anywhere on the board, so a test can assert which lane
// it is in rather than only that it is in the one expected.
func findCard(t *testing.T, board Board, jobID string) Card {
	t.Helper()
	for _, column := range board.Columns {
		for _, card := range column.Cards {
			if card.JobID == jobID {
				return card
			}
		}
	}
	t.Fatalf("job %s is not on the board at all", jobID)
	return Card{}
}

func TestAQueuedJobIsInTheQueuedLane(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	card := findCard(t, readBoard(t, store), id)
	if card.Lane != LaneQueued {
		t.Fatalf("lane = %q, want %q", card.Lane, LaneQueued)
	}
	if !card.Recognised {
		t.Fatal("a queued job should be a state the board recognises")
	}
}

func TestADispatchedJobMovesToRunningAndNamesItsWorker(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	dispatch(t, store)
	card := findCard(t, readBoard(t, store), id)
	if card.Lane != LaneRunning {
		t.Fatalf("lane = %q, want %q", card.Lane, LaneRunning)
	}
	if card.Worker == "" {
		t.Fatal("a running card should say which worker has it, or the lane tells the operator nothing they can act on")
	}
}

func TestAFinishedJobLandsInTheLaneItsOutcomeDeserves(t *testing.T) {
	for outcome, want := range map[string]Lane{
		"succeeded": LaneDone,
		"failed":    LaneFailed,
	} {
		t.Run(outcome, func(t *testing.T) {
			store := boardStore(t)
			id := queueJob(t, store, "do the thing")
			finish(t, store, dispatch(t, store), outcome)
			card := findCard(t, readBoard(t, store), id)
			if card.Lane != want {
				t.Fatalf("%s landed in %q, want %q", outcome, card.Lane, want)
			}
		})
	}
}

func TestAJobInAStateTheBoardDoesNotKnowIsShownAnywayInOther(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	// A state from a newer build, or a hand-edited row. Either way the operator
	// needs to see the work, not lose it.
	if _, err := store.db.ExecContext(t.Context(), `UPDATE jobs SET state='marinating' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	board := readBoard(t, store)
	card := findCard(t, board, id)
	if card.Lane != LaneOther {
		t.Fatalf("lane = %q, want %q", card.Lane, LaneOther)
	}
	if card.Recognised {
		t.Fatal("a state the board has no lane for must not be reported as recognised")
	}
	if card.State != "marinating" {
		t.Fatalf("state = %q, want the raw state kept so the card explains itself", card.State)
	}
}

func TestARunAwaitingAVerdictIsInReviewEvenThoughItSucceeded(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	run := dispatch(t, store)
	finish(t, store, run, "succeeded")
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO review_assignments(reviewed_run_id,pull_request,reviewer_job_id,reviewer_command,assigned_at)
		 VALUES(?,?,?,?,?)`, run.ID, 41, "reviewer-job", "review", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	card := findCard(t, readBoard(t, store), id)
	if card.Lane != LaneReview {
		t.Fatalf("lane = %q, want %q: a run nobody has accepted yet is not done", card.Lane, LaneReview)
	}
	if card.PullRequest != 41 {
		t.Fatalf("pull request = %d, want 41", card.PullRequest)
	}
	if card.AwaitingFrom == "" {
		t.Fatal("a review card should name who it is waiting on")
	}
}

func TestAReviewedRunLeavesTheReviewLaneOnceAVerdictExists(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	run := dispatch(t, store)
	finish(t, store, run, "succeeded")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO review_assignments(reviewed_run_id,pull_request,reviewer_job_id,reviewer_command,assigned_at)
		 VALUES(?,?,?,?,?)`, run.ID, 41, "reviewer-job", "review", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO run_reviews(run_id,reviewer_run_id,pull_request,verdict,recorded_at) VALUES(?,?,?,?,?)`,
		run.ID, "reviewer-run", 41, "approved", now); err != nil {
		t.Fatal(err)
	}
	card := findCard(t, readBoard(t, store), id)
	if card.Lane != LaneDone {
		t.Fatalf("lane = %q, want %q once a verdict exists", card.Lane, LaneDone)
	}
	if card.Verdict != "approved" {
		t.Fatalf("verdict = %q, want it carried onto the card", card.Verdict)
	}
	if card.AwaitingFrom != "" {
		t.Fatalf("awaiting = %q, want nothing: the wait is over", card.AwaitingFrom)
	}
}

func TestAReviewRowWithNoVerdictIsAnErrorNotAnAcceptance(t *testing.T) {
	store := boardStore(t)
	queueJob(t, store, "do the thing")
	run := dispatch(t, store)
	finish(t, store, run, "succeeded")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO run_reviews(run_id,reviewer_run_id,pull_request,verdict,recorded_at) VALUES(?,?,?,?,?)`,
		run.ID, "reviewer-run", 41, "   ", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Board(t.Context()); err == nil {
		t.Fatal("a review that says nothing must not be read as a review that said yes")
	}
}

func TestEveryLaneIsAlwaysPresentEvenWhenEmpty(t *testing.T) {
	board := readBoard(t, boardStore(t))
	if got, want := len(board.Columns), len(laneOrder); got != want {
		t.Fatalf("columns = %d, want %d", got, want)
	}
	for index, column := range board.Columns {
		if column.Lane != laneOrder[index] {
			t.Fatalf("column %d = %q, want %q: the lanes are shown in the order work moves through them",
				index, column.Lane, laneOrder[index])
		}
		if column.Cards == nil {
			t.Fatalf("lane %q has nil cards, which encodes as JSON null rather than an empty column", column.Lane)
		}
	}
}

func TestEveryLaneTheStateMapCanProduceHasAColumn(t *testing.T) {
	placed := map[Lane]bool{}
	for _, name := range laneOrder {
		placed[name] = true
	}
	for state, name := range laneByJobState {
		if !placed[name] {
			t.Fatalf("state %q maps to lane %q, which has no column: work in it would be dropped", state, name)
		}
	}
	if !placed[LaneOther] {
		t.Fatal("LaneOther has no column, so unrecognised work would vanish rather than land in it")
	}
}

func TestACardIsTitledWithWhatSomeoneWroteNotItsIdentifier(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "Fix the flaky proxy test\n\nIt fails only on Linux.")
	card := findCard(t, readBoard(t, store), id)
	if card.Title != "Fix the flaky proxy test" {
		t.Fatalf("title = %q, want the first line of the prompt", card.Title)
	}
}

func TestAGitHubIssueTitleWinsOverThePrompt(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "the rendered prompt, which is not what a person wrote")
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE jobs SET github_issue_title='Proxy drops upstream on Linux' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	card := findCard(t, readBoard(t, store), id)
	if card.Title != "Proxy drops upstream on Linux" {
		t.Fatalf("title = %q, want the issue title", card.Title)
	}
}

// recordStage says what a job's published marker described, which is the only
// thing on the board that was checked against anything outside this control
// plane.
func recordStage(t *testing.T, store *Store, jobID, runState string, stage factoryrun.Stage) {
	t.Helper()
	if err := store.RecordPublishedMarker(t.Context(),
		GitHubMarkerTarget{JobID: jobID, RunState: runState, AttemptID: "attempt_one"}, stage); err != nil {
		t.Fatal(err)
	}
}

// A card only leaves the done lane, because done is the only lane that says
// something untrue about a run that stopped to ask a person. Cancelled work was
// stopped by the operator reading the board, and telling them it is waiting for
// them is noise, not news.
func TestOnlyTheDoneLaneGivesACardUpToParked(t *testing.T) {
	for _, outcome := range []struct {
		state string
		lane  Lane
	}{
		{"succeeded", LaneParked},
		{"cancelled", LaneCancelled},
		{"failed", LaneFailed},
	} {
		t.Run(outcome.state, func(t *testing.T) {
			store := boardStore(t)
			id := queueJob(t, store, "do the thing")
			finish(t, store, dispatch(t, store), outcome.state)
			recordStage(t, store, id, outcome.state, factoryrun.StageParked)
			if got := findCard(t, readBoard(t, store), id).Lane; got != outcome.lane {
				t.Fatalf("a %s job with a parked marker is in lane %q, want %q", outcome.state, got, outcome.lane)
			}
		})
	}
}

// Markers published before the stage was recorded carry no stage. That is not a
// stage of "finished" and it is not one of "parked": it is the absence of an
// answer, and the board leaves the card where the run's own state put it.
func TestAMarkerFromBeforeStagesWereRecordedMovesNothing(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	finish(t, store, dispatch(t, store), "succeeded")
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO github_run_markers(job_id,run_state,attempt_id,published_at) VALUES(?,'succeeded','attempt_one','2026-09-04T12:00:00Z')`, id); err != nil {
		t.Fatal(err)
	}
	if got := findCard(t, readBoard(t, store), id).Lane; got != LaneDone {
		t.Fatalf("a marker with no stage moved the card to %q", got)
	}
}

// The stage a marker described is read from the store like everything else on
// the board, so a read of it that fails fails the whole board rather than
// quietly rendering every parked run as finished.
func TestAnUnreadableMarkerTableIsAnErrorNotAQuietBoard(t *testing.T) {
	store := boardStore(t)
	id := queueJob(t, store, "do the thing")
	finish(t, store, dispatch(t, store), "succeeded")
	recordStage(t, store, id, "succeeded", factoryrun.StageParked)
	if _, err := store.db.ExecContext(t.Context(), `DROP TABLE github_run_markers`); err != nil {
		t.Fatal(err)
	}
	board, err := store.Board(t.Context())
	if err == nil {
		t.Fatalf("board = %#v, want an error: an unreadable stage must not render as finished", board)
	}
	if len(board.Columns) != 0 {
		t.Fatalf("columns = %d, want no board at all when the read failed", len(board.Columns))
	}
}

func TestAnUnreadableBoardIsAnErrorNotAnEmptyBoard(t *testing.T) {
	store := boardStore(t)
	queueJob(t, store, "do the thing")
	// The read fails after the board is known to be non-empty, so an empty
	// result would be a lie rather than a coincidence.
	if _, err := store.db.ExecContext(t.Context(), `DROP TABLE review_assignments`); err != nil {
		t.Fatal(err)
	}
	board, err := store.Board(t.Context())
	if err == nil {
		t.Fatalf("board = %#v, want an error: an unreadable board must not render as a quiet one", board)
	}
	if len(board.Columns) != 0 {
		t.Fatalf("columns = %d, want no board at all when the read failed", len(board.Columns))
	}
}

func boardServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	web, _, store := leaseServerWithStore(t)
	return web, store
}

func getBoard(t *testing.T, web *httptest.Server) (int, Board) {
	t.Helper()
	response, err := http.Get(web.URL + "/api/v1/board")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var board Board
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&board); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, board
}

func TestTheBoardIsServedOverTheAPI(t *testing.T) {
	web, store := boardServer(t)
	id := queueJob(t, store, "do the thing")
	status, board := getBoard(t, web)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := findCard(t, board, id).Lane; got != LaneQueued {
		t.Fatalf("lane = %q, want %q", got, LaneQueued)
	}
}

func TestAnUnreadableBoardIsNotServedAsAnEmptyOne(t *testing.T) {
	web, store := boardServer(t)
	queueJob(t, store, "do the thing")
	if _, err := store.db.ExecContext(t.Context(), `DROP TABLE review_assignments`); err != nil {
		t.Fatal(err)
	}
	status, board := getBoard(t, web)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: a board that could not be read is not a board with no work on it", status)
	}
	if len(board.Columns) != 0 {
		t.Fatalf("columns = %d, want none", len(board.Columns))
	}
}
