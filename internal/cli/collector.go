package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/telemetry"
	"github.com/owainlewis/machinist/internal/telemetry/provider"
	"github.com/spf13/cobra"
)

func newCollectorCommand(options *commandOptions) *cobra.Command {
	collector := &cobra.Command{
		Use:   "collector",
		Short: "Run the Machinist telemetry collector",
	}
	collector.AddCommand(newCollectorStartCommand(options))
	collector.AddCommand(newCollectorDoctorCommand(options))
	collector.AddCommand(newCollectorBackupCommand(options))
	collector.AddCommand(newCollectorPurgeCommand(options))
	collector.AddCommand(newCollectorDemoCommand(options))
	return collector
}

func newCollectorStartCommand(options *commandOptions) *cobra.Command {
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the telemetry collector",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			if options.listen != "" {
				collectorConfig.Listen = options.listen
			}
			return serveCollector(command.Context(), options, collectorConfig, func(address net.Addr) {
				fmt.Fprintf(options.stderr, "machinist: telemetry collector listening on http://%s\n", address)
			})
		},
	}
	start.Flags().StringVar(&options.listen, "listen", "", "loopback listen address (default 127.0.0.1:7900)")
	return start
}

func serveCollector(ctx context.Context, options *commandOptions, collectorConfig config.Collector, onListening func(net.Addr)) error {
	// Both secrets are created on first start if they are absent, and read
	// otherwise. The salt is loaded even though this process never reads it:
	// producers hash their identities with it before they send anything, so it
	// has to exist before the first event does, or the identities in the record
	// change the day it appears.
	token, err := telemetry.LoadOrCreateToken(collectorConfig.TokenFile)
	if err != nil {
		return err
	}
	if _, err := telemetry.LoadOrCreateIdentitySalt(collectorConfig.IdentitySaltFile); err != nil {
		return err
	}
	store, err := telemetry.OpenStore(collectorConfig.Database)
	if err != nil {
		return err
	}
	defer store.Close()

	logger := log.New(options.stderr, "", log.LstdFlags)
	server, err := telemetry.NewServer(store, token, collectorConfig.RetentionWindow(), logger)
	if err != nil {
		return err
	}

	providers, err := collectorProviders(collectorConfig)
	if err != nil {
		return err
	}
	var wait sync.WaitGroup
	if len(providers) > 0 {
		supervisor, err := provider.NewSupervisor(providers, server.Ingest, collectorConfig.PollInterval(), options.version, logger)
		if err != nil {
			return err
		}
		server.SetProviderDiagnostics(func() any { return supervisor.Diagnostics() })
		wait.Add(1)
		go func() {
			defer wait.Done()
			supervisor.Run(ctx)
		}()
	}
	// The supervisor is waited for after the HTTP server returns, so a poll in
	// flight finishes writing before the store is closed.
	defer wait.Wait()

	return server.Serve(ctx, collectorConfig.Listen, onListening)
}

// collectorProviders builds the configured infrastructure providers.
//
// A provider that cannot be built stops the collector rather than being
// skipped. A poller silently absent is indistinguishable from one whose
// hardware is idle, and the gap only shows up as missing data long after the
// configuration that caused it was written.
func collectorProviders(collectorConfig config.Collector) ([]provider.Provider, error) {
	var providers []provider.Provider
	if vllm := collectorConfig.Vllm; vllm != nil {
		polled, err := provider.NewVllm(vllm.MetricsURL, vllm.EndpointID, providerTimeout)
		if err != nil {
			return nil, fmt.Errorf("collector.vllm: %w", err)
		}
		providers = append(providers, polled)
	}
	if nvidia := collectorConfig.Nvidia; nvidia != nil {
		polled, err := provider.NewNvidia(nvidia.NodeID, "", providerTimeout, nil)
		if err != nil {
			return nil, fmt.Errorf("collector.nvidia: %w", err)
		}
		providers = append(providers, polled)
	}
	if nvidia := collectorConfig.NvidiaRemote; nvidia != nil {
		polled, err := provider.NewNvidia(nvidia.NodeID, nvidia.SSHHost, providerTimeout, nil)
		if err != nil {
			return nil, fmt.Errorf("collector.nvidia_remote: %w", err)
		}
		providers = append(providers, polled)
	}
	return providers, nil
}

// providerTimeout bounds one poll. It is well under the shortest interval a
// provider can be configured for, so a hung endpoint costs one reading rather
// than stacking up behind the next.
const providerTimeout = 3 * time.Second

// enabledCollectorConfig loads the [collector] section and refuses a deployment
// that never enabled one.
//
// Refused rather than defaulted. A collector nobody configured would bind a
// port, create a credential, and begin keeping a record of every agent on the
// machine, none of which anyone asked for. Every verb goes through this, so
// none of them can be pointed at a database no configured collector owns.
func enabledCollectorConfig(options *commandOptions) (config.Collector, error) {
	machinistConfig, err := config.LoadConfig(options.configPath)
	if err != nil {
		return config.Collector{}, err
	}
	if !machinistConfig.Collector.Enabled {
		return config.Collector{}, errors.New("the collector is not enabled: set collector.enabled in the Machinist config")
	}
	return machinistConfig.Collector, nil
}

// openExistingStore opens the telemetry database and creates nothing.
//
// backup and purge each name a record that already exists. Opening the way the
// collector does would create an empty database on a typo in the path, and then
// backup would write an archive of nothing and report that it succeeded.
func openExistingStore(path string) (*telemetry.Store, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("the telemetry database does not exist: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read the telemetry database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the telemetry database is not a regular file: %s", path)
	}
	return telemetry.OpenStore(path)
}

// collectorCheck is one thing doctor looked at and what it found.
type collectorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// collectorReport is doctor's whole answer. Healthy is derived from the checks
// rather than decided alongside them, so a check cannot fail while the report
// says the collector is fit to run.
type collectorReport struct {
	Listen   string           `json:"listen"`
	Database string           `json:"database"`
	Healthy  bool             `json:"healthy"`
	Checks   []collectorCheck `json:"checks"`
}

// errCollectorUnfit is returned after the report has been printed, so the exit
// status carries the same answer the report does. A diagnostic that always
// exits 0 is one no script can act on.
var errCollectorUnfit = errors.New("the collector is not fit to run: see the report above")

func newCollectorDoctorCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report whether the configured collector is fit to run",
		Long: "Inspect what starting the collector would depend on: its two secret files, its\n" +
			"database, and one poll from every configured provider. Nothing is created and\n" +
			"nothing is repaired — an absent file is the finding, not a step to take.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			report := diagnoseCollector(command.Context(), collectorConfig)
			encoded, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(options.stdout, string(encoded))
			if !report.Healthy {
				return errCollectorUnfit
			}
			return nil
		},
	}
}

// diagnoseCollector runs every check and reports all of them.
//
// It does not stop at the first failure. An operator fixing a collector wants
// the list of what is wrong, not one item of it per run; a doctor that returned
// early would make repairing three problems take three invocations.
func diagnoseCollector(ctx context.Context, collectorConfig config.Collector) collectorReport {
	report := collectorReport{
		Listen:   collectorConfig.Listen,
		Database: collectorConfig.Database,
		Healthy:  true,
	}
	record := func(name, detail string, err error) {
		if err != nil {
			report.Healthy = false
			report.Checks = append(report.Checks, collectorCheck{name, "failed", err.Error()})
			return
		}
		report.Checks = append(report.Checks, collectorCheck{name, "ok", detail})
	}

	_, err := telemetry.ReadToken(collectorConfig.TokenFile)
	record("token_file", collectorConfig.TokenFile, err)
	_, err = telemetry.ReadIdentitySalt(collectorConfig.IdentitySaltFile)
	record("identity_salt_file", collectorConfig.IdentitySaltFile, err)

	if health, err := collectorHealth(ctx, collectorConfig.Database); err != nil {
		record("database", "", err)
	} else {
		record("database", fmt.Sprintf("schema version %v, journal mode %v, %v event(s), %v agent(s)",
			health["schema_version"], health["journal_mode"], health["events"], health["agents"]), nil)
	}

	providers, err := collectorProviders(collectorConfig)
	if err != nil {
		// One failure for the whole set, because construction is what failed:
		// there are no providers to report on individually.
		record("providers", "", err)
		return report
	}
	for _, polled := range providers {
		// Each poll is bounded the same way the supervisor bounds one, so
		// doctor reports what the collector would experience rather than
		// waiting on an endpoint the collector would have given up on.
		polling, cancel := context.WithTimeout(ctx, providerTimeout)
		samples, err := polled.Poll(polling)
		cancel()
		record("provider:"+polled.Name(), fmt.Sprintf("%d sample(s)", len(samples)), err)
	}
	return report
}

func collectorHealth(ctx context.Context, path string) (map[string]any, error) {
	store, err := openExistingStore(path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.Health(ctx)
}

func newCollectorBackupCommand(options *commandOptions) *cobra.Command {
	var output string
	backup := &cobra.Command{
		Use:          "backup",
		Short:        "Write a consistent copy of the telemetry database",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(output) == "" {
				return errors.New("--output is required: a backup names the file it writes")
			}
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			store, err := openExistingStore(collectorConfig.Database)
			if err != nil {
				return err
			}
			defer store.Close()
			written, err := store.BackupTo(command.Context(), output)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "machinist: telemetry backup written to %s\n", written)
			return nil
		},
	}
	backup.Flags().StringVar(&output, "output", "", "file to write the backup to")
	return backup
}

func newCollectorPurgeCommand(options *commandOptions) *cobra.Command {
	var before string
	var confirmed bool
	purge := &cobra.Command{
		Use:   "purge",
		Short: "Delete raw telemetry events observed before a timestamp",
		Long: "Delete raw events and infrastructure samples observed before a cutoff. Agents\n" +
			"and turns are kept: they are small, they are what a reader actually asks\n" +
			"about, and deleting them would erase that an agent ever ran rather than the\n" +
			"detail of what it did.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			// The confirmation is a flag rather than a prompt because this runs
			// from launchd and cron at least as often as from a terminal, and a
			// prompt there is not a question but a command that hangs.
			if !confirmed {
				return errors.New("purge deletes raw events permanently: pass --confirm-delete-raw-events")
			}
			cutoff, err := parsePurgeCutoff(before)
			if err != nil {
				return err
			}
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			store, err := openExistingStore(collectorConfig.Database)
			if err != nil {
				return err
			}
			defer store.Close()
			removed, err := store.Purge(command.Context(), cutoff)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "machinist: deleted %d raw telemetry event(s) observed before %s\n",
				removed, cutoff.UTC().Format(time.RFC3339))
			return nil
		},
	}
	purge.Flags().StringVar(&before, "before", "", "RFC 3339 cutoff; events observed before it are deleted")
	purge.Flags().BoolVar(&confirmed, "confirm-delete-raw-events", false, "acknowledge that this deletes raw events permanently")
	return purge
}

// parsePurgeCutoff refuses a timestamp that carries no zone.
//
// Every observed_at is UTC. Reading a zoneless cutoff as local time would, on a
// machine east of Greenwich, delete hours of events that have not expired — and
// nothing would show it afterwards, because what is gone is what is missing.
func parsePurgeCutoff(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("--before is required: a purge names the moment it deletes up to")
	}
	cutoff, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.New("--before must be an RFC 3339 timestamp with a timezone, such as 2026-01-31T00:00:00Z")
	}
	return cutoff, nil
}
