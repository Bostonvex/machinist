package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/telemetry"
	"github.com/owainlewis/machinist/internal/telemetry/provider"
)

// collectorSetup writes a Machinist config whose [collector] section is the
// body given, and returns its path.
func collectorSetup(t *testing.T, body string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "worker.token"), []byte(strings.Repeat("w", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nworker_token_file = \"worker.token\"\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A collector nobody enabled is refused rather than started on defaults. It
// would bind a port, create a credential, and begin keeping a record of every
// agent on the machine, none of which anyone asked for.
func TestCollectorStartRefusesWhenNotEnabled(t *testing.T) {
	path := collectorSetup(t, "[collector]\nenabled = false\n")
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"collector", "start", "--config", path}, strings.NewReader(""), &stdout, &stderr, "test")
	if code == 0 {
		t.Fatalf("collector start succeeded while disabled: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not enabled") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// The secrets are created on first start. The salt is created even though the
// collector never reads it: producers hash their identities with it before they
// send anything, so it has to exist before the first event does.
func TestCollectorStartCreatesItsSecretsAndServes(t *testing.T) {
	directory := t.TempDir()
	path := collectorSetup(t, `[collector]
enabled = true
listen = "127.0.0.1:0"
database = "`+filepath.Join(directory, "telemetry.db")+`"
token_file = "`+filepath.Join(directory, "ingest.token")+`"
identity_salt_file = "`+filepath.Join(directory, "identity.salt")+`"
`)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	base := startedCollector(t, loaded.Collector)

	for _, secret := range []string{"ingest.token", "identity.salt"} {
		info, err := os.Stat(filepath.Join(directory, secret))
		if err != nil {
			t.Fatalf("%s: %v", secret, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others: %s", secret, info.Mode())
		}
	}

	response, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer response.Body.Close()
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["status"] != "ok" {
		t.Fatalf("health = %#v", health)
	}
	// No providers were configured, so none are reported. An empty set would
	// say the providers are fine when there are none.
	if _, present := health["providers"]; present {
		t.Fatalf("health reported providers: %#v", health)
	}
}

// A provider that cannot be built stops the collector rather than being
// skipped. A poller silently absent is indistinguishable from hardware that is
// idle, and the gap only shows up as missing data much later.
func TestCollectorStartRefusesAnUnbuildableProvider(t *testing.T) {
	directory := t.TempDir()
	path := collectorSetup(t, `[collector]
enabled = true
listen = "127.0.0.1:0"
database = "`+filepath.Join(directory, "telemetry.db")+`"
token_file = "`+filepath.Join(directory, "ingest.token")+`"
identity_salt_file = "`+filepath.Join(directory, "identity.salt")+`"

[collector.vllm]
metrics_url = "http://127.0.0.1:18000/v1/models"
`)
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"collector", "start", "--config", path}, strings.NewReader(""), &stdout, &stderr, "test")
	if code == 0 {
		t.Fatal("the collector started with a provider it could not build")
	}
	if !strings.Contains(stderr.String(), "collector.vllm") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// startedCollector runs the collector in the background and returns its base
// URL once it is listening. It stops when the test does.
func startedCollector(t *testing.T, collectorConfig config.Collector) string {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	addresses := make(chan net.Addr, 1)
	done := make(chan error, 1)
	options := &commandOptions{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, version: "test"}
	go func() { done <- serveCollector(ctx, options, collectorConfig, func(a net.Addr) { addresses <- a }) }()
	select {
	case address := <-addresses:
		t.Cleanup(func() {
			stop()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("collector: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("the collector did not stop")
			}
		})
		return "http://" + address.String()
	case err := <-done:
		t.Fatalf("the collector stopped before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the collector never reported an address")
	}
	return ""
}

// Two remote nodes have to reach the supervisor as two providers it will
// accept. The supervisor refuses two providers under one name -- which is the
// same invariant the config enforces on node_id -- so a config that permits two
// nodes and a provider name that does not distinguish them would produce a
// collector that loads its configuration and then refuses to start.
func TestEveryConfiguredRemoteNodeBecomesItsOwnProvider(t *testing.T) {
	built, err := collectorProviders(config.Collector{
		NvidiaRemote: config.CollectorNvidiaNodes{
			{NodeID: "spark-0e9f", SSHHost: "spark-0e9f"},
			{NodeID: "spark-27c2", SSHHost: "spark-27c2"},
		},
	})
	if err != nil {
		t.Skipf("no ssh on this machine: %v", err)
	}
	if len(built) != 2 {
		t.Fatalf("built %d providers for two nodes", len(built))
	}
	names := []string{built[0].Name(), built[1].Name()}
	if names[0] == names[1] {
		t.Fatalf("both nodes report as %q", names[0])
	}
	for index, node := range []string{"spark-0e9f", "spark-27c2"} {
		if !strings.Contains(names[index], node) {
			t.Errorf("provider %d is named %q, which does not say which machine it reads", index, names[index])
		}
	}
	if _, err := provider.NewSupervisor(built, func(context.Context, []telemetry.Event) {}, time.Second, "test", nil); err != nil {
		t.Fatalf("a supervisor refused the providers for two configured nodes: %v", err)
	}
}

// The node is named in the error. With more than one remote node configured,
// "collector.nvidia_remote" alone sends an operator to read every one of them.
func TestAnUnbuildableRemoteNodeSaysWhichOne(t *testing.T) {
	_, err := collectorProviders(config.Collector{
		NvidiaRemote: config.CollectorNvidiaNodes{
			{NodeID: "spark-0e9f", SSHHost: "spark-0e9f"},
			{NodeID: "spark-27c2", SSHHost: "-oProxyCommand=touch /tmp/x"},
		},
	})
	if err == nil {
		t.Fatal("a destination that could become an option was accepted")
	}
	if !strings.Contains(err.Error(), "spark-27c2") {
		t.Fatalf("error = %v, want it to name the node that could not be built", err)
	}
}
