package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/spf13/cobra"
)

// machinist claim is how an agent says it is working on a GitHub issue, and how
// it hands the issue back. It asks the control plane, which holds the claim as
// a row, rather than writing a comment on the issue and hoping every other
// reader replays the thread the same way.

type claimView struct {
	Repository string    `json:"repository"`
	Issue      int       `json:"issue"`
	State      string    `json:"state"`
	Holder     string    `json:"holder"`
	Branch     string    `json:"branch"`
	Reason     string    `json:"reason"`
	Transfer   string    `json:"transfer"`
	ClaimedAt  time.Time `json:"claimed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Live       bool      `json:"live"`
}

type claimListing struct {
	Claims []claimView `json:"claims"`
}

type claimWrite struct {
	Action     string `json:"action"`
	Repository string `json:"repository"`
	Issue      int    `json:"issue"`
	Holder     string `json:"holder"`
	Branch     string `json:"branch,omitempty"`
	Reason     string `json:"reason"`
	Transfer   string `json:"transfer,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func newClaimCommand(options *commandOptions) *cobra.Command {
	claim := &cobra.Command{
		Use:   "claim",
		Short: "Say who is working on a GitHub issue",
		Args:  cobra.NoArgs,
	}
	claim.AddCommand(newClaimListCommand(options))
	claim.AddCommand(newClaimTakeCommand(options))
	claim.AddCommand(newClaimReleaseCommand(options))
	claim.AddCommand(newClaimHoldCommand(options))
	return claim
}

func newClaimListCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show every issue claim and whether it is still live",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return listClaims(command.Context(), options)
		},
	}
}

func newClaimTakeCommand(options *commandOptions) *cobra.Command {
	var issue, holder, branch, reason, forDuration string
	take := &cobra.Command{
		Use:   "take",
		Short: "Claim an issue for a holder until the claim expires",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeClaim(command.Context(), options, claimArguments{
				action: "take", issue: issue, holder: holder,
				branch: branch, reason: reason, forDuration: forDuration,
			})
		},
	}
	addClaimFlags(take, &issue, &holder, &reason)
	take.Flags().StringVar(&branch, "branch", "", "the branch the work will land on")
	addClaimWindowFlag(take, &forDuration)
	return take
}

func newClaimReleaseCommand(options *commandOptions) *cobra.Command {
	var issue, holder, reason string
	release := &cobra.Command{
		Use:   "release",
		Short: "Give an issue back so anyone can take it",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeClaim(command.Context(), options, claimArguments{
				action: "release", issue: issue, holder: holder, reason: reason,
			})
		},
	}
	addClaimFlags(release, &issue, &holder, &reason)
	return release
}

func newClaimHoldCommand(options *commandOptions) *cobra.Command {
	var issue, holder, reason, transfer, forDuration string
	hold := &cobra.Command{
		Use:   "hold",
		Short: "Stop work on an issue without making it free work yet",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeClaim(command.Context(), options, claimArguments{
				action: "hold", issue: issue, holder: holder,
				reason: reason, transfer: transfer, forDuration: forDuration,
			})
		},
	}
	addClaimFlags(hold, &issue, &holder, &reason)
	hold.Flags().StringVar(&transfer, "transfer", "", "where the work went, as owner/repo#n")
	addClaimWindowFlag(hold, &forDuration)
	return hold
}

func addClaimFlags(command *cobra.Command, issue, holder, reason *string) {
	command.Flags().StringVar(issue, "issue", "", "the issue, as owner/repo#n (required)")
	command.Flags().StringVar(holder, "holder", "", "the seat doing the work (required)")
	command.Flags().StringVar(reason, "reason", "", "why, in a sentence the next agent will read (required)")
	_ = command.MarkFlagRequired("issue")
	_ = command.MarkFlagRequired("holder")
	_ = command.MarkFlagRequired("reason")
}

// addClaimWindowFlag adds the required --for. There is no open-ended claim: one
// that never lapses becomes a lock the moment the holder stops answering, and
// the agent that took it is usually not the one who finds it still standing.
func addClaimWindowFlag(command *cobra.Command, forDuration *string) {
	command.Flags().StringVar(forDuration, "for", "", "how long this holds, as a Go duration such as 4h (required)")
	_ = command.MarkFlagRequired("for")
}

type claimArguments struct {
	action      string
	issue       string
	holder      string
	branch      string
	reason      string
	transfer    string
	forDuration string
}

// parseIssue splits owner/repo#n. It refuses anything else rather than doing
// its best with it: a claim written against a repository nobody meant is worse
// than a claim that was not written.
func parseIssue(reference string) (string, int, error) {
	repository, number, found := strings.Cut(strings.TrimSpace(reference), "#")
	if !found {
		return "", 0, fmt.Errorf("--issue %q must be owner/repo#n", reference)
	}
	repository = strings.TrimSpace(repository)
	if !strings.Contains(repository, "/") {
		return "", 0, fmt.Errorf("--issue %q must name a repository as owner/repo", reference)
	}
	issue, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil {
		return "", 0, fmt.Errorf("--issue %q must end in an issue number: %w", reference, err)
	}
	if issue <= 0 {
		return "", 0, fmt.Errorf("--issue %q must name a positive issue number", reference)
	}
	return repository, issue, nil
}

func claimClient(options *commandOptions) (*managedworker.Client, error) {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return nil, err
	}
	return managedworker.NewClient(worker)
}

func listClaims(ctx context.Context, options *commandOptions) error {
	client, err := claimClient(options)
	if err != nil {
		return err
	}
	var listing claimListing
	if err := client.Get(ctx, "/api/v1/claims", &listing); err != nil {
		return fmt.Errorf("read issue claims: %w", err)
	}
	if len(listing.Claims) == 0 {
		fmt.Fprintln(options.stdout, "no issue claims")
		return nil
	}
	table := tabwriter.NewWriter(options.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ISSUE\tLIVE\tSTATE\tHOLDER\tEXPIRES\tREASON")
	for _, claim := range listing.Claims {
		live := "no"
		if claim.Live {
			live = "yes"
		}
		fmt.Fprintf(table, "%s#%d\t%s\t%s\t%s\t%s\t%s\n",
			claim.Repository, claim.Issue, live, claim.State, claim.Holder,
			claim.ExpiresAt.Local().Format(time.RFC3339), claim.Reason)
	}
	return table.Flush()
}

func writeClaim(ctx context.Context, options *commandOptions, arguments claimArguments) error {
	repository, issue, err := parseIssue(arguments.issue)
	if err != nil {
		return err
	}
	request := claimWrite{
		Action: arguments.action, Repository: repository, Issue: issue,
		Holder: arguments.holder, Branch: arguments.branch,
		Reason: arguments.reason, Transfer: arguments.transfer,
	}
	if arguments.forDuration != "" {
		window, err := time.ParseDuration(strings.TrimSpace(arguments.forDuration))
		if err != nil {
			return fmt.Errorf("--for must be a duration such as 4h: %w", err)
		}
		if window <= 0 {
			return errors.New("--for must be positive: a claim that has already expired claims nothing")
		}
		request.ExpiresAt = time.Now().Add(window).UTC().Format(time.RFC3339)
	}
	client, err := claimClient(options)
	if err != nil {
		return err
	}
	var written claimView
	if err := client.Post(ctx, "/api/v1/claims", request, &written); err != nil {
		return fmt.Errorf("%s issue claim: %w", arguments.action, err)
	}
	// Report what the control plane stored, not what was sent. Those are the
	// same thing only when the write did what it was asked to, and a claim is
	// exactly the case where believing otherwise means two agents on one issue.
	if arguments.action == "release" {
		fmt.Fprintf(options.stdout, "%s#%d: released by %s (%s)\n",
			repository, issue, arguments.holder, arguments.reason)
		return nil
	}
	verb := "claimed by"
	if written.State == "on-hold" {
		verb = "held, not free work, last held by"
	}
	fmt.Fprintf(options.stdout, "%s#%d: %s %s until %s (%s)\n",
		written.Repository, written.Issue, verb, written.Holder,
		written.ExpiresAt.Local().Format(time.RFC3339), written.Reason)
	return nil
}
