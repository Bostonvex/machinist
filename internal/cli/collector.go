package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
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
	return collector
}

func newCollectorStartCommand(options *commandOptions) *cobra.Command {
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the telemetry collector",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			machinistConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			collectorConfig := machinistConfig.Collector
			if !collectorConfig.Enabled {
				// Refused rather than started on defaults. A collector nobody
				// configured would bind a port, create a token, and begin
				// keeping a record of every agent on the machine, none of which
				// anyone asked for.
				return errors.New("the collector is not enabled: set collector.enabled in the Machinist config")
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
