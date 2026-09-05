package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

// proxyConfigAt writes a config whose collector has a model proxy in front of
// upstream, and returns its path.
func proxyConfigAt(t *testing.T, upstream, extra string) string {
	t.Helper()
	directory := t.TempDir()
	return enabledCollectorAt(t, directory, `
[collector.proxy]
listen = "127.0.0.1:0"
upstream = "`+upstream+`"
model = "ds-0731"
endpoint_id = "dgx-primary"
context_token_file = "`+filepath.Join(directory, "proxy-context.token")+`"
`+extra)
}

func TestTheProxyIsRefusedWithoutItsTable(t *testing.T) {
	// Starting on defaults would put a process in the model call path that
	// forwards to somewhere nobody named.
	path := enabledCollectorAt(t, t.TempDir(), "")
	code, stdout, stderr := run(t, "proxy", "start", "--config", path)
	if code == 0 {
		t.Fatalf("an unconfigured proxy started: %s%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "[collector.proxy]") {
		t.Fatalf("the refusal does not say what to write: %s", stderr)
	}
}

func TestTheProxyTableRequiresWhatItIsMeasuring(t *testing.T) {
	// A measurement with no endpoint on it is a number nobody can act on, so
	// the identity is required rather than defaulted to something plausible.
	missing := map[string]string{
		"upstream":           "model = \"m\"\nendpoint_id = \"e\"\ncontext_token_file = \"t\"\n",
		"model":              "upstream = \"http://127.0.0.1:9\"\nendpoint_id = \"e\"\ncontext_token_file = \"t\"\n",
		"endpoint_id":        "upstream = \"http://127.0.0.1:9\"\nmodel = \"m\"\ncontext_token_file = \"t\"\n",
		"context_token_file": "upstream = \"http://127.0.0.1:9\"\nmodel = \"m\"\nendpoint_id = \"e\"\n",
	}
	for field, table := range missing {
		path := enabledCollectorAt(t, t.TempDir(), "\n[collector.proxy]\n"+table)
		if _, err := config.LoadConfig(path); err == nil {
			t.Fatalf("a proxy table with no %s was accepted", field)
		}
	}
}

func TestTheProxyRefusesANonLoopbackListenAddress(t *testing.T) {
	// The proxy carries whatever credential the harness sends to the model
	// endpoint. Bound to a routable interface it is an open relay for that
	// key, which must not be reachable by a typo.
	path := proxyConfigAt(t, "http://127.0.0.1:9", "")
	rewrite(t, path, `listen = "127.0.0.1:0"
upstream`, `listen = "0.0.0.0:7901"
upstream`)
	if _, err := config.LoadConfig(path); err == nil {
		t.Fatal("a proxy bound to every interface was accepted")
	}
}

func TestTheProxyTakesTheDefaultPortWhenNoneIsWritten(t *testing.T) {
	path := proxyConfigAt(t, "http://127.0.0.1:9", "")
	// The collector table carries a listen line of its own, so the proxy's is
	// identified by what follows it.
	rewrite(t, path, "listen = \"127.0.0.1:0\"\nupstream", "upstream")
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Collector.Proxy.Listen != "127.0.0.1:7901" {
		t.Fatalf("listen = %q", loaded.Collector.Proxy.Listen)
	}
}

func TestTheProxyForwardsACallAndDeliversWhatItMeasured(t *testing.T) {
	// End to end through the verb: the upstream is reached, the answer comes
	// back unchanged, and the running collector holds the measurement.
	var forwarded int
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		forwarded++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(upstream.Close)

	path := proxyConfigAt(t, upstream.URL, "")
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// The collector runs first, on an ephemeral port, and the proxy is then
	// pointed at the address it actually took. A test that assumed 7900 would
	// pass or fail on what else is running on this machine.
	collectorConfig := loaded.Collector
	base := startedCollector(t, collectorConfig)
	collectorConfig.Listen = strings.TrimPrefix(base, "http://")

	address, stop := startedProxy(t, collectorConfig)
	response, err := http.Post("http://"+address+"/v1/chat/completions",
		"application/json", strings.NewReader(`{"model":"ds-0731"}`))
	if err != nil {
		t.Fatalf("post through the proxy: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()

	if string(body) != `{"choices":[]}` {
		t.Fatalf("body = %q, want the upstream's answer unchanged", body)
	}
	if forwarded != 1 {
		t.Fatalf("the upstream saw %d calls, want one", forwarded)
	}

	// Stopping the proxy flushes the queue, so what the collector holds
	// afterwards is what the call produced rather than what happened to have
	// been delivered by the time the assertion ran.
	stop()
	waitForEvents(t, base, collectorConfig.TokenFile)
}

func TestTheProxyStartsWithNoCollectorListening(t *testing.T) {
	// The proxy sits in the model call path. A collector that is down must
	// cost telemetry, never the ability to make a model call.
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(upstream.Close)

	path := proxyConfigAt(t, upstream.URL, "")
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// A port nothing is listening on. The collector's secrets are created by
	// the proxy's own start, which is what lets it run before one ever has.
	collectorConfig := loaded.Collector
	collectorConfig.Listen = "127.0.0.1:1"

	address, stop := startedProxy(t, collectorConfig)
	defer stop()
	response, err := http.Post("http://"+address+"/v1/chat/completions",
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("the proxy failed a call because the collector was down: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the upstream's answer", response.StatusCode)
	}
}

// startedProxy runs the proxy verb in the background and returns the address it
// took, with the function that stops it and waits for the flush.
func startedProxy(t *testing.T, collectorConfig config.Collector) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	options := &commandOptions{
		stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, version: "test",
	}
	addresses := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveProxy(ctx, options, collectorConfig, *collectorConfig.Proxy,
			func(address net.Addr) { addresses <- address })
	}()

	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("proxy: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the proxy did not stop")
		}
	}
	t.Cleanup(stop)

	select {
	case address := <-addresses:
		return address.String(), stop
	case err := <-done:
		t.Fatalf("the proxy stopped before it listened: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy never reported an address")
	}
	return "", stop
}

// waitForEvents asks the collector how many events it holds, until the call the
// proxy measured has arrived.
func waitForEvents(t *testing.T, base, tokenFile string) {
	t.Helper()
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read the collector token: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	held := -1
	for time.Now().Before(deadline) {
		held = eventsHeld(t, base, strings.TrimSpace(string(token)))
		// A model call produces a start and a completion. Anything less means
		// the proxy measured the call but the collector never took it.
		if held >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the collector holds %d events, want the two the call produced", held)
}

// eventsHeld reads the collector's own count of what it has stored.
func eventsHeld(t *testing.T, base, token string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return -1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the collector answered health with %d", response.StatusCode)
	}
	var health struct {
		Events *int `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.Events == nil {
		t.Fatal("the collector's health does not report an event count")
	}
	return *health.Events
}

func rewrite(t *testing.T, path, from, to string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := strings.Replace(string(body), from, to, 1)
	if replaced == string(body) {
		t.Fatalf("the config did not contain %q", from)
	}
	if err := os.WriteFile(path, []byte(replaced), 0o600); err != nil {
		t.Fatal(err)
	}
}
