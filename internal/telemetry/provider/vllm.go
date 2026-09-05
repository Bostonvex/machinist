package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maximumMetricsBody = 2 * 1024 * 1024
	maximumMetricLine  = 8192
)

// metricLine matches one Prometheus exposition line. The label block is matched
// and discarded rather than parsed: labels routinely carry model names, adapter
// paths and deployment details, and none of that is a metric.
var metricLine = regexp.MustCompile(`^([A-Za-z_:][A-Za-z0-9_:]*)(?:\{[^\r\n]*\})?\s+([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)(?:\s+\d+)?$`)

// The three tables below are an allowlist, not a mapping. A metric vLLM emits
// that is absent from all three is dropped, because a collector that forwarded
// everything an endpoint offered would inherit whatever a future vLLM decides
// to expose, including labels this parser has never seen.
var (
	summedMetrics = map[string]metricTarget{
		"vllm:num_requests_running":    {"requests_running", "requests"},
		"vllm:num_requests_waiting":    {"requests_waiting", "requests"},
		"vllm:num_requests_swapped":    {"requests_swapped", "requests"},
		"vllm:prompt_tokens_total":     {"prompt_tokens_total", "tokens"},
		"vllm:generation_tokens_total": {"generation_tokens_total", "tokens"},
		"vllm:request_success_total":   {"successful_requests_total", "requests"},
		"vllm:num_preemptions_total":   {"preemptions_total", "requests"},
	}
	meanMetrics = map[string]metricTarget{
		"vllm:kv_cache_usage_perc":  {"gpu_kv_cache_usage_ratio", "ratio"},
		"vllm:gpu_cache_usage_perc": {"gpu_kv_cache_usage_ratio", "ratio"},
		"vllm:cpu_cache_usage_perc": {"cpu_kv_cache_usage_ratio", "ratio"},
	}
	histogramMetrics = map[string]metricTarget{
		"vllm:time_to_first_token_seconds": {"request_ttft_mean_seconds", "seconds"},
		"vllm:e2e_request_latency_seconds": {"request_e2e_latency_mean_seconds", "seconds"},
		"vllm:request_queue_time_seconds":  {"request_queue_mean_seconds", "seconds"},
	}
)

type metricTarget struct{ name, unit string }

// Vllm polls a vLLM Prometheus endpoint.
type Vllm struct {
	metricsURL string
	endpointID string
	client     *http.Client
}

// ValidateMetricsURL refuses anything that is not a plain /metrics endpoint.
//
// The path is pinned because this is a poller pointed at an operator-supplied
// URL: without it, a typo or a rewritten config turns a metrics scrape into a
// request to any endpoint on that host, every ten seconds, with whatever the
// URL carried. Credentials are refused for the same reason a token does not
// live in configuration — a URL is copied, logged and pasted.
func ValidateMetricsURL(value string) (string, error) {
	if len(value) > 2048 || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 }) {
		return "", errors.New("vLLM metrics URL is unsafe")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("vLLM metrics URL is not a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("vLLM metrics URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("vLLM metrics URL must name a host")
	}
	if parsed.User != nil {
		return "", errors.New("vLLM metrics URL cannot carry credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimRight(parsed.Path, "/") != "/metrics" {
		return "", errors.New("vLLM metrics URL must be a bare /metrics endpoint")
	}
	return value, nil
}

func NewVllm(metricsURL, endpointID string, timeout time.Duration) (*Vllm, error) {
	validated, err := ValidateMetricsURL(metricsURL)
	if err != nil {
		return nil, err
	}
	if !identifierPattern.MatchString(endpointID) {
		return nil, errors.New("endpoint_id is not a safe identifier")
	}
	if timeout <= 0 {
		timeout = defaultCommandRun
	}
	if timeout > maximumTimeout {
		return nil, fmt.Errorf("vLLM timeout must not exceed %s", maximumTimeout)
	}
	return &Vllm{validated, endpointID, &http.Client{
		Timeout: timeout,
		// Redirects are refused rather than followed. The URL was checked for
		// being a bare /metrics endpoint; a redirect would move the request
		// somewhere that was never checked, which is the whole point of having
		// checked it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the vLLM metrics endpoint redirected")
		},
	}}, nil
}

func (v *Vllm) Name() string { return "vllm" }

func (v *Vllm) Poll(ctx context.Context) ([]Sample, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.metricsURL, nil)
	if err != nil {
		return nil, errors.New("vLLM metrics request could not be built")
	}
	request.Header.Set("Accept", "text/plain")
	request.Header.Set("User-Agent", "machinist-telemetry")

	response, err := v.client.Do(request)
	if err != nil {
		// The transport error is not wrapped in. It can carry the URL, and a
		// provider that fails every ten seconds would write it into the log
		// every ten seconds.
		return nil, errors.New("the vLLM metrics endpoint could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the vLLM metrics endpoint answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumMetricsBody+1))
	if err != nil {
		return nil, errors.New("the vLLM metrics response could not be read")
	}
	return ParsePrometheus(body, v.endpointID)
}

// ParsePrometheus reduces an exposition body to the allowlisted samples.
func ParsePrometheus(body []byte, endpointID string) ([]Sample, error) {
	if len(body) > maximumMetricsBody {
		return nil, errors.New("the vLLM metrics response exceeded the limit")
	}
	if !utf8.Valid(body) {
		return nil, errors.New("the vLLM metrics response is not UTF-8")
	}

	values := map[string][]float64{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > maximumMetricLine {
			return nil, errors.New("a vLLM metric line exceeded the limit")
		}
		match := metricLine.FindStringSubmatch(line)
		if match == nil {
			// A line this parser does not recognise is skipped, not refused.
			// vLLM emits metrics beyond the allowlist and will emit more; a
			// collector that fell over on an unfamiliar line would stop
			// reporting the metrics it does understand every time vLLM
			// gained one.
			continue
		}
		name := match[1]
		if !wanted(name) {
			continue
		}
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maximumValue {
			continue
		}
		values[name] = append(values[name], value)
	}

	var samples []Sample
	server := func(target metricTarget, value float64, quality string) {
		samples = append(samples, Sample{
			Scope: ScopeServer, MetricName: target.name, Value: value,
			Unit: target.unit, EndpointID: endpointID, Quality: quality,
		})
	}
	// Sorted so the emitted order does not depend on map iteration; a caller
	// diffing two polls should see metrics change, not move.
	for _, name := range sortedKeys(summedMetrics) {
		if readings := values[name]; len(readings) > 0 {
			server(summedMetrics[name], sum(readings), "")
		}
	}
	emitted := map[string]bool{}
	for _, name := range sortedKeys(meanMetrics) {
		target := meanMetrics[name]
		// Two vLLM versions expose the same cache ratio under different names.
		// Emitting both would double a gauge that describes one thing.
		if readings := values[name]; len(readings) > 0 && !emitted[target.name] {
			server(target, sum(readings)/float64(len(readings)), "")
			emitted[target.name] = true
		}
	}
	for _, name := range sortedKeys(histogramMetrics) {
		count := sum(values[name+"_count"])
		if count > 0 {
			// A mean recovered from a histogram's sum and count is not a
			// measurement, so it says derived. Reading it as one would treat a
			// number nothing observed as if something had.
			server(histogramMetrics[name], sum(values[name+"_sum"])/count, "derived")
		}
	}
	return samples, nil
}

func wanted(name string) bool {
	if _, ok := summedMetrics[name]; ok {
		return true
	}
	if _, ok := meanMetrics[name]; ok {
		return true
	}
	base, found := strings.CutSuffix(name, "_sum")
	if !found {
		base, found = strings.CutSuffix(name, "_count")
	}
	if found {
		_, ok := histogramMetrics[base]
		return ok
	}
	return false
}

func sum(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total
}

func sortedKeys(table map[string]metricTarget) []string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
