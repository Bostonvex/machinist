package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// collectorConfig writes a config file whose [collector] section is the body
// given, and loads it. Every collector test needs a valid [server] section it
// does not care about, so it is supplied here once.
func collectorConfig(t *testing.T, body string) (Collector, error) {
	t.Helper()
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "worker.token"), strings.Repeat("t", 40)+"\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, "[server]\nworker_token_file = \"worker.token\"\n\n"+body)
	loaded, err := LoadConfig(path)
	return loaded.Collector, err
}

// enabledCollector is the smallest collector a deployment can configure: the
// two secrets, and nothing else.
const enabledCollector = `[collector]
enabled = true
token_file = "ingest.token"
identity_salt_file = "identity.salt"
`

func TestCollectorDefaultsAreLoopbackAndBounded(t *testing.T) {
	collector, err := collectorConfig(t, enabledCollector)
	if err != nil {
		t.Fatal(err)
	}
	if collector.Listen != "127.0.0.1:7900" {
		t.Errorf("listen = %q", collector.Listen)
	}
	if collector.RetentionWindow() != 7*24*time.Hour {
		t.Errorf("retention = %s", collector.RetentionWindow())
	}
	if collector.PollInterval() != 10*time.Second {
		t.Errorf("provider interval = %s", collector.PollInterval())
	}
	// The secrets resolve against the config file's directory, as every other
	// path in this file does.
	if !filepath.IsAbs(collector.TokenFile) || !filepath.IsAbs(collector.IdentitySaltFile) {
		t.Errorf("token %q, salt %q", collector.TokenFile, collector.IdentitySaltFile)
	}
	// The telemetry database is not the control plane's. One is an append-only
	// record with its own retention; the other is transactional state.
	if !strings.HasSuffix(collector.Database, filepath.Join(".machinist", "collector", "telemetry.db")) {
		t.Errorf("database = %q", collector.Database)
	}
}

// A collector nobody enabled is not half-validated. Reporting a bad address for
// something that will never bind sends an operator to fix a line with no
// effect.
func TestADisabledCollectorIsNotValidated(t *testing.T) {
	collector, err := collectorConfig(t, `[collector]
listen = "0.0.0.0:7900"
retention = "9000h"
`)
	if err != nil {
		t.Fatal(err)
	}
	if collector != (Collector{}) {
		t.Fatalf("collector = %#v", collector)
	}
}

func TestTheCollectorRefusesToBindOffLoopback(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7900", "192.168.1.4:7900", "[::]:7900", "127.0.0.1", ""} {
		body := enabledCollector
		if listen != "" {
			body += "listen = \"" + listen + "\"\n"
		} else {
			continue
		}
		if _, err := collectorConfig(t, body); err == nil {
			t.Errorf("listen %q was accepted", listen)
		}
	}
}

// Neither secret is defaulted. A token this process invented would be a
// credential nobody knows exists, and the producers that must present it are
// configured somewhere else entirely.
func TestTheCollectorSecretsAreRequired(t *testing.T) {
	for _, body := range []string{
		"[collector]\nenabled = true\nidentity_salt_file = \"identity.salt\"\n",
		"[collector]\nenabled = true\ntoken_file = \"ingest.token\"\n",
	} {
		_, err := collectorConfig(t, body)
		if err == nil {
			t.Fatalf("accepted %q", body)
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("error = %v", err)
		}
	}
}

func TestCollectorDurationsAreBounded(t *testing.T) {
	for _, field := range []struct{ name, tooSmall, tooLarge string }{
		{"retention", "30m", "90000h"},
		{"provider_interval", "500ms", "2h"},
	} {
		for _, value := range []string{field.tooSmall, field.tooLarge, "not-a-duration"} {
			if _, err := collectorConfig(t, enabledCollector+field.name+" = \""+value+"\"\n"); err == nil {
				t.Errorf("%s = %q was accepted", field.name, value)
			}
		}
	}
}

// The two nvidia tables are not interchangeable. Which machine's GPUs a reading
// describes is the whole content of the reading, so the table that names a host
// must name one and the table that does not must not.
func TestTheTwoNvidiaTablesSayWhoseGPUsTheyAre(t *testing.T) {
	if _, err := collectorConfig(t, enabledCollector+"\n[collector.nvidia]\nssh_host = \"spark\"\n"); err == nil {
		t.Error("[collector.nvidia] accepted a remote host")
	}
	if _, err := collectorConfig(t, enabledCollector+"\n[collector.nvidia_remote]\nnode_id = \"spark\"\n"); err == nil {
		t.Error("[collector.nvidia_remote] was accepted without a host")
	}
	collector, err := collectorConfig(t, enabledCollector+`
[collector.nvidia]

[collector.nvidia_remote]
ssh_host = "spark"

[collector.vllm]
metrics_url = "http://127.0.0.1:18000/metrics"
`)
	if err != nil {
		t.Fatal(err)
	}
	if collector.Nvidia == nil || collector.Nvidia.NodeID != "local-nvidia" {
		t.Errorf("nvidia = %#v", collector.Nvidia)
	}
	if collector.NvidiaRemote == nil || collector.NvidiaRemote.NodeID != "remote-nvidia" {
		t.Errorf("nvidia_remote = %#v", collector.NvidiaRemote)
	}
	if collector.Vllm == nil || collector.Vllm.EndpointID != "vllm-primary" {
		t.Errorf("vllm = %#v", collector.Vllm)
	}
}

// A provider that is not configured is absent, not present and idle. The
// difference decides whether the collector starts a poller at all.
func TestUnconfiguredProvidersAreAbsent(t *testing.T) {
	collector, err := collectorConfig(t, enabledCollector)
	if err != nil {
		t.Fatal(err)
	}
	if collector.Vllm != nil || collector.Nvidia != nil || collector.NvidiaRemote != nil {
		t.Fatalf("collector = %#v", collector)
	}
}

func TestAnUnknownCollectorFieldIsRefused(t *testing.T) {
	if _, err := collectorConfig(t, enabledCollector+"retention_days = 7\n"); err == nil {
		t.Fatal("an unknown collector field was accepted")
	}
}
