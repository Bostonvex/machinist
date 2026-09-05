package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a sink that keeps what the proxy measured.
type recorder struct {
	mutex  sync.Mutex
	events []Event
}

func (r *recorder) Enqueue(event Event) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.events = append(r.events, event)
	return true
}

func (r *recorder) types() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	names := make([]string, len(r.events))
	for index, event := range r.events {
		names[index] = event.EventType
	}
	return names
}

func (r *recorder) last(eventType string) (Event, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for index := len(r.events) - 1; index >= 0; index-- {
		if r.events[index].EventType == eventType {
			return r.events[index], true
		}
	}
	return Event{}, false
}

// proxied stands a proxy in front of an upstream and returns both.
func proxied(t *testing.T, upstream http.Handler) (*httptest.Server, *recorder, *httptest.Server) {
	t.Helper()
	origin := httptest.NewServer(upstream)
	t.Cleanup(origin.Close)

	settings, err := Validate(Config{
		Upstream: origin.URL, Model: "ds-0731", EndpointID: "dgx-primary",
		ConnectTimeout: time.Second, ResponseTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	sink := &recorder{}
	front := httptest.NewServer(New(settings, sink).Handler())
	t.Cleanup(front.Close)
	return front, sink, origin
}

func post(t *testing.T, server *httptest.Server, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestTheProxyForwardsTheBodyAndTheAnswerUnchanged(t *testing.T) {
	// Byte parity is the whole compatibility claim. A model client must not be
	// able to tell it is talking through this.
	var received []byte
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		received, _ = io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("X-Request-Id", "upstream-42")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"id":"chatcmpl-1","choices":[]}`))
	}))

	sent := `{"model":"ds-0731","messages":[{"role":"user","content":"hello"}]}`
	response := post(t, front, "/v1/chat/completions", sent, nil)
	body, _ := io.ReadAll(response.Body)

	if string(received) != sent {
		t.Fatalf("upstream received %q, want %q", received, sent)
	}
	if string(body) != `{"id":"chatcmpl-1","choices":[]}` {
		t.Fatalf("client received %q", body)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("X-Request-Id"); got != "upstream-42" {
		t.Fatalf("upstream header was not preserved: %q", got)
	}
}

func TestTheProxyForwardsTheCredentialAndNotTheCorrelation(t *testing.T) {
	// The Authorization header is the model client's and has to reach the
	// model. The correlation headers are this system's own, mean nothing
	// upstream, and would send a turn identifier to a third party for no
	// reason at all.
	var seen http.Header
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		response.WriteHeader(http.StatusOK)
	}))

	post(t, front, "/v1/chat/completions", "{}", map[string]string{
		"Authorization":                    "Bearer sk-secret",
		"X-Machinist-Telemetry-Context-Id": "turn-1",
	})

	if got := seen.Get("Authorization"); got != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q, want it forwarded untouched", got)
	}
	for name := range seen {
		if strings.HasPrefix(strings.ToLower(name), ContextHeaderPrefix) {
			t.Fatalf("correlation header %s reached the upstream", name)
		}
	}
}

func TestTheProxyDoesNotTellTheUpstreamAboutTheMachineCallingIt(t *testing.T) {
	var seen http.Header
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		response.WriteHeader(http.StatusOK)
	}))
	post(t, front, "/v1/chat/completions", "{}", nil)

	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if value := seen.Get(name); value != "" {
			t.Fatalf("%s = %q was sent to the model endpoint", name, value)
		}
	}
}

func TestARequestCannotChooseItsPath(t *testing.T) {
	// The closed path set is what stops a proxy holding a harness's
	// credentials from being asked to send them somewhere else.
	reached := false
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reached = true
		response.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/v1/models", "/", "/admin", "/v1/files", "/v1/chat/completions/x"} {
		response := post(t, front, path, "{}", nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s answered %d, want a refusal", path, response.StatusCode)
		}
	}
	if reached {
		t.Fatal("a refused path still reached the upstream")
	}
}

func TestOnlyPostIsForwarded(t *testing.T) {
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	request, _ := http.NewRequest(http.MethodGet, front.URL+"/v1/chat/completions", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %d", response.StatusCode)
	}
}

func TestAChunkedUploadIsRefusedRatherThanRead(t *testing.T) {
	// Without a declared length there is no size to refuse before reading it,
	// so the limit would be advisory. Requiring the length makes it real.
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	request, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions",
		io.NopCloser(strings.NewReader("{}")))
	request.ContentLength = -1
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusLengthRequired {
		t.Fatalf("a chunked upload answered %d", response.StatusCode)
	}
}

func TestTheProxyMeasuresAForwardedCall(t *testing.T) {
	front, sink, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	post(t, front, "/v1/chat/completions", "{}", nil)

	types := sink.types()
	if len(types) != 2 || types[0] != ModelRequestStarted || types[1] != ModelCompleted {
		t.Fatalf("events = %v, want a start and a completion", types)
	}
	completed, _ := sink.last(ModelCompleted)
	if completed.Attributes["http_status"] != 200 {
		t.Fatalf("http_status = %v", completed.Attributes["http_status"])
	}
	if _, measured := completed.Attributes["first_byte_ms"]; !measured {
		t.Fatal("no first_byte_ms was recorded")
	}
	if completed.Attributes["duration_ms"].(float64) < 0 {
		t.Fatalf("duration_ms = %v", completed.Attributes["duration_ms"])
	}
	if completed.SpanID == "" || completed.Producer.Name != "machinist-model-proxy" {
		t.Fatalf("event identity = %+v", completed)
	}
	started, _ := sink.last(ModelRequestStarted)
	if started.SpanID != completed.SpanID {
		t.Fatal("the start and the completion are not the same call")
	}
}

func TestAnUpstreamErrorIsAnAnsweredCallAndNotALostOne(t *testing.T) {
	// An upstream that answered 429 answered. Calling it a transport failure
	// would put it in the same bucket as an endpoint that was not there.
	front, sink, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	response := post(t, front, "/v1/chat/completions", "{}", nil)
	body, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the upstream's", response.StatusCode)
	}
	if !strings.Contains(string(body), "slow down") {
		t.Fatalf("the upstream's error body was not preserved: %q", body)
	}
	failed, ok := sink.last(ModelFailed)
	if !ok {
		t.Fatalf("events = %v, want a failure", sink.types())
	}
	if failed.Attributes["error_category"] != "upstream_http" {
		t.Fatalf("error_category = %v", failed.Attributes["error_category"])
	}
	if failed.Attributes["error_code"] != "http_429" {
		t.Fatalf("error_code = %v", failed.Attributes["error_code"])
	}
	if failed.Attributes["http_status"] != 429 {
		t.Fatalf("http_status = %v", failed.Attributes["http_status"])
	}
}

func TestAnUnreachableUpstreamIsRefusedWithoutNamingIt(t *testing.T) {
	// A harness renders what it gets back, and a transport error can carry the
	// upstream host and the reason a handshake failed.
	settings, err := Validate(Config{
		// Port 1 on loopback, which nothing listens on.
		Upstream: "http://127.0.0.1:1", Model: "ds-0731", EndpointID: "dgx-primary",
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	sink := &recorder{}
	front := httptest.NewServer(New(settings, sink).Handler())
	defer front.Close()

	response := post(t, front, "/v1/chat/completions", "{}", nil)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if strings.Contains(string(body), "127.0.0.1") {
		t.Fatalf("the refusal named the upstream: %q", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the refusal is not the shape a model client renders: %q", body)
	}
	failed, ok := sink.last(ModelFailed)
	if !ok {
		t.Fatalf("events = %v, want a failure", sink.types())
	}
	if failed.Attributes["error_category"] != "upstream_transport" {
		t.Fatalf("error_category = %v", failed.Attributes["error_category"])
	}
}

func TestAStreamedResponseIsNotHeldUntilItEnds(t *testing.T) {
	// A model client measures its own first token. A proxy that buffered the
	// stream would make that measurement about this process.
	release := make(chan struct{})
	front, _, _ := proxied(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
		response.(http.Flusher).Flush()
		<-release
		_, _ = response.Write([]byte("data: [DONE]\n\n"))
	}))

	request, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions", strings.NewReader("{}"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	first := make([]byte, 64)
	read, err := response.Body.Read(first)
	if err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}
	if !bytes.Contains(first[:read], []byte("delta")) {
		t.Fatalf("the first chunk was %q", first[:read])
	}
	close(release)
	_, _ = io.ReadAll(response.Body)
}

func TestTheProxyServesOnTheAddressItReports(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()
	settings, err := Validate(Config{Upstream: origin.URL, Model: "m", EndpointID: "e"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	server := New(settings, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for server.URL() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if server.URL() == "" {
		t.Fatal("the proxy never reported an address")
	}
	if !strings.HasPrefix(server.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback address", server.URL())
	}

	response, err := http.Post(server.URL()+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post to the reported address: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

func TestAnUpstreamUnderABasePathKeepsIt(t *testing.T) {
	// /api/anthropic is a real deployment, and the base path must appear
	// exactly once in what the upstream receives.
	var path string
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		response.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()
	settings, err := Validate(Config{
		Upstream: origin.URL + "/api/anthropic", Model: "m", EndpointID: "e",
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	front := httptest.NewServer(New(settings, nil).Handler())
	defer front.Close()

	post(t, front, "/v1/messages", "{}", nil)
	if path != "/api/anthropic/v1/messages" {
		t.Fatalf("upstream received %q", path)
	}
}

func TestAProxyWithNoCollectorStillForwards(t *testing.T) {
	// Measuring nothing is a supported deployment. The proxy is a forwarder
	// first and a measurement second, and the second must not be able to stop
	// the first.
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte("ok"))
	}))
	defer origin.Close()
	settings, err := Validate(Config{Upstream: origin.URL, Model: "m", EndpointID: "e"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	front := httptest.NewServer(New(settings, nil).Handler())
	defer front.Close()

	response := post(t, front, "/v1/completions", "{}", nil)
	body, _ := io.ReadAll(response.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}
