package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/telemetry"
	"github.com/owainlewis/machinist/internal/telemetry/proxy"
	"github.com/spf13/cobra"
)

func newProxyCommand(options *commandOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "proxy",
		Short: "Run the Machinist model proxy",
	}
	command.AddCommand(newProxyStartCommand(options))
	return command
}

func newProxyStartCommand(options *commandOptions) *cobra.Command {
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the model proxy in front of one endpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			if collectorConfig.Proxy == nil {
				return errors.New("the model proxy is not configured: add a [collector.proxy] table naming upstream, model and endpoint_id")
			}
			proxyConfig := *collectorConfig.Proxy
			if options.listen != "" {
				proxyConfig.Listen = options.listen
			}
			return serveProxy(command.Context(), options, collectorConfig, proxyConfig, nil)
		},
	}
	start.Flags().StringVar(&options.listen, "listen", "", "loopback listen address (default 127.0.0.1:7901)")
	return start
}

// serveProxy runs the proxy until the context is done.
//
// The proxy is a separate process from the collector on purpose. It sits in
// the model call path, so a collector restart must not interrupt a generation,
// and a collector that is down must not stop agents from working. Everything
// here follows from that: the sink drops rather than blocks, and the proxy
// serves whether or not anything is listening on the other end.
func serveProxy(ctx context.Context, options *commandOptions, collectorConfig config.Collector, proxyConfig config.CollectorProxy, onListening func(net.Addr)) error {
	// Both processes read the same two files, and whichever starts first
	// creates them. That is what lets the proxy come up before the collector
	// ever has: a proxy that refused to start without the collector's token
	// would have made the model call path depend on the observability of it.
	token, err := telemetry.LoadOrCreateToken(collectorConfig.TokenFile)
	if err != nil {
		return err
	}
	contextToken, err := telemetry.LoadOrCreateToken(proxyConfig.ContextTokenFile)
	if err != nil {
		return err
	}

	settings, err := proxy.Validate(proxy.Config{
		Listen:       proxyConfig.Listen,
		Upstream:     proxyConfig.Upstream,
		Model:        proxyConfig.Model,
		EndpointID:   proxyConfig.EndpointID,
		ContextToken: contextToken,
	})
	if err != nil {
		return err
	}

	sink, err := proxy.NewCollector(proxy.CollectorConfig{
		URL:   "http://" + collectorConfig.Listen + telemetry.IngestPath,
		Token: token,
	})
	if err != nil {
		return err
	}
	defer func() {
		// The queue is drained on the way out, so the events describing
		// whatever caused the shutdown are not the ones that get lost.
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(flush)
		if stats := sink.Stats(); stats.Dropped > 0 || stats.Failed > 0 {
			fmt.Fprintf(options.stderr,
				"machinist: model proxy delivered %d events, dropped %d, failed %d\n",
				stats.Sent, stats.Dropped, stats.Failed)
		}
	}()

	server := proxy.New(settings, sink)
	return server.Serve(ctx, func(address net.Addr) {
		fmt.Fprintf(options.stderr,
			"machinist: model proxy listening on http://%s, forwarding to %s\n",
			address, settings.Upstream())
		fmt.Fprintf(options.stderr,
			"machinist: point the harness at http://%s and declare turns at %s\n",
			address, proxy.ContextPath)
		if onListening != nil {
			onListening(address)
		}
	})
}
