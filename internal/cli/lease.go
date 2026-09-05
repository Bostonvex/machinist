package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/spf13/cobra"
)

// The lease verbs are how an operator says which machines may run agents right
// now. Standing a fleet down is the one thing they need to be able to do from
// a machine that is misbehaving, so it goes over the same control-plane API as
// everything else rather than through anything the fleet itself has to obey.

type leaseView struct {
	Fleet     string    `json:"fleet"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Allowed   bool      `json:"allowed"`
}

type leaseListing struct {
	Leases   []leaseView `json:"leases"`
	Required bool        `json:"required"`
}

type leaseWrite struct {
	Fleet     string `json:"fleet"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
	Reason    string `json:"reason"`
}

func newLeaseCommand(options *commandOptions) *cobra.Command {
	lease := &cobra.Command{
		Use:   "lease",
		Short: "Decide which fleets may take new work",
		Args:  cobra.NoArgs,
	}
	lease.AddCommand(newLeaseListCommand(options))
	lease.AddCommand(newLeaseAllowCommand(options))
	lease.AddCommand(newLeaseStandDownCommand(options))
	return lease
}

func newLeaseListCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show every fleet lease and whether it currently allows work",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return listLeases(command.Context(), options)
		},
	}
}

func newLeaseAllowCommand(options *commandOptions) *cobra.Command {
	var fleet, reason, forDuration string
	allow := &cobra.Command{
		Use:   "allow",
		Short: "Let a fleet take work until the lease expires",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeLease(command.Context(), options, "allowed", fleet, reason, forDuration)
		},
	}
	addLeaseFlags(allow, &fleet, &reason, &forDuration)
	return allow
}

func newLeaseStandDownCommand(options *commandOptions) *cobra.Command {
	var fleet, reason, forDuration string
	standDown := &cobra.Command{
		Use:   "stand-down",
		Short: "Stop a fleet taking new work",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeLease(command.Context(), options, "stood-down", fleet, reason, forDuration)
		},
	}
	addLeaseFlags(standDown, &fleet, &reason, &forDuration)
	return standDown
}

func addLeaseFlags(command *cobra.Command, fleet, reason, forDuration *string) {
	command.Flags().StringVar(fleet, "fleet", "", "fleet this decision applies to (required)")
	command.Flags().StringVar(reason, "reason", "", "why, in a sentence the next operator will read (required)")
	// There is no open-ended lease. A permission that never lapses outlives the
	// situation it was granted for, and the person who granted it is usually
	// not the person who finds it still in force.
	command.Flags().StringVar(forDuration, "for", "", "how long this holds, as a Go duration such as 8h (required)")
	_ = command.MarkFlagRequired("fleet")
	_ = command.MarkFlagRequired("reason")
	_ = command.MarkFlagRequired("for")
}

func leaseClient(options *commandOptions) (*managedworker.Client, error) {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return nil, err
	}
	return managedworker.NewClient(worker)
}

func listLeases(ctx context.Context, options *commandOptions) error {
	client, err := leaseClient(options)
	if err != nil {
		return err
	}
	var listing leaseListing
	if err := client.Get(ctx, "/api/v1/leases", &listing); err != nil {
		return fmt.Errorf("read fleet leases: %w", err)
	}
	if !listing.Required {
		// Without this line a full listing of allowed fleets reads as a fleet
		// under control, when in fact nothing is being enforced at all.
		fmt.Fprintln(options.stdout, "fleet leasing is off: these leases are recorded but no worker is held to them")
	}
	if len(listing.Leases) == 0 {
		fmt.Fprintln(options.stdout, "no fleet leases")
		return nil
	}
	table := tabwriter.NewWriter(options.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "FLEET\tTAKING WORK\tSTATE\tEXPIRES\tREASON")
	for _, lease := range listing.Leases {
		taking := "no"
		if lease.Allowed {
			taking = "yes"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			lease.Fleet, taking, lease.State, lease.ExpiresAt.Local().Format(time.RFC3339), lease.Reason)
	}
	return table.Flush()
}

func writeLease(ctx context.Context, options *commandOptions, state, fleet, reason, forDuration string) error {
	window, err := time.ParseDuration(strings.TrimSpace(forDuration))
	if err != nil {
		return fmt.Errorf("--for must be a duration such as 8h: %w", err)
	}
	if window <= 0 {
		return errors.New("--for must be positive: a lease that has already expired says nothing about what happens next")
	}
	client, err := leaseClient(options)
	if err != nil {
		return err
	}
	request := leaseWrite{
		Fleet: fleet, State: state,
		ExpiresAt: time.Now().Add(window).UTC().Format(time.RFC3339),
		Reason:    reason,
	}
	var written leaseView
	if err := client.Post(ctx, "/api/v1/leases", request, &written); err != nil {
		return fmt.Errorf("set fleet lease: %w", err)
	}
	// Report the lease the control plane stored, not the one that was sent: the
	// operator needs to know what is now in force, and those are the same thing
	// only when the write did what it was asked to.
	verb := "stood down"
	if written.Allowed {
		verb = "taking work"
	}
	fmt.Fprintf(options.stdout, "%s: %s until %s (%s)\n",
		written.Fleet, verb, written.ExpiresAt.Local().Format(time.RFC3339), written.Reason)
	return nil
}
