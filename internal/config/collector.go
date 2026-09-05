package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultCollectorDirName holds the telemetry database. It is deliberately not
// the server directory: telemetry is a high-volume append-only record with its
// own retention, and putting it beside the transactional control-plane database
// invites the two being backed up, copied and truncated as one thing.
const defaultCollectorDirName = ".machinist/collector"

// Bounds on collector configuration. Each exists because the value is a knob an
// operator sets once and forgets, and a wrong one is only noticed as missing
// data weeks later.
const (
	minimumRetention        = time.Hour
	maximumRetention        = 3650 * 24 * time.Hour
	minimumProviderInterval = time.Second
	maximumProviderInterval = time.Hour
)

// Collector configures the telemetry collector Machinist serves.
//
// It is separate from [observability], which says where the *control plane*
// reads a collector from. One is this process offering the endpoint; the other
// is this process consuming it, and a deployment can do either without the
// other.
type Collector struct {
	Enabled          bool             `toml:"enabled"`
	Listen           string           `toml:"listen"`
	Database         string           `toml:"database"`
	TokenFile        string           `toml:"token_file"`
	IdentitySaltFile string           `toml:"identity_salt_file"`
	Retention        string           `toml:"retention"`
	ProviderInterval string           `toml:"provider_interval"`
	Vllm             *CollectorVllm   `toml:"vllm"`
	Nvidia           *CollectorNvidia `toml:"nvidia"`
	NvidiaRemote     *CollectorNvidia `toml:"nvidia_remote"`

	retention        time.Duration
	providerInterval time.Duration
	configDir        string
}

// CollectorVllm polls one vLLM server's Prometheus endpoint.
type CollectorVllm struct {
	MetricsURL string `toml:"metrics_url"`
	EndpointID string `toml:"endpoint_id"`
}

// CollectorNvidia polls nvidia-smi for the GPUs of one node. SSHHost is empty
// for this machine; the [collector.nvidia_remote] table is the one that names
// another.
type CollectorNvidia struct {
	NodeID  string `toml:"node_id"`
	SSHHost string `toml:"ssh_host"`
}

// RetentionWindow is how long raw events are kept, resolved from Retention.
func (c Collector) RetentionWindow() time.Duration { return c.retention }

// PollInterval is how often each configured provider is polled, resolved from
// ProviderInterval.
func (c Collector) PollInterval() time.Duration { return c.providerInterval }

func applyCollectorDefaults(collector Collector) (Collector, error) {
	if !collector.Enabled {
		// A disabled collector is not half-validated. Reporting a bad listen
		// address for a collector that will never bind sends an operator to fix
		// a line that has no effect.
		return Collector{}, nil
	}
	if strings.TrimSpace(collector.Listen) == "" {
		collector.Listen = "127.0.0.1:7900"
	}
	if err := validateLoopbackOrigin(collector.Listen); err != nil {
		return Collector{}, err
	}
	if strings.TrimSpace(collector.Database) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Collector{}, fmt.Errorf("find user home directory: %w", err)
		}
		collector.Database = filepath.Join(home, filepath.FromSlash(defaultCollectorDirName), "telemetry.db")
	} else {
		path, err := resolveConfigPath(collector.Database, collector.configDir)
		if err != nil {
			return Collector{}, fmt.Errorf("resolve collector database: %w", err)
		}
		collector.Database = path
	}
	// Both secrets are required rather than defaulted to a path under the
	// database. A token this process would create silently is a credential
	// nobody knows exists, and the producers that must present it are
	// configured somewhere else entirely.
	for _, secret := range []struct {
		field string
		value *string
	}{
		{"token_file", &collector.TokenFile},
		{"identity_salt_file", &collector.IdentitySaltFile},
	} {
		if strings.TrimSpace(*secret.value) == "" {
			return Collector{}, fmt.Errorf("collector.%s is required", secret.field)
		}
		path, err := resolveConfigPath(*secret.value, collector.configDir)
		if err != nil {
			return Collector{}, fmt.Errorf("resolve collector %s: %w", secret.field, err)
		}
		*secret.value = path
	}

	var err error
	if collector.retention, err = collectorDuration("retention", collector.Retention, 7*24*time.Hour, minimumRetention, maximumRetention); err != nil {
		return Collector{}, err
	}
	if collector.providerInterval, err = collectorDuration("provider_interval", collector.ProviderInterval, 10*time.Second, minimumProviderInterval, maximumProviderInterval); err != nil {
		return Collector{}, err
	}
	if collector.Vllm != nil && strings.TrimSpace(collector.Vllm.EndpointID) == "" {
		collector.Vllm.EndpointID = "vllm-primary"
	}
	if collector.Nvidia != nil && strings.TrimSpace(collector.Nvidia.NodeID) == "" {
		collector.Nvidia.NodeID = "local-nvidia"
	}
	if collector.NvidiaRemote != nil {
		if strings.TrimSpace(collector.NvidiaRemote.NodeID) == "" {
			collector.NvidiaRemote.NodeID = "remote-nvidia"
		}
		if strings.TrimSpace(collector.NvidiaRemote.SSHHost) == "" {
			return Collector{}, errors.New("collector.nvidia_remote.ssh_host is required: the local GPUs are [collector.nvidia]")
		}
	}
	if collector.Nvidia != nil && strings.TrimSpace(collector.Nvidia.SSHHost) != "" {
		return Collector{}, errors.New("collector.nvidia polls this machine: name another host under [collector.nvidia_remote]")
	}
	return collector, nil
}

func collectorDuration(field, input string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	if strings.TrimSpace(input) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(input)
	if err != nil {
		return 0, fmt.Errorf("collector.%s: %w", field, err)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("collector.%s must be between %s and %s", field, minimum, maximum)
	}
	return value, nil
}

// validateLoopbackOrigin refuses a listen address that is not literal loopback.
// The collector is a live description of what every agent on this machine is
// doing; binding it to a network is a deliberate act, not a typo in a config
// file. Reaching it from elsewhere is an SSH tunnel.
func validateLoopbackOrigin(listen string) error {
	parsed, err := url.Parse("http://" + listen)
	if err != nil || parsed.Port() == "" {
		return errors.New("collector.listen must be HOST:PORT")
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "localhost" {
		return errors.New("collector.listen must be a literal loopback host")
	}
	return nil
}
