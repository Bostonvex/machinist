package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/spf13/cobra"
)

// machinist board is the view an operator opens to find out where work is
// stuck. It asks the control plane rather than reading the database, so it says
// the same thing as the dashboard and can be run from a machine that is not the
// one holding the database.

type boardCard struct {
	JobID        string    `json:"job_id"`
	Repository   string    `json:"repository"`
	Title        string    `json:"title"`
	Command      string    `json:"command"`
	State        string    `json:"state"`
	Lane         string    `json:"lane"`
	Recognised   bool      `json:"recognised"`
	Worker       string    `json:"worker"`
	Attempt      int       `json:"attempt"`
	MaxAttempts  int       `json:"max_attempts"`
	ErrorClass   string    `json:"error_class"`
	Error        string    `json:"error"`
	PullRequest  int       `json:"pull_request"`
	Verdict      string    `json:"verdict"`
	AwaitingFrom string    `json:"awaiting_from"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type boardColumn struct {
	Lane  string      `json:"lane"`
	Cards []boardCard `json:"cards"`
}

type boardView struct {
	Columns     []boardColumn `json:"columns"`
	GeneratedAt time.Time     `json:"generated_at"`
}

func newBoardCommand(options *commandOptions) *cobra.Command {
	var asJSON bool
	var lane string
	board := &cobra.Command{
		Use:   "board",
		Short: "Show every job in the lane it is currently in",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return showBoard(command.Context(), options, lane, asJSON)
		},
	}
	board.Flags().StringVar(&lane, "lane", "", "show only this lane")
	board.Flags().BoolVar(&asJSON, "json", false, "print the board as JSON")
	return board
}

func showBoard(ctx context.Context, options *commandOptions, lane string, asJSON bool) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	client, err := managedworker.NewClient(worker)
	if err != nil {
		return err
	}
	var view boardView
	if err := client.Get(ctx, "/api/v1/board", &view); err != nil {
		// The error is returned rather than an empty board printed. An operator
		// who asked where work is stuck and was shown nothing would conclude
		// there is no work, which is not what happened here.
		return fmt.Errorf("read board: %w", err)
	}
	if asJSON {
		encoder := json.NewEncoder(options.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	return printBoard(options, view, strings.TrimSpace(lane))
}

func printBoard(options *commandOptions, view boardView, only string) error {
	if only != "" {
		// A filter that matches no lane is refused rather than answered with an
		// empty board, because those two look identical and mean opposite
		// things: one is a typo, the other is good news.
		names := make([]string, 0, len(view.Columns))
		found := false
		for _, column := range view.Columns {
			names = append(names, column.Lane)
			if column.Lane == only {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("no lane named %q: the board has %s", only, strings.Join(names, ", "))
		}
	}
	empty := true
	for _, column := range view.Columns {
		if only != "" && column.Lane != only {
			continue
		}
		if len(column.Cards) > 0 {
			empty = false
		}
	}
	if empty {
		fmt.Fprintln(options.stdout, "no work on the board")
		return nil
	}
	for _, column := range view.Columns {
		if only != "" && column.Lane != only {
			continue
		}
		// Empty lanes are printed with their count rather than skipped. A lane
		// that vanishes when it empties makes the board change shape, and an
		// operator scanning for "nothing queued" has to notice an absence
		// instead of reading a zero.
		fmt.Fprintf(options.stdout, "\n%s (%d)\n", strings.ToUpper(column.Lane), len(column.Cards))
		if len(column.Cards) == 0 {
			continue
		}
		table := tabwriter.NewWriter(options.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "  JOB\tREPOSITORY\tTITLE\tDETAIL")
		for _, card := range column.Cards {
			fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n",
				shortJobID(card.JobID), card.Repository, elide(card.Title, 48), cardDetail(card))
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// cardDetail is the one thing worth saying about a card beyond its title: why
// it is where it is. Which fact that is depends on the lane, so the card is
// asked rather than every lane being given its own column that is blank for
// most rows.
func cardDetail(card boardCard) string {
	if !card.Recognised {
		// This is the whole reason LaneOther exists, so it is said first and
		// said plainly rather than left for the operator to deduce.
		return fmt.Sprintf("unrecognised state %q", card.State)
	}
	switch {
	case card.Lane == string(controlplane.LaneParked):
		// A parked card would otherwise read like a running one -- a worker
		// name and nothing else -- when it is the one lane where nothing at all
		// will happen until a person acts.
		return "stopped, waiting on a person"
	case card.AwaitingFrom != "":
		return fmt.Sprintf("awaiting review of #%d", card.PullRequest)
	case card.Verdict != "":
		return fmt.Sprintf("#%d %s", card.PullRequest, card.Verdict)
	case card.Error != "":
		if card.ErrorClass != "" {
			return fmt.Sprintf("%s: %s", card.ErrorClass, elide(card.Error, 60))
		}
		return elide(card.Error, 60)
	case card.Worker != "":
		if card.MaxAttempts > 1 {
			return fmt.Sprintf("%s, attempt %d of %d", card.Worker, card.Attempt, card.MaxAttempts)
		}
		return card.Worker
	default:
		return card.State
	}
}

// shortJobID keeps enough of an identifier to pick a job out of a listing and
// to paste into another command. The full one is in --json for anything that
// needs to match exactly.
func shortJobID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func elide(text string, width int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len([]rune(text)) <= width {
		return text
	}
	return string([]rune(text)[:width-1]) + "…"
}
