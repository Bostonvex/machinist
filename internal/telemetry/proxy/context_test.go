package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "a-token-long-enough-to-be-one"

// declaring stands a proxy that accepts contexts in front of an upstream.
func declaring(t *testing.T, upstream http.Handler) (*httptest.Server, *recorder) {
	t.Helper()
	origin := httptest.NewServer(upstream)
	t.Cleanup(origin.Close)

	settings, err := Validate(Config{
		Upstream: origin.URL, Model: "ds-0731", EndpointID: "dgx-primary",
		ContextToken:   testToken,
		ConnectTimeout: time.Second, ResponseTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	sink := &recorder{}
	front := httptest.NewServer(New(settings, sink).Handler())
	t.Cleanup(front.Close)
	return front, sink
}

func answered(t *testing.T, server *httptest.Server, token, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+ContextPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return response.StatusCode, decoded
}

func ok(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write([]byte(`{"choices":[]}`))
}

func TestADeclaredTurnClaimsTheCallsMadeUnderIt(t *testing.T) {
	front, sink := declaring(t, http.HandlerFunc(ok))

	status, body := answered(t, front, testToken, `{"action":"start","context":{
		"context_id":"ctx-a","agent_id":"reviewer","display_name":"Reviewer",
		"harness":"codex","model":"ds-0731","endpoint_id":"dgx-primary",
		"session_id":"session-1","turn_id":"turn-1"}}`)
	if status != http.StatusOK || body["active_contexts"] != float64(1) {
		t.Fatalf("declare = %d %v", status, body)
	}

	post(t, front, "/v1/chat/completions", `{}`, map[string]string{
		ContextHeaderPrefix + "context-id": "ctx-a",
	})
	completed := sink.settled(t)
	if value(completed.SessionID) != "session-1" || value(completed.TurnID) != "turn-1" {
		t.Fatalf("event = %+v, want the declared turn", completed)
	}
	if completed.Agent.ID != "reviewer" || value(completed.Harness) != "codex" {
		t.Fatalf("event = %+v, want the declared agent", completed)
	}
	if completed.Attributes["correlation"] != CorrelationExact {
		t.Fatalf("correlation = %v, want exact", completed.Attributes["correlation"])
	}
	// The turn is the call's parent: the turn made the call, not the reverse.
	if value(completed.ParentSpanID) != "turn-1" {
		t.Fatalf("parent = %v, want the turn", completed.ParentSpanID)
	}
}

func TestACallOutsideAnyTurnIsAttributedToTheEndpoint(t *testing.T) {
	front, sink := declaring(t, http.HandlerFunc(ok))
	post(t, front, "/v1/chat/completions", `{}`, nil)

	completed := sink.settled(t)
	if completed.Attributes["correlation"] != CorrelationUnavailable {
		t.Fatalf("correlation = %v, want unavailable", completed.Attributes["correlation"])
	}
	if completed.Agent.ID != "dgx-primary" || value(completed.Model) != "ds-0731" {
		t.Fatalf("event = %+v, want the endpoint's identity", completed)
	}
	if completed.SessionID != nil || completed.TurnID != nil {
		t.Fatal("a call outside any turn named one")
	}
}

func TestTheContextRouteRefusesTheWrongToken(t *testing.T) {
	front, _ := declaring(t, http.HandlerFunc(ok))
	for name, token := range map[string]string{
		"no token":     "",
		"wrong token":  "not-the-token-but-long-enough",
		"prefix only":  testToken[:8],
		"with a comma": testToken + ",",
	} {
		if status, _ := answered(t, front, token, `{"action":"end","context_id":"ctx-a"}`); status != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", name, status)
		}
	}
}

func TestTheContextRouteIsClosedWhenNoTokenIsConfigured(t *testing.T) {
	// A proxy that cannot authenticate a caller has no caller it may serve.
	// Leaving the route open and unauthenticated would let anything on this
	// machine decide whose latency a model call becomes.
	front, _, _ := proxied(t, http.HandlerFunc(ok))
	status, _ := answered(t, front, testToken, `{"action":"end","context_id":"ctx-a"}`)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestTheContextRouteRefusesABodyItDoesNotFullyUnderstand(t *testing.T) {
	front, _ := declaring(t, http.HandlerFunc(ok))
	refused := map[string]string{
		"no action":         `{"context":{"context_id":"ctx-a"}}`,
		"unknown action":    `{"action":"pause","context_id":"ctx-a"}`,
		"unknown field":     `{"action":"end","context_id":"ctx-a","priority":9}`,
		"start with no ctx": `{"action":"start"}`,
		"start missing ids": `{"action":"start","context":{"context_id":"ctx-a","agent_id":"r","display_name":"R"}}`,
		"end with a ctx":    `{"action":"end","context_id":"ctx-a","context":{"context_id":"ctx-a"}}`,
		"trailing json":     `{"action":"end","context_id":"ctx-a"}{"action":"end","context_id":"ctx-b"}`,
		"not an object":     `["start"]`,
		"unsafe id":         `{"action":"end","context_id":"ctx a"}`,
	}
	for name, body := range refused {
		if status, _ := answered(t, front, testToken, body); status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422", name, status)
		}
	}
}

func TestTheContextRouteRefusesAnOversizedBody(t *testing.T) {
	front, _ := declaring(t, http.HandlerFunc(ok))
	oversized := `{"action":"end","context_id":"` + strings.Repeat("a", MaximumContextBytes) + `"}`
	if status, _ := answered(t, front, testToken, oversized); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", status)
	}
	if status, _ := answered(t, front, testToken, `{`[:1]); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body too short to be a declaration was not refused")
	}
}

func TestEndingATurnReturnsTheCallsToTheEndpoint(t *testing.T) {
	front, sink := declaring(t, http.HandlerFunc(ok))
	start := `{"action":"start","context":{"context_id":"ctx-a","agent_id":"reviewer",
		"display_name":"Reviewer","session_id":"s1","turn_id":"t1"}}`
	if status, _ := answered(t, front, testToken, start); status != http.StatusOK {
		t.Fatalf("start: status = %d", status)
	}
	status, body := answered(t, front, testToken, `{"action":"end","context_id":"ctx-a"}`)
	if status != http.StatusOK || body["active_contexts"] != float64(0) {
		t.Fatalf("end = %d %v", status, body)
	}

	post(t, front, "/v1/chat/completions", `{}`, nil)
	if completed := sink.settled(t); completed.Attributes["correlation"] != CorrelationUnavailable {
		t.Fatalf("correlation = %v, want unavailable after the turn ended",
			completed.Attributes["correlation"])
	}
}

func TestTheContextRouteIsNotForwarded(t *testing.T) {
	// The upstream must never see it, whatever the forwarded path set says.
	var reached bool
	front, _ := declaring(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reached = true
		ok(response, request)
	}))
	answered(t, front, testToken, `{"action":"end","context_id":"ctx-a"}`)
	if reached {
		t.Fatal("a context declaration reached the upstream")
	}
}

func TestTheContextPathCannotBeConfiguredAsAForwardedPath(t *testing.T) {
	_, err := Validate(Config{
		Upstream: "http://127.0.0.1:9", Model: "m", EndpointID: "e",
		Paths: []string{ContextPath},
	})
	if err == nil {
		t.Fatal("the context path was accepted as a forwarded path")
	}
}

func TestAShortContextTokenIsRefusedAtStartup(t *testing.T) {
	// The token decides who a model call is attributed to. One short enough to
	// guess is not a boundary, and finding that out at startup is the only
	// place it can still be declined.
	if _, err := Validate(Config{
		Upstream: "http://127.0.0.1:9", Model: "m", EndpointID: "e", ContextToken: "short",
	}); err == nil {
		t.Fatal("a guessable context token was accepted")
	}
}
