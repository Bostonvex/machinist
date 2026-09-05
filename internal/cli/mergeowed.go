package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/spf13/cobra"
)

// machinist merge-owed is the question "what did we finish and not land". It
// replaces factory-merge-owed.sh, which asked it by parsing issue comments.
//
// It reports and never merges. That is not a limitation of the port: merge is a
// human act, and a command that both found the work and landed it would be a
// merge bot wearing a report's name.

type mergeOwedChange struct {
	Repository   string   `json:"repository"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	Head         string   `json:"head"`
	Disposition  string   `json:"disposition"`
	Reason       string   `json:"reason"`
	OpenFindings []string `json:"open_findings"`
	OwedSeconds  float64  `json:"owed_seconds"`
}

type mergeOwedView struct {
	Repository string            `json:"repository"`
	Changes    []mergeOwedChange `json:"changes"`
	ReadAt     time.Time         `json:"read_at"`
}

// The dispositions, in the order they are printed. A change carrying a
// disposition that is not one of these is printed under its own heading rather
// than dropped: this build not recognising an answer is not the same as there
// being nothing to report, and the shell losing work is why that distinction is
// worth the four lines it costs.
const (
	dispositionMerge     = "merge-owed"
	dispositionAttention = "attention-owed"
	dispositionWaiting   = "waiting"
)

// exitMergeOwed is the exit code when something is owed a merge, carried over
// from the shell so the cron entries that call it keep working: 0 nothing owed,
// 1 something owed, anything else a failure to answer.
const exitMergeOwed = 1

func newMergeOwedCommand(options *commandOptions) *cobra.Command {
	var repository string
	var asJSON, quiet bool
	command := &cobra.Command{
		Use:   "merge-owed",
		Short: "Show which reviewed work is waiting to be merged",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return showMergeOwed(command.Context(), options, repository, asJSON, quiet)
		},
	}
	command.Flags().StringVar(&repository, "repository", "", "the repository to look at (required)")
	command.Flags().BoolVar(&asJSON, "json", false, "print the answer as JSON")
	command.Flags().BoolVar(&quiet, "quiet", false, "print nothing; say it in the exit code")
	_ = command.MarkFlagRequired("repository")
	return command
}

func showMergeOwed(ctx context.Context, options *commandOptions, repository string, asJSON, quiet bool) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	client, err := managedworker.NewClient(worker)
	if err != nil {
		return err
	}
	var view mergeOwedView
	if err := client.Get(ctx, "/api/v1/merge-owed?repository="+strings.TrimSpace(repository), &view); err != nil {
		// The error is returned rather than an empty answer printed. "Nothing
		// is owed" and "I could not find out" are the two answers this command
		// exists to keep apart, and a rate-limited read that printed the first
		// one would be the shell's worst failure ported forward.
		return fmt.Errorf("read what is owed a merge: %w", err)
	}
	// An answer that does not say what it is about or when it was taken is not
	// an answer. Printed, it is indistinguishable from a repository with
	// nothing outstanding, which is the one thing this command must never say
	// by accident.
	if view.ReadAt.IsZero() || strings.TrimSpace(view.Repository) == "" {
		return fmt.Errorf("the control plane answered about %q without saying which repository it read or when",
			strings.TrimSpace(repository))
	}
	if asJSON {
		encoder := json.NewEncoder(options.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(view); err != nil {
			return err
		}
		return mergeOwedExit(view)
	}
	if !quiet {
		if err := printMergeOwed(options, view); err != nil {
			return err
		}
	}
	return mergeOwedExit(view)
}

// mergeOwedExit turns the answer into an exit code without swallowing it. A
// cron entry reads the code; a person reads the table above it.
func mergeOwedExit(view mergeOwedView) error {
	for _, change := range view.Changes {
		if change.Disposition == dispositionMerge {
			return &exitCodeError{code: exitMergeOwed}
		}
	}
	return nil
}

func printMergeOwed(options *commandOptions, view mergeOwedView) error {
	grouped := map[string][]mergeOwedChange{}
	var extra []string
	for _, change := range view.Changes {
		disposition := change.Disposition
		switch disposition {
		case dispositionMerge, dispositionAttention, dispositionWaiting:
		default:
			if _, seen := grouped[disposition]; !seen {
				extra = append(extra, disposition)
			}
		}
		grouped[disposition] = append(grouped[disposition], change)
	}
	order := append([]string{dispositionMerge, dispositionAttention, dispositionWaiting}, extra...)
	headings := map[string]string{
		dispositionMerge:     "OWED A MERGE",
		dispositionAttention: "OWED ATTENTION",
		dispositionWaiting:   "NOTHING OWED YET",
	}
	fmt.Fprintf(options.stdout, "%s, read at %s\n", view.Repository, view.ReadAt.Local().Format(time.RFC3339))
	for _, disposition := range order {
		heading, named := headings[disposition]
		if !named {
			heading = strings.ToUpper(disposition)
		}
		changes := grouped[disposition]
		// Every group prints, including the empty ones. A section that
		// disappears when it empties makes an operator scanning for "nothing
		// needs attention" read an absence instead of a zero, and an absence is
		// what a broken query also looks like.
		fmt.Fprintf(options.stdout, "\n%s (%d)\n", heading, len(changes))
		if len(changes) == 0 {
			continue
		}
		table := tabwriter.NewWriter(options.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "  PR\tSTANDING FOR\tTITLE\tWHY")
		for _, change := range changes {
			fmt.Fprintf(table, "  #%d\t%s\t%s\t%s\n",
				change.Number, mergeOwedAge(change), elide(change.Title, 44), change.Reason)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// mergeOwedAge prints how long a change has stood in its current standing, and
// prints a dash rather than "0s" when there is no verdict to measure from. Zero
// would read as "it just happened", which is the opposite of "nobody has
// looked at it yet".
func mergeOwedAge(change mergeOwedChange) string {
	if change.OwedSeconds <= 0 {
		return "-"
	}
	owed := time.Duration(change.OwedSeconds) * time.Second
	switch {
	case owed < time.Hour:
		return fmt.Sprintf("%dm", int(owed.Minutes()))
	case owed < 48*time.Hour:
		return fmt.Sprintf("%dh", int(owed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(owed.Hours()/24))
	}
}
