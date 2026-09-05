package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// served starts the collector on an ephemeral loopback port and returns its
// base URL. It stops when the test does.
func served(t *testing.T, server *Server) string {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	addresses := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, "127.0.0.1:0", func(address net.Addr) { addresses <- address }) }()
	var address net.Addr
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatalf("serve returned before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the collector never reported an address")
	}
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the collector did not stop")
		}
	})
	return "http://" + address.String()
}

func TestTheServedCollectorAnswersHealth(t *testing.T) {
	server, _ := newTestServer(t)
	response, err := http.Get(served(t, server) + HealthPath)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", response.StatusCode)
	}
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["status"] != "ok" {
		t.Fatalf("health = %#v", health)
	}
}

// A cancelled context stops the collector and gives the port back. A serve that
// returned while still holding the socket would make a restart fail with an
// error about the address rather than about whatever actually went wrong.
func TestCancellingTheContextReleasesThePort(t *testing.T) {
	server, _ := newTestServer(t)
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	bound := make(chan net.Addr, 1)
	go func() { done <- server.Serve(ctx, address, func(a net.Addr) { bound <- a }) }()
	<-bound
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the collector did not stop")
	}
	reopened, err := Listen(address)
	if err != nil {
		t.Fatalf("the port was not released: %v", err)
	}
	reopened.Close()
}

func TestServeRefusesAnAddressThatIsNotLoopback(t *testing.T) {
	server, _ := newTestServer(t)
	if err := server.Serve(t.Context(), "0.0.0.0:0", nil); err == nil {
		t.Fatal("the collector bound a non-loopback address")
	}
}

// Provider diagnostics are reported only when providers were configured. An
// empty set would say the providers are fine when there are none.
func TestHealthReportsProviderDiagnosticsOnlyWhenConfigured(t *testing.T) {
	server, _ := newTestServer(t)
	base := served(t, server)
	if _, present := healthOf(t, base)["providers"]; present {
		t.Fatal("health reported providers before any were configured")
	}
	server.SetProviderDiagnostics(func() any {
		return map[string]any{"nvidia-smi": map[string]any{"state": "ok"}}
	})
	providers, present := healthOf(t, base)["providers"].(map[string]any)
	if !present {
		t.Fatal("health did not report the configured providers")
	}
	if _, named := providers["nvidia-smi"]; !named {
		t.Fatalf("providers = %#v", providers)
	}
}

func healthOf(t *testing.T, base string) map[string]any {
	t.Helper()
	response, err := http.Get(base + HealthPath)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	var health map[string]any
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health %q: %v", body, err)
	}
	return health
}

// A provider's samples reach live readers, not just the store. A dashboard
// watching a quiet fleet sees hardware move; one fed only by the ingest
// endpoint would see nothing until an agent did something.
func TestProviderEventsAreStoredAndPublished(t *testing.T) {
	server, store := newTestServer(t)
	subscriber := server.broker.subscribe()
	defer server.broker.unsubscribe(subscriber)

	server.Ingest(context.Background(), []Event{event(t, "provider-1", EventTurnStarted, nil)})

	select {
	case live := <-subscriber:
		if live.EventID != uuidFor("provider-1") {
			t.Fatalf("published %#v", live)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a provider event was never published")
	}
	health, err := store.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if health["events"] != 1 {
		t.Fatalf("stored %#v", health["events"])
	}
}

// Nothing is published when the store refused the write: a live reader would
// be shown an event it could never look up.
func TestAProviderEventTheStoreRefusesIsNotPublished(t *testing.T) {
	server, store := newTestServer(t)
	subscriber := server.broker.subscribe()
	defer server.broker.unsubscribe(subscriber)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server.Ingest(context.Background(), []Event{event(t, "provider-2", EventTurnStarted, nil)})

	select {
	case live := <-subscriber:
		t.Fatalf("published %#v after the store refused it", live)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestIngestingNothingDoesNothing(t *testing.T) {
	server, _ := newTestServer(t)
	subscriber := server.broker.subscribe()
	defer server.broker.unsubscribe(subscriber)
	server.Ingest(context.Background(), nil)
	select {
	case live := <-subscriber:
		t.Fatalf("published %#v for an empty batch", live)
	case <-time.After(100 * time.Millisecond):
	}
}
