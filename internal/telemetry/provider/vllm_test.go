package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func named(t *testing.T, samples []Sample, name string) Sample {
	t.Helper()
	for _, sample := range samples {
		if sample.MetricName == name {
			return sample
		}
	}
	t.Fatalf("no sample named %q in %v", name, samples)
	return Sample{}
}

func absent(t *testing.T, samples []Sample, name string) {
	t.Helper()
	for _, sample := range samples {
		if sample.MetricName == name {
			t.Fatalf("sample %q should not have been emitted", name)
		}
	}
}

const exposition = `# HELP vllm:num_requests_running Number running.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="ds-0731"} 3.0
vllm:num_requests_running{model_name="other"} 2.0
vllm:num_requests_waiting{model_name="ds-0731"} 1.0
vllm:gpu_cache_usage_perc{model_name="ds-0731"} 0.25
vllm:time_to_first_token_seconds_sum{model_name="ds-0731"} 12.0
vllm:time_to_first_token_seconds_count{model_name="ds-0731"} 4.0
vllm:time_to_first_token_seconds_bucket{le="0.5",model_name="ds-0731"} 2.0
process_resident_memory_bytes 1.234e+09
python_gc_objects_collected_total{generation="0"} 512.0
`

func TestOnlyAllowlistedMetricsSurvive(t *testing.T) {
	// A collector that forwarded everything an endpoint offered would inherit
	// whatever a future vLLM decides to expose, labels included.
	samples, err := ParsePrometheus([]byte(exposition), "dgx-spark-vllm")
	if err != nil {
		t.Fatalf("a normal exposition was refused: %v", err)
	}
	absent(t, samples, "process_resident_memory_bytes")
	absent(t, samples, "python_gc_objects_collected_total")
	for _, sample := range samples {
		if err := sample.Valid(); err != nil {
			t.Fatalf("the parser produced a sample the validator refuses: %v", err)
		}
		if sample.Scope != ScopeServer || sample.EndpointID != "dgx-spark-vllm" {
			t.Fatalf("a server sample lost its endpoint: %+v", sample)
		}
	}
}

func TestOneMetricSplitAcrossModelsIsSummed(t *testing.T) {
	// vLLM reports per-model series. Taking the first would report three of the
	// five requests actually running on the server.
	samples, err := ParsePrometheus([]byte(exposition), "dgx-spark-vllm")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if running := named(t, samples, "requests_running"); running.Value != 5 {
		t.Fatalf("requests_running was %v, not the sum of both series", running.Value)
	}
}

func TestAMeanRecoveredFromAHistogramSaysSo(t *testing.T) {
	// Nothing observed twelve-over-four seconds. Recording it as exact would
	// present an arithmetic result as a measurement.
	samples, err := ParsePrometheus([]byte(exposition), "dgx-spark-vllm")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	ttft := named(t, samples, "request_ttft_mean_seconds")
	if ttft.Value != 3 {
		t.Fatalf("the mean was %v, not sum over count", ttft.Value)
	}
	if ttft.Quality != "derived" {
		t.Fatalf("a derived value was labelled %q", ttft.Quality)
	}
}

func TestAHistogramWithNoObservationsReportsNothing(t *testing.T) {
	// Dividing by a zero count would emit NaN, and a chart would draw it.
	samples, err := ParsePrometheus([]byte(
		"vllm:e2e_request_latency_seconds_sum 0.0\nvllm:e2e_request_latency_seconds_count 0.0\n"), "e")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	absent(t, samples, "request_e2e_latency_mean_seconds")
}

func TestTwoNamesForOneRatioAreNotCountedTwice(t *testing.T) {
	// Two vLLM versions expose the same cache ratio under different names, and
	// emitting both would double a gauge that describes one thing.
	body := "vllm:gpu_cache_usage_perc 0.25\nvllm:kv_cache_usage_perc 0.75\n"
	samples, err := ParsePrometheus([]byte(body), "dgx-spark-vllm")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	count := 0
	for _, sample := range samples {
		if sample.MetricName == "gpu_kv_cache_usage_ratio" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the cache ratio was emitted %d times", count)
	}
}

func TestAnUnfamiliarLineDoesNotStopTheScrape(t *testing.T) {
	// vLLM will emit metrics this parser has never seen. Falling over on one
	// would stop reporting the metrics it does understand every time vLLM
	// gained a new one.
	body := "this is not exposition at all\nvllm:num_requests_running 7.0\n\x00garbage line\n"
	samples, err := ParsePrometheus([]byte(body), "dgx-spark-vllm")
	if err != nil {
		t.Fatalf("an unfamiliar line stopped the scrape: %v", err)
	}
	if running := named(t, samples, "requests_running"); running.Value != 7 {
		t.Fatalf("the readable metric was lost: %v", running.Value)
	}
}

func TestTheEmittedOrderDoesNotDependOnMapIteration(t *testing.T) {
	// A caller diffing two polls should see metrics change, not move.
	first, err := ParsePrometheus([]byte(exposition), "e")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		again, err := ParsePrometheus([]byte(exposition), "e")
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		for index := range first {
			if first[index].MetricName != again[index].MetricName {
				t.Fatalf("order changed between polls: %q then %q", first[index].MetricName, again[index].MetricName)
			}
		}
	}
}

func TestOnlyABareMetricsEndpointIsPolled(t *testing.T) {
	// This is a poller aimed at an operator-supplied URL. Without the pinned
	// path, a typo turns a scrape into a request to any endpoint on that host,
	// every few seconds, carrying whatever the URL carried.
	for name, value := range map[string]string{
		"a different path": "http://127.0.0.1:18000/v1/completions",
		"a query string":   "http://127.0.0.1:18000/metrics?reset=true",
		"credentials":      "http://user:secret@127.0.0.1:18000/metrics",
		"a file url":       "file:///etc/passwd",
		"a fragment":       "http://127.0.0.1:18000/metrics#x",
		"no host":          "http:///metrics",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateMetricsURL(value); err == nil {
				t.Fatalf("%q was accepted", value)
			}
		})
	}
	if _, err := ValidateMetricsURL("http://127.0.0.1:18000/metrics"); err != nil {
		t.Fatalf("a plain metrics endpoint was refused: %v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	// The URL was checked for being a bare /metrics endpoint. Following a
	// redirect would move the request somewhere that was never checked.
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the poller followed a redirect to another host")
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/metrics", http.StatusFound)
	}))
	defer server.Close()

	provider, err := NewVllm(server.URL+"/metrics", "dgx-spark-vllm", 2*time.Second)
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	if _, err := provider.Poll(context.Background()); err == nil {
		t.Fatal("a redirect was followed")
	}
}

func TestAnEndpointThatIsDownIsReportedWithoutItsURL(t *testing.T) {
	// A provider that fails every few seconds writes to the log every few
	// seconds, and the transport error carries the URL.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	provider, err := NewVllm(server.URL+"/metrics", "dgx-spark-vllm", 2*time.Second)
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	if _, err := provider.Poll(context.Background()); err == nil {
		t.Fatal("a 503 was accepted as metrics")
	}
	server.Close()

	_, err = provider.Poll(context.Background())
	if err == nil {
		t.Fatal("a dead endpoint was accepted")
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("the failure repeated the endpoint URL: %v", err)
	}
}

func TestAWorkingEndpointIsScraped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(exposition))
	}))
	defer server.Close()
	provider, err := NewVllm(server.URL+"/metrics", "dgx-spark-vllm", 2*time.Second)
	if err != nil {
		t.Fatalf("could not build the provider: %v", err)
	}
	samples, err := provider.Poll(context.Background())
	if err != nil {
		t.Fatalf("a working endpoint was refused: %v", err)
	}
	if named(t, samples, "requests_waiting").Value != 1 {
		t.Fatal("the scrape did not read the endpoint")
	}
	if provider.Name() != "vllm" {
		t.Fatalf("provider name was %q", provider.Name())
	}
}

func TestABodyTooLargeToBeMetricsIsRefused(t *testing.T) {
	if _, err := ParsePrometheus(make([]byte, maximumMetricsBody+1), "e"); err == nil {
		t.Fatal("an oversized body was parsed")
	}
	line := "vllm:num_requests_running " + strings.Repeat("0", maximumMetricLine) + "\n"
	if _, err := ParsePrometheus([]byte(line), "e"); err == nil {
		t.Fatal("an oversized line was parsed")
	}
}

func TestAResponseThatIsNotTextIsRefused(t *testing.T) {
	if _, err := ParsePrometheus([]byte{0xff, 0xfe, 0xfd}, "e"); err == nil {
		t.Fatal("a non-UTF-8 body was parsed")
	}
}
