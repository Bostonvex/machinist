package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func stubRunner(output string, err error) Runner {
	return func(context.Context, []string, time.Duration, int) ([]byte, error) {
		return []byte(output), err
	}
}

func capturingRunner(argv *[]string) Runner {
	return func(_ context.Context, seen []string, _ time.Duration, _ int) ([]byte, error) {
		*argv = append([]string(nil), seen...)
		return []byte("0, 41, 1024, 81920, 52, 210.5\n"), nil
	}
}

func TestEveryColumnOfARowBecomesASample(t *testing.T) {
	samples, err := parseNvidiaCSV("0, 41, 1024, 81920, 52, 210.5\n1, 0, 12, 81920, 40, 60\n", "dgx-spark")
	if err != nil {
		t.Fatalf("well-formed output was refused: %v", err)
	}
	if len(samples) != 10 {
		t.Fatalf("two GPUs and five metrics should give ten samples, got %d", len(samples))
	}
	for _, sample := range samples {
		if err := sample.Valid(); err != nil {
			t.Fatalf("the parser produced a sample the validator refuses: %v", err)
		}
		if sample.NodeID != "dgx-spark" || sample.ProviderID != "nvidia-smi" {
			t.Fatalf("a sample lost its identity: %+v", sample)
		}
	}
	if samples[0].MetricName != "gpu.0.utilization_percent" || samples[0].Value != 41 {
		t.Fatalf("the first column was misread: %+v", samples[0])
	}
	if samples[5].MetricName != "gpu.1.utilization_percent" {
		t.Fatalf("the second GPU was misnamed: %+v", samples[5])
	}
}

func TestARowOfADifferentWidthIsRefused(t *testing.T) {
	// The width is the only evidence that this output is the query that was
	// asked for. Pairing columns positionally against a different query would
	// file a temperature under a utilisation name and look entirely plausible.
	for name, output := range map[string]string{
		"too few":         "0, 41, 1024, 81920, 52\n",
		"too many":        "0, 41, 1024, 81920, 52, 210.5, 99\n",
		"another program": "Wed Sep  5 12:00:00 2026\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseNvidiaCSV(output, "dgx-spark"); !errors.Is(err, ErrCommand) {
				t.Fatalf("output of the wrong shape was accepted: %v", err)
			}
		})
	}
}

func TestACardThatCannotMeasureReportsNothingRatherThanZero(t *testing.T) {
	// "[N/A]" is the card saying it has no sensor for this. Recording zero
	// would put a flat line on a power chart and it would look like a reading.
	samples, err := parseNvidiaCSV("0, 41, 1024, 81920, 52, [N/A]\n", "dgx-spark")
	if err != nil {
		t.Fatalf("an unsupported field should not fail the row: %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("expected the four measured metrics, got %d", len(samples))
	}
	for _, sample := range samples {
		if strings.HasSuffix(sample.MetricName, "power_watts") {
			t.Fatal("an unmeasured field was recorded as zero")
		}
	}
}

func TestAnUnreadableFieldFailsTheRead(t *testing.T) {
	if _, err := parseNvidiaCSV("0, 41, 1024, 81920, 52, hot\n", "dgx-spark"); !errors.Is(err, ErrCommand) {
		t.Fatalf("a non-numeric reading was accepted: %v", err)
	}
	if _, err := parseNvidiaCSV("0, -41, 1024, 81920, 52, 210\n", "dgx-spark"); !errors.Is(err, ErrCommand) {
		t.Fatalf("a negative reading was accepted: %v", err)
	}
}

func TestARemoteReadNeverWaitsAtAPrompt(t *testing.T) {
	// This poller runs unattended every few seconds. Without BatchMode it can
	// sit at a passphrase prompt, and without StrictHostKeyChecking it is the
	// thing that accepts an unknown host key on the operator's behalf.
	var argv []string
	provider, err := NewNvidia("dgx-spark", "spark.local", 5*time.Second, capturingRunner(&argv))
	if err != nil {
		t.Skipf("no ssh on this machine: %v", err)
	}
	if _, err := provider.Poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, required := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "ConnectTimeout=5"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("the remote command was missing %s: %v", required, argv)
		}
	}
	// Without "--", a destination beginning with a dash is read by ssh as an
	// option rather than rejected as a bad host.
	if !strings.Contains(joined, " -- spark.local ") {
		t.Fatalf("option parsing was not ended before the destination: %v", argv)
	}
	if provider.Name() != "nvidia-smi-remote" {
		t.Fatalf("a remote read reported itself as local: %q", provider.Name())
	}
}

func TestADestinationThatCouldBecomeAnOptionIsRefused(t *testing.T) {
	for _, host := range []string{"-oProxyCommand=touch /tmp/x", "spark.local; touch /tmp/x", "spark local", strings.Repeat("a", 300)} {
		if _, err := NewNvidia("dgx-spark", host, time.Second, stubRunner("", nil)); err == nil {
			t.Fatalf("host %q was accepted as an SSH destination", host)
		}
	}
}

func TestANodeIdentifierIsBounded(t *testing.T) {
	// The node id ends up in a metric's identity, in the database and on a
	// dashboard, and it comes from configuration.
	if _, err := NewNvidia("dgx spark", "", time.Second, stubRunner("", nil)); err == nil {
		t.Fatal("an unsafe node id was accepted")
	}
}

func TestAFailingCommandIsTheProvidersFailure(t *testing.T) {
	provider, err := NewNvidia("dgx-spark", "spark.local", time.Second, stubRunner("", ErrCommand))
	if err != nil {
		t.Skipf("no ssh on this machine: %v", err)
	}
	if _, err := provider.Poll(context.Background()); !errors.Is(err, ErrCommand) {
		t.Fatalf("a failing command was not reported: %v", err)
	}
}
