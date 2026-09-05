package provider

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"testing"
)

// uuidFor gives a test a stable identifier that the event validator accepts.
// The validator refuses anything that is not a UUID, and a test that used
// "event-1" would fail for a reason that has nothing to do with what it checks.
func uuidFor(name string) string {
	digest := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

func hardware(edits func(*Sample)) Sample {
	sample := Sample{
		Scope: ScopeHardware, MetricName: "gpu.0.utilization_percent",
		Value: 41, Unit: "percent", ProviderID: "nvidia-smi", NodeID: "dgx-spark",
	}
	if edits != nil {
		edits(&sample)
	}
	return sample
}

func TestAServerSampleCannotClaimAHardwareIdentity(t *testing.T) {
	// The two scopes carry different identity, and a sample holding both would
	// be filed under a node it never measured as well as an endpoint.
	sample := Sample{
		Scope: ScopeServer, MetricName: "requests_running", Value: 3,
		Unit: "requests", EndpointID: "dgx-spark-vllm", NodeID: "dgx-spark",
	}
	err := sample.Valid()
	if err == nil {
		t.Fatal("a server sample carrying a node was accepted")
	}
	if !strings.Contains(err.Error(), "hardware identity") {
		t.Fatalf("the refusal did not name the problem: %v", err)
	}
}

func TestAHardwareSampleCannotClaimAnEndpoint(t *testing.T) {
	if err := hardware(func(s *Sample) { s.EndpointID = "dgx-spark-vllm" }).Valid(); err == nil {
		t.Fatal("a hardware sample carrying an endpoint was accepted")
	}
}

func TestAnUnreadableReadingIsNotASample(t *testing.T) {
	// Each of these is a parse that went wrong rather than a machine worth
	// recording, and storing any of them would put a value in a chart that no
	// instrument produced.
	for name, value := range map[string]float64{
		"not a number": math.NaN(),
		"infinite":     math.Inf(1),
		"negative":     -1,
		"absurd":       maximumValue * 10,
	} {
		t.Run(name, func(t *testing.T) {
			if err := hardware(func(s *Sample) { s.Value = value }).Valid(); err == nil {
				t.Fatalf("value %v was accepted", value)
			}
		})
	}
}

func TestAMetricNameFromOutsideIsBounded(t *testing.T) {
	// Metric names arrive from command output and HTTP responses and end up in
	// a database, in JSON and on a dashboard.
	for name, metric := range map[string]string{
		"empty":               "",
		"a space":             "gpu 0 utilization",
		"a newline":           "gpu.0\nutilization",
		"a quote":             `gpu."0"`,
		"leading punctuation": ".gpu.0",
		"far too long":        strings.Repeat("a", 300),
	} {
		t.Run(name, func(t *testing.T) {
			if err := hardware(func(s *Sample) { s.MetricName = metric }).Valid(); err == nil {
				t.Fatalf("metric name %q was accepted", metric)
			}
		})
	}
}

func TestAnUndeclaredQualityIsRefused(t *testing.T) {
	if err := hardware(func(s *Sample) { s.Quality = "probably" }).Valid(); err == nil {
		t.Fatal("an invented measurement quality was accepted")
	}
	if err := hardware(func(s *Sample) { s.Quality = "" }).Valid(); err != nil {
		t.Fatalf("an unset quality should default to exact: %v", err)
	}
}

func TestAScopeThatIsNeitherIsRefused(t *testing.T) {
	if err := hardware(func(s *Sample) { s.Scope = "gpu" }).Valid(); err == nil {
		t.Fatal("an unrecognised scope was accepted")
	}
}

func TestAnInfrastructureEventIsNeverChargedToAnAgent(t *testing.T) {
	// A GPU is used by everything on the host at once. Attributing it to
	// whichever agent happened to be running would invent a per-agent number
	// out of a shared one, and the number would look measured.
	event, err := hardware(nil).Event(uuidFor("instance"), "1.2.3", uuidFor("event"), "2026-09-05T12:00:00.000Z", 1500)
	if err != nil {
		t.Fatalf("a well-formed hardware sample was refused: %v", err)
	}
	if event.Agent.ID != sharedAgent.ID {
		t.Fatalf("agent id was %q, not the shared identity", event.Agent.ID)
	}
	if event.TurnID != nil || event.SessionID != nil {
		t.Fatal("an infrastructure sample was attached to a turn or session")
	}
	if event.EventType != "hardware.sample" {
		t.Fatalf("event type was %q", event.EventType)
	}
}

func TestAHardwareEventCarriesItsNodeAndReader(t *testing.T) {
	event, err := hardware(nil).Event(uuidFor("instance"), "1.2.3", uuidFor("event"), "2026-09-05T12:00:00.000Z", 0)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	attributes := event.Attributes
	// Without both, two machines polled by the same collector average together.
	if attributes["node_id"] != "dgx-spark" || attributes["provider_id"] != "nvidia-smi" {
		t.Fatalf("hardware identity was lost: %v", attributes)
	}
	if attributes["measurement_quality"] != "exact" {
		t.Fatalf("quality was %v", attributes["measurement_quality"])
	}
}

func TestAServerEventNamesItsEndpoint(t *testing.T) {
	sample := Sample{
		Scope: ScopeServer, MetricName: "requests_running", Value: 3,
		Unit: "requests", EndpointID: "dgx-spark-vllm",
	}
	event, err := sample.Event(uuidFor("instance"), "1.2.3", uuidFor("event"), "2026-09-05T12:00:00.000Z", 0)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if event.EndpointID == nil || *event.EndpointID != "dgx-spark-vllm" {
		t.Fatalf("endpoint was %v", event.EndpointID)
	}
}

func TestAnInvalidSampleNeverBecomesAnEvent(t *testing.T) {
	// Event goes through the same validator a producer's submission does. A
	// provider running inside the collector reads output from commands and
	// endpoints it does not control, so it is not the more trusted of the two.
	_, err := hardware(func(s *Sample) { s.Value = math.NaN() }).
		Event(uuidFor("instance"), "1.2.3", uuidFor("event"), "2026-09-05T12:00:00.000Z", 0)
	if err == nil {
		t.Fatal("an unreadable value reached the validator as an event")
	}
}
