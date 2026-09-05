package config

import (
	"os"
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(collector, Collector{}) {
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
	if len(collector.NvidiaRemote) != 1 || collector.NvidiaRemote[0].NodeID != "remote-nvidia" {
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
	if collector.Vllm != nil || collector.Nvidia != nil || len(collector.NvidiaRemote) != 0 {
		t.Fatalf("collector = %#v", collector)
	}
}

func TestAnUnknownCollectorFieldIsRefused(t *testing.T) {
	if _, err := collectorConfig(t, enabledCollector+"retention_days = 7\n"); err == nil {
		t.Fatal("an unknown collector field was accepted")
	}
}

// tomlPath writes a filesystem path into a TOML basic string. A Windows path
// carries backslashes, which TOML reads as escape sequences: C:\Users parses
// as an invalid \U, and the config is refused before the check under test is
// ever reached. Go accepts forward slashes on every platform it builds for.
func tomlPath(path string) string {
	return filepath.ToSlash(path)
}

func TestTelemetryIsRefusedInTheControlPlaneDatabase(t *testing.T) {
	// The two stores have opposite shapes. The collector's has its own
	// retention and deletes on a timer; pointed at the control plane's file
	// that sweep runs against the table of runs, and a backup of one is a
	// backup of the other taken at a moment neither chose.
	directory := t.TempDir()
	shared := tomlPath(filepath.Join(directory, "machinist.db"))
	writeTestFile(t, filepath.Join(directory, "worker.token"), strings.Repeat("t", 40)+"\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, "[server]\nworker_token_file = \"worker.token\"\ndatabase = \""+
		shared+"\"\n\n"+enabledCollector+"database = \""+shared+"\"\n")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("telemetry was accepted into the control plane's database")
	}
	if !strings.Contains(err.Error(), "same file") {
		t.Fatalf("the refusal does not say what is wrong: %v", err)
	}
}

func TestTheSharedDatabaseCheckSeesThroughASymlink(t *testing.T) {
	// Two names for one file is the case being caught. Comparing the strings
	// would miss exactly the configuration that looks most correct.
	directory := t.TempDir()
	real := filepath.Join(directory, "machinist.db")
	writeTestFile(t, real, "")
	alias := filepath.Join(directory, "telemetry.db")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	writeTestFile(t, filepath.Join(directory, "worker.token"), strings.Repeat("t", 40)+"\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, "[server]\nworker_token_file = \"worker.token\"\ndatabase = \""+
		tomlPath(real)+"\"\n\n"+enabledCollector+"database = \""+tomlPath(alias)+"\"\n")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("a symlink to the control plane's database was accepted")
	}
	// Any other refusal would pass this test without the alias ever being
	// resolved, which is the one thing it exists to check.
	if !strings.Contains(err.Error(), "same file") {
		t.Fatalf("the alias was refused for some other reason: %v", err)
	}
}

func TestSeparateDatabasesAreAccepted(t *testing.T) {
	// The check must not be a refusal of the ordinary case. On a fresh install
	// neither file exists yet, which is the path with nothing to resolve.
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "worker.token"), strings.Repeat("t", 40)+"\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, "[server]\nworker_token_file = \"worker.token\"\ndatabase = \""+
		tomlPath(filepath.Join(directory, "machinist.db"))+"\"\n\n"+enabledCollector+"database = \""+
		tomlPath(filepath.Join(directory, "telemetry.db"))+"\"\n")

	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("two separate databases were refused: %v", err)
	}
}

func TestADisabledCollectorDoesNotForceADatabaseChoice(t *testing.T) {
	// A disabled collector opens nothing. Refusing a path it will never use
	// would make the two settings harder to move than to have.
	directory := t.TempDir()
	shared := tomlPath(filepath.Join(directory, "machinist.db"))
	writeTestFile(t, filepath.Join(directory, "worker.token"), strings.Repeat("t", 40)+"\n")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, "[server]\nworker_token_file = \"worker.token\"\ndatabase = \""+
		shared+"\"\n\n[collector]\nenabled = false\ndatabase = \""+shared+"\"\n")

	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("a disabled collector was refused for a path it never opens: %v", err)
	}
}

// The deployment this was written for reaches two GB10 nodes. Before this, the
// table named one of them and the other was invisible -- which is the one state
// indistinguishable from idle.
func TestEveryRemoteNodeThatIsNamedIsPolled(t *testing.T) {
	collector, err := collectorConfig(t, enabledCollector+`
[[collector.nvidia_remote]]
node_id = "spark-0e9f"
ssh_host = "spark-0e9f"

[[collector.nvidia_remote]]
node_id = "spark-27c2"
ssh_host = "spark-27c2"
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(collector.NvidiaRemote) != 2 {
		t.Fatalf("nvidia_remote = %#v", collector.NvidiaRemote)
	}
	for index, want := range []CollectorNvidia{
		{NodeID: "spark-0e9f", SSHHost: "spark-0e9f"},
		{NodeID: "spark-27c2", SSHHost: "spark-27c2"},
	} {
		if collector.NvidiaRemote[index] != want {
			t.Errorf("node %d = %#v, want %#v", index, collector.NvidiaRemote[index], want)
		}
	}
}

// A deployment with one remote node should not have to be rewritten to stay
// where it is. Both spellings mean the same thing and neither is deprecated.
func TestOneRemoteNodeMayBeWrittenEitherWay(t *testing.T) {
	for name, body := range map[string]string{
		"single table":   "\n[collector.nvidia_remote]\nnode_id = \"spark\"\nssh_host = \"spark.local\"\n",
		"table array":    "\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"spark.local\"\n",
		"inline table":   "\nnvidia_remote = {node_id = \"spark\", ssh_host = \"spark.local\"}\n",
		"inline array":   "\nnvidia_remote = [{node_id = \"spark\", ssh_host = \"spark.local\"}]\n",
		"array with one": "\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"spark.local\"\n",
		// Padding is not part of a name. A node stored under " spark " is one
		// the operator would search the board for as "spark" and not find, and
		// nvidia-smi would refuse the destination at start rather than at load.
		"padded": "\n[[collector.nvidia_remote]]\nnode_id = \" spark \"\nssh_host = \"  spark.local\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			collector, err := collectorConfig(t, enabledCollector+body)
			if err != nil {
				t.Fatal(err)
			}
			want := []CollectorNvidia{{NodeID: "spark", SSHHost: "spark.local"}}
			if !reflect.DeepEqual([]CollectorNvidia(collector.NvidiaRemote), want) {
				t.Fatalf("nvidia_remote = %#v", collector.NvidiaRemote)
			}
		})
	}
}

// The invariant the old one-table-only rule was protecting. Two nodes under one
// name share a status row, and an operator reading a failure cannot tell which
// machine stopped answering.
func TestTwoGPUNodesMayNotShareAName(t *testing.T) {
	for name, body := range map[string]string{
		"two remote nodes": "\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"a\"\n\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"b\"\n",
		// The local table is in the same namespace: a sample carries a node_id
		// and nothing that says whether it was read here or over SSH.
		"remote taking the local name":    "\n[collector.nvidia]\nnode_id = \"spark\"\n\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"b\"\n",
		"remote taking the local default": "\n[collector.nvidia]\n\n[[collector.nvidia_remote]]\nnode_id = \"local-nvidia\"\nssh_host = \"b\"\n",
		// Two names that differ only in padding are one name to the person
		// reading the board, and the collision has to be found where it can
		// still be explained.
		"names differing only in padding":          "\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"a\"\n\n[[collector.nvidia_remote]]\nnode_id = \" spark \"\nssh_host = \"b\"\n",
		"the local name differing only in padding": "\n[collector.nvidia]\nnode_id = \" spark \"\n\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"b\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collectorConfig(t, enabledCollector+body)
			if err == nil {
				t.Fatal("two nodes were accepted under one name")
			}
			if !strings.Contains(err.Error(), "node_id") {
				t.Fatalf("error = %v, want it to name the field that collided", err)
			}
		})
	}
}

// "remote-nvidia" is a name for the remote node, which is only a name while
// there is one of them. Defaulting past that would hand two machines one
// identity on the operator's behalf, and the refusal above would then report a
// collision between two lines that say nothing about a node_id at all.
func TestMoreThanOneRemoteNodeMustBeNamedExplicitly(t *testing.T) {
	_, err := collectorConfig(t, enabledCollector+`
[[collector.nvidia_remote]]
ssh_host = "a"

[[collector.nvidia_remote]]
node_id = "spark-27c2"
ssh_host = "b"
`)
	if err == nil {
		t.Fatal("an unnamed node was accepted alongside another")
	}
	if !strings.Contains(err.Error(), "node_id is required") {
		t.Fatalf("error = %v, want it to ask for the name", err)
	}
}

// Every entry is checked, not just the first. A host missing from the second
// node is a node that is configured, never polled, and reported as nothing.
func TestEveryRemoteNodeNeedsItsOwnHost(t *testing.T) {
	_, err := collectorConfig(t, enabledCollector+`
[[collector.nvidia_remote]]
node_id = "spark-0e9f"
ssh_host = "spark-0e9f"

[[collector.nvidia_remote]]
node_id = "spark-27c2"
`)
	if err == nil {
		t.Fatal("a remote node with no host was accepted")
	}
	if !strings.Contains(err.Error(), "ssh_host") {
		t.Fatalf("error = %v", err)
	}
}

// The outer decoder's strictness does not reach inside a type that unmarshals
// itself, so the type re-applies it. Every other line of this config file is
// refused when it names a field nothing reads, and a node is not the place for
// that to stop being true: an ssh_port nobody honours is a connection an
// operator believes they configured.
//
// The node is otherwise complete. A key written *instead of* ssh_host would be
// refused anyway by the requirement that a remote node name a host, which is a
// different check -- and a check only ever reached through another check can be
// deleted without a test noticing.
func TestAFieldARemoteNodeDoesNotHaveIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"table array":  "\n[[collector.nvidia_remote]]\nnode_id = \"spark\"\nssh_host = \"spark.local\"\nssh_port = 2222\n",
		"single table": "\n[collector.nvidia_remote]\nnode_id = \"spark\"\nssh_host = \"spark.local\"\nssh_port = 2222\n",
		"inline table": "\nnvidia_remote = {node_id = \"spark\", ssh_host = \"spark.local\", ssh_port = 2222}\n",
		"inline array": "\nnvidia_remote = [{node_id = \"spark\", ssh_host = \"spark.local\", ssh_port = 2222}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := collectorConfig(t, enabledCollector+body)
			if err == nil {
				t.Fatal("a field the node does not have was accepted")
			}
			if !strings.Contains(err.Error(), "strict") {
				t.Fatalf("error = %v, want it to refuse the field rather than something else", err)
			}
		})
	}
}
