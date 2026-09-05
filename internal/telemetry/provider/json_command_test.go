package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// hardwareDocument is a valid json_command output document for a hardware
// scope: one allowlisted sample, with the hardware identity the provider adds
// applied afterwards.
const hardwareDocument = `{
  "schema_version": 1,
  "samples": [
    {"metric_name": "gpu_utilization", "value": 52.5, "unit": "percent", "measurement_quality": "exact"}
  ]
}`

func TestAValidJSONDocumentBecomesSamples(t *testing.T) {
	samples, err := parseJSONCommand([]byte(hardwareDocument), ScopeHardware,
		map[string]bool{"gpu_utilization": true}, "nvidia-smi", "dgx-spark", "")
	if err != nil {
		t.Fatalf("a valid document was refused: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(samples))
	}
	sample := samples[0]
	if err := sample.Valid(); err != nil {
		t.Fatalf("the parser produced a sample the validator refuses: %v", err)
	}
	if sample.MetricName != "gpu_utilization" || sample.Value != 52.5 {
		t.Fatalf("the sample was misread: %+v", sample)
	}
	if sample.ProviderID != "nvidia-smi" || sample.NodeID != "dgx-spark" {
		t.Fatalf("the sample lost its hardware identity: %+v", sample)
	}
}

func TestAServerDocumentCarriesItsEndpoint(t *testing.T) {
	document := `{
	  "schema_version": 1,
	  "samples": [
	    {"metric_name": "requests_running", "value": 7, "unit": "requests", "measurement_quality": "exact"}
	  ]
	}`
	samples, err := parseJSONCommand([]byte(document), ScopeServer,
		map[string]bool{"requests_running": true}, "", "", "llama-api")
	if err != nil {
		t.Fatalf("a valid server document was refused: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(samples))
	}
	if samples[0].EndpointID != "llama-api" || samples[0].ProviderID != "" || samples[0].NodeID != "" {
		t.Fatalf("the server sample lost or gained identity: %+v", samples[0])
	}
}

func TestADocumentWithABadSampleFailsTheWholePoll(t *testing.T) {
	// Output that does not parse is a failed poll, not a partial one: a parser
	// that quietly dropped the bad reading would leave an operator believing a
	// metric was fine when the command that produced it was wrong.
	cases := map[string]string{
		"not json output":          "this is not a document at all",
		"wrong fields":             `{"schema_version": 1, "samples": [], "extra": true}`,
		"wrong schema version":     `{"schema_version": 2, "samples": []}`,
		"metric outside allowlist": `{"schema_version": 1, "samples": [{"metric_name": "other_metric", "value": 1, "unit": "percent", "measurement_quality": "exact"}]}`,
		"negative value":           `{"schema_version": 1, "samples": [{"metric_name": "gpu_utilization", "value": -1, "unit": "percent", "measurement_quality": "exact"}]}`,
		"non-numeric value":        `{"schema_version": 1, "samples": [{"metric_name": "gpu_utilization", "value": "hot", "unit": "percent", "measurement_quality": "exact"}]}`,
		"missing unit":             `{"schema_version": 1, "samples": [{"metric_name": "gpu_utilization", "value": 1, "measurement_quality": "exact"}]}`,
		"unknown sample field":     `{"schema_version": 1, "samples": [{"metric_name": "gpu_utilization", "value": 1, "unit": "percent", "measurement_quality": "exact", "extra": true}]}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseJSONCommand([]byte(document), ScopeHardware,
				map[string]bool{"gpu_utilization": true}, "nvidia-smi", "dgx-spark", ""); err == nil {
				t.Fatalf("document %q was accepted", document)
			}
		})
	}
}

func TestTooManySamplesFailsThePoll(t *testing.T) {
	// The sample cap bounds a command before its output reaches the store,
	// which is the difference between one failed poll and a flood of columns.
	count := maximumJSONSamples + 1
	items := make([]string, count)
	for index := range items {
		items[index] = `{"metric_name": "gpu_utilization", "value": 1, "unit": "percent", "measurement_quality": "exact"}`
	}
	document := `{"schema_version": 1, "samples": [` + strings.Join(items, ",") + `]}`
	if _, err := parseJSONCommand([]byte(document), ScopeHardware,
		map[string]bool{"gpu_utilization": true}, "nvidia-smi", "dgx-spark", ""); err == nil {
		t.Fatal("a document with too many samples was accepted")
	}
}

func TestAValidDocumentThroughPoll(t *testing.T) {
	provider, err := NewJsonCommand(ScopeHardware,
		[]string{"/usr/local/bin/gpu-read"}, []string{"gpu_utilization"},
		"nvidia-smi", "dgx-spark", "", time.Second, stubRunner(hardwareDocument, nil))
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	samples, err := provider.Poll(context.Background())
	if err != nil {
		t.Fatalf("a healthy poll failed: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample, got %d", len(samples))
	}
	for _, sample := range samples {
		if err := sample.Valid(); err != nil {
			t.Fatalf("the provider emitted an invalid sample: %v", err)
		}
	}
}

func TestATimeoutIsAFailedPoll(t *testing.T) {
	// A command that hangs costs one poll, and the failure is the command's,
	// never a partial set of whatever it printed before stalling.
	provider, err := NewJsonCommand(ScopeHardware,
		[]string{"/usr/local/bin/gpu-read"}, []string{"gpu_utilization"},
		"nvidia-smi", "dgx-spark", "", 300*time.Millisecond,
		func(context.Context, []string, time.Duration, int) ([]byte, error) {
			return nil, errors.New("provider command failed: timed out")
		})
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	if _, err := provider.Poll(context.Background()); !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a timed-out command was not reported as such: %v", err)
	}
}

func TestOversizedOutputIsAFailedPoll(t *testing.T) {
	// Output beyond the bound is refused rather than truncated, because a
	// truncated stream would hand the parser half a sample.
	provider, err := NewJsonCommand(ScopeHardware,
		[]string{"/usr/local/bin/gpu-read"}, []string{"gpu_utilization"},
		"nvidia-smi", "dgx-spark", "", time.Second,
		func(context.Context, []string, time.Duration, int) ([]byte, error) {
			return nil, errors.New("provider command failed: output exceeded 1048576 bytes")
		})
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	if _, err := provider.Poll(context.Background()); !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversized output was not reported as such: %v", err)
	}
}

func TestANonAbsoluteExecutableIsRefused(t *testing.T) {
	// A relative name resolved repeatedly could pick up a different binary
	// halfway through the collector's life. The executable is pinned once, when
	// the provider is built, and that pin is the contract.
	if _, err := NewJsonCommand(ScopeHardware,
		[]string{"gpu-read"}, []string{"gpu_utilization"},
		"nvidia-smi", "dgx-spark", "", time.Second, stubRunner("", nil)); err == nil {
		t.Fatal("a relative executable was accepted")
	}
}

func TestTwoJSONProvidersCannotShareAName(t *testing.T) {
	// json-command is a distinct name from the other providers, so the
	// supervisor's uniqueness check still separates one failing reader from
	// another.
	first, err := NewJsonCommand(ScopeHardware,
		[]string{"/usr/local/bin/gpu-read-1"}, []string{"gpu_utilization"},
		"nvidia-smi", "node-a", "", time.Second, stubRunner("", nil))
	if err != nil {
		t.Fatalf("could not build the first provider: %v", err)
	}
	second, err := NewJsonCommand(ScopeHardware,
		[]string{"/usr/local/bin/gpu-read-2"}, []string{"memory_used"},
		"nvidia-smi", "node-b", "", time.Second, stubRunner("", nil))
	if err != nil {
		t.Fatalf("could not build the second provider: %v", err)
	}
	if first.Name() != "json-command" || second.Name() != "json-command" {
		t.Fatalf("json providers swapped or lost their name: %q %q", first.Name(), second.Name())
	}
	if _, err := NewSupervisor([]Provider{first, second}, func(context.Context, []telemetry.Event) {}, time.Second, "1.2.3", quiet()); err == nil {
		t.Fatal("two json-command providers under one name were accepted")
	}
}

func TestAConfigFileBuildsAProvider(t *testing.T) {
	config := `{
	  "schema_version": 1,
	  "scope": "hardware",
	  "provider_id": "nvidia-smi",
	  "node_id": "dgx-spark",
	  "endpoint_id": null,
	  "argv": ["/usr/local/bin/gpu-read", "--gpu", "0"],
	  "allowed_metrics": ["gpu_utilization"],
	  "timeout_seconds": 5
	}`
	path := filepath.Join(t.TempDir(), "gpu.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("could not write the config: %v", err)
	}
	provider, err := NewJsonCommandFromFile(path, stubRunner(hardwareDocument, nil))
	if err != nil {
		t.Fatalf("a good config file was refused: %v", err)
	}
	samples, err := provider.Poll(context.Background())
	if err != nil {
		t.Fatalf("a provider from a file failed to poll: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected one sample from the file-based provider, got %d", len(samples))
	}
}

func TestAnOversizedConfigFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maximumConfigBytes+1)), 0o600); err != nil {
		t.Fatalf("could not write the config: %v", err)
	}
	if _, err := NewJsonCommandFromFile(path, nil); err == nil {
		t.Fatal("an oversized config file was accepted")
	}
}

func TestAMissingConfigFileIsRefused(t *testing.T) {
	if _, err := NewJsonCommandFromFile(filepath.Join(t.TempDir(), "absent.json"), nil); err == nil {
		t.Fatal("a missing config file was accepted")
	}
}
