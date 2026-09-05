package managedworker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// pollRecorder answers every poll with the queued responses in turn, repeating
// the last one, and keeps the requests it was sent.
type pollRecorder struct {
	mutex     sync.Mutex
	requests  []protocol.PollRequest
	responses []protocol.PollResponse
}

func (r *pollRecorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var input protocol.PollRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode poll request: %v", err)
		}
		r.mutex.Lock()
		r.requests = append(r.requests, input)
		next := r.responses[min(len(r.requests), len(r.responses))-1]
		r.mutex.Unlock()
		if err := json.NewEncoder(response).Encode(next); err != nil {
			t.Errorf("encode poll response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (r *pollRecorder) seen() []protocol.PollRequest {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]protocol.PollRequest(nil), r.requests...)
}

func fleetWorker(t *testing.T, server *httptest.Server, fleet string, stderr io.Writer) *Worker {
	t.Helper()
	return &Worker{
		config: config.Worker{
			Name:         "local-test",
			Fleet:        fleet,
			ControlPlane: config.ControlPlane{URL: server.URL},
			Executors:    map[string]config.Executor{"test": {Command: []string{"true"}}},
			Repositories: map[string]config.Repository{"machinist": {Path: t.TempDir()}},
		},
		instanceID: "worker-test",
		client:     newClient(server.URL, "secret", server.Client()),
		stderr:     stderr,
	}
}

func TestTheWorkerTellsTheControlPlaneWhichFleetItIsIn(t *testing.T) {
	recorder := &pollRecorder{responses: []protocol.PollResponse{{}}}
	server := recorder.serve(t)
	worker := fleetWorker(t, server, "  workshop  ", io.Discard)
	if _, err := worker.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	seen := recorder.seen()
	if len(seen) != 1 || seen[0].Fleet != "workshop" {
		t.Fatalf("poll requests = %#v, want the configured fleet", seen)
	}
}

func TestAWorkerInNoFleetClaimsNone(t *testing.T) {
	recorder := &pollRecorder{responses: []protocol.PollResponse{{}}}
	server := recorder.serve(t)
	worker := fleetWorker(t, server, "", io.Discard)
	if _, err := worker.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Not "default", not the hostname, not the worker name: a fleet nobody
	// configured is a fleet nobody can stand down, and inventing one here would
	// let a host be refused under a name its operator never chose.
	if seen := recorder.seen(); seen[0].Fleet != "" {
		t.Fatalf("fleet = %q, want none invented", seen[0].Fleet)
	}
}

func TestAStandingRefusalIsSaidOnceAndItsLiftingIsSaidOnce(t *testing.T) {
	recorder := &pollRecorder{responses: []protocol.PollResponse{
		{Refused: "fleet \"workshop\" is stood down: owner is at the keyboard"},
	}}
	server := recorder.serve(t)
	var stderr strings.Builder
	worker := fleetWorker(t, server, "workshop", &stderr)
	for range 3 {
		if _, err := worker.poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if said := strings.Count(stderr.String(), "not taking work"); said != 1 {
		t.Fatalf("said the refusal %d times in %q, want once", said, stderr.String())
	}
	if !strings.Contains(stderr.String(), "owner is at the keyboard") {
		t.Fatalf("stderr = %q, want the operator's reason", stderr.String())
	}

	recorder.mutex.Lock()
	recorder.responses = append(recorder.responses, protocol.PollResponse{})
	recorder.mutex.Unlock()
	for range 3 {
		if _, err := worker.poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if said := strings.Count(stderr.String(), "taking work again"); said != 1 {
		t.Fatalf("said the lifting %d times in %q, want once", said, stderr.String())
	}
}

func TestAnUnrefusedWorkerSaysNothingAboutLeases(t *testing.T) {
	recorder := &pollRecorder{responses: []protocol.PollResponse{{}}}
	server := recorder.serve(t)
	var stderr strings.Builder
	worker := fleetWorker(t, server, "workshop", &stderr)
	for range 3 {
		if _, err := worker.poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want silence when nothing is refusing", stderr.String())
	}
}

func TestANewRefusalIsAnnouncedEvenAfterAnother(t *testing.T) {
	recorder := &pollRecorder{responses: []protocol.PollResponse{
		{Refused: "fleet \"workshop\" holds no lease"},
		{Refused: "fleet \"workshop\" is stood down: owner is at the keyboard"},
	}}
	server := recorder.serve(t)
	var stderr strings.Builder
	worker := fleetWorker(t, server, "workshop", &stderr)
	for range 2 {
		if _, err := worker.poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(stderr.String(), "holds no lease") || !strings.Contains(stderr.String(), "stood down") {
		t.Fatalf("stderr = %q, want both refusals reported", stderr.String())
	}
}
