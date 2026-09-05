package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The board is the one view that answers "what is this fleet doing right now",
// laid out as lanes rather than as a list, because the question an operator
// actually has is where work is piling up.
//
// It replaces the factory kanban, which had to rebuild the state of every piece
// of work by reading GitHub issue labels and comments: nothing else knew it.
// This control plane already recorded every job it admitted, every run it
// dispatched and every attempt a worker made, so the board is a projection of
// what it knows and not a reconstruction of what it can see. That is the whole
// reason it can be trusted enough to act on.
//
// Two rules are carried over from the kanban, both learned the hard way:
//
//   - Work that does not fit a lane still gets a lane. A card in the wrong
//     column is a mistake an operator can see and correct; a card that is
//     simply absent is a mistake nobody can see at all.
//   - A read that failed is reported as a failed read. Rendering an unreadable
//     board as an empty one tells the operator that nothing is happening, which
//     is the opposite of what is true.

// Lane is a column of the board.
type Lane string

const (
	LaneQueued    Lane = "queued"
	LaneRunning   Lane = "running"
	LaneReview    Lane = "review"
	LaneDone      Lane = "done"
	LaneFailed    Lane = "failed"
	LaneCancelled Lane = "cancelled"

	// LaneOther holds work whose state this build does not recognise. It exists
	// so that adding a state to the store cannot make work disappear from the
	// board of an older binary that has not been taught about it yet.
	LaneOther Lane = "other"
)

// laneByJobState is the only place a job state and a lane are related. Any
// state missing from it lands in LaneOther by design rather than by omission.
var laneByJobState = map[string]Lane{
	"queued":    LaneQueued,
	"running":   LaneRunning,
	"succeeded": LaneDone,
	"failed":    LaneFailed,
	"abandoned": LaneFailed,
	"cancelled": LaneCancelled,
}

// laneOrder is the left-to-right order of the columns, which is the order work
// moves through them. LaneOther is last because reaching it should feel like
// reaching the end of what the board understood.
var laneOrder = []Lane{LaneQueued, LaneRunning, LaneReview, LaneDone, LaneFailed, LaneCancelled, LaneOther}

// laneForJobState answers which lane a state belongs in, and says whether it
// recognised the state. Callers need the second answer: an unrecognised state
// is worth showing to the operator, not silently absorbing.
func laneForJobState(state string) (Lane, bool) {
	lane, known := laneByJobState[strings.TrimSpace(state)]
	if !known {
		return LaneOther, false
	}
	return lane, true
}

// Card is one piece of work on the board.
type Card struct {
	JobID      string `json:"job_id"`
	Repository string `json:"repository"`
	// Title is what the work is, in the words it arrived with. It is the GitHub
	// issue title when the job came from an issue and the first line of the
	// prompt otherwise, because a board of job identifiers tells an operator
	// nothing they did not already know.
	Title   string `json:"title"`
	Command string `json:"command"`
	// State is the raw state the store holds, kept even when it mapped cleanly.
	// For a card in LaneOther it is the only thing that explains why it is
	// there, and reading it from the card beats going back to the database.
	State string `json:"state"`
	Lane  Lane   `json:"lane"`
	// Recognised is false when this build had no lane for State. It is on the
	// card rather than inferred from the lane because a card can be in
	// LaneOther for exactly one reason, and saying so is cheaper than making
	// every reader work it out.
	Recognised   bool      `json:"recognised"`
	Worker       string    `json:"worker,omitempty"`
	Attempt      int       `json:"attempt,omitempty"`
	MaxAttempts  int       `json:"max_attempts,omitempty"`
	ErrorClass   string    `json:"error_class,omitempty"`
	Error        string    `json:"error,omitempty"`
	PullRequest  int       `json:"pull_request,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	AwaitingFrom string    `json:"awaiting_from,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Column is one lane and the work in it.
type Column struct {
	Lane  Lane   `json:"lane"`
	Cards []Card `json:"cards"`
}

// Board is every column, always all of them, in laneOrder. Empty columns are
// kept: a board that drops its empty lanes changes shape as work moves, and an
// operator reading it has to notice an absence rather than read a zero.
type Board struct {
	Columns     []Column  `json:"columns"`
	GeneratedAt time.Time `json:"generated_at"`
}

// reviewStanding is what the review tables say about one run.
type reviewStanding struct {
	pullRequest int
	verdict     string
	reviewer    string
}

// Board projects the jobs this control plane knows about into lanes.
//
// It fails whole rather than partially: any read that does not come back is an
// error and no board is returned. A board assembled from the parts that
// happened to load looks exactly like a quiet fleet.
func (s *Store) Board(ctx context.Context) (Board, error) {
	jobs, err := s.listJobs(ctx)
	if err != nil {
		return Board{}, fmt.Errorf("read board: %w", err)
	}
	standings, err := s.reviewStandings(ctx)
	if err != nil {
		return Board{}, fmt.Errorf("read board: %w", err)
	}
	byLane := make(map[Lane][]Card, len(laneOrder))
	for _, job := range jobs {
		card := cardForJob(job, standings)
		byLane[card.Lane] = append(byLane[card.Lane], card)
	}
	board := Board{Columns: make([]Column, 0, len(laneOrder)), GeneratedAt: s.now().UTC()}
	for _, lane := range laneOrder {
		cards := byLane[lane]
		if cards == nil {
			cards = []Card{}
		}
		board.Columns = append(board.Columns, Column{Lane: lane, Cards: cards})
	}
	return board, nil
}

// cardForJob turns one job into its card. The state map decides the lane first;
// the review standing can then move the card, because a run that has been sent
// for review is not finished no matter what its own state says. Those are
// different questions — the state records what the run did, the assignment
// records whether anyone has accepted it — so they are answered separately
// rather than folded into one table that would have to encode both.
func cardForJob(job Job, standings map[string]reviewStanding) Card {
	lane, recognised := laneForJobState(job.State)
	card := Card{
		JobID:      job.ID,
		Repository: job.Repository,
		Title:      jobTitle(job),
		Command:    job.Command,
		State:      job.State,
		Lane:       lane,
		Recognised: recognised,
		UpdatedAt:  job.UpdatedAt,
	}
	if len(job.Runs) > 0 {
		run := job.Runs[0]
		card.Worker = run.WorkerName
		card.Attempt = run.AttemptCount
		card.MaxAttempts = run.MaxAttempts
		card.ErrorClass = run.LastErrorClass
		card.Error = run.Error
		if standing, found := standings[run.ID]; found {
			card.PullRequest = standing.pullRequest
			card.Verdict = standing.verdict
			if standing.verdict == "" {
				card.Lane = LaneReview
				card.AwaitingFrom = standing.reviewer
			}
		}
	}
	return card
}

// jobTitle is the sentence a person wrote about this work. A job from a GitHub
// issue carries the issue title; anything else carries a prompt, whose first
// line is what someone would have written as a title had they been asked for
// one.
func jobTitle(job Job) string {
	if title := strings.TrimSpace(job.GitHubIssueTitle); title != "" {
		return title
	}
	if subject := strings.TrimSpace(job.TriggerSubject); subject != "" {
		return subject
	}
	prompt := strings.TrimSpace(job.Prompt)
	if line, _, found := strings.Cut(prompt, "\n"); found {
		return strings.TrimSpace(line)
	}
	return prompt
}

// reviewStandings reads, for every run that was sent for review, the pull
// request it was sent as and the verdict if one has come back. A run with an
// assignment and no verdict is waiting; that is the distinction the review lane
// is made of, so both tables are read rather than just the one that records
// finished reviews.
func (s *Store) reviewStandings(ctx context.Context) (map[string]reviewStanding, error) {
	standings := map[string]reviewStanding{}
	assignments, err := s.db.QueryContext(ctx,
		`SELECT reviewed_run_id,pull_request,reviewer_job_id FROM review_assignments`)
	if err != nil {
		return nil, fmt.Errorf("read review assignments: %w", err)
	}
	defer assignments.Close()
	for assignments.Next() {
		var runID, reviewer string
		var pullRequest int
		if err := assignments.Scan(&runID, &pullRequest, &reviewer); err != nil {
			return nil, fmt.Errorf("read review assignments: %w", err)
		}
		standings[runID] = reviewStanding{pullRequest: pullRequest, reviewer: reviewer}
	}
	if err := assignments.Err(); err != nil {
		return nil, fmt.Errorf("read review assignments: %w", err)
	}
	if err := assignments.Close(); err != nil {
		return nil, fmt.Errorf("read review assignments: %w", err)
	}
	verdicts, err := s.db.QueryContext(ctx,
		`SELECT run_id,pull_request,verdict,recorded_at FROM run_reviews ORDER BY recorded_at`)
	if err != nil {
		return nil, fmt.Errorf("read recorded reviews: %w", err)
	}
	defer verdicts.Close()
	for verdicts.Next() {
		var runID, verdict, recordedAt string
		var pullRequest int
		if err := verdicts.Scan(&runID, &pullRequest, &verdict, &recordedAt); err != nil {
			return nil, fmt.Errorf("read recorded reviews: %w", err)
		}
		// A verdict with no text is not a verdict. Treating one as an answer
		// would move the card out of the review lane while nobody has actually
		// said anything about it.
		if strings.TrimSpace(verdict) == "" {
			return nil, fmt.Errorf("read recorded reviews: run %s has a review with no verdict", runID)
		}
		standing := standings[runID]
		standing.verdict = strings.TrimSpace(verdict)
		if standing.pullRequest == 0 {
			standing.pullRequest = pullRequest
		}
		standings[runID] = standing
	}
	if err := verdicts.Err(); err != nil {
		return nil, fmt.Errorf("read recorded reviews: %w", err)
	}
	return standings, verdicts.Close()
}
