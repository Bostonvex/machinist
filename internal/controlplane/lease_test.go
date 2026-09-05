package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// leaseStore opens a store and enqueues one job, so that the only thing
// standing between a poll and a run is the lease under test.
func leaseStore(t *testing.T) *Store {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement",
		testAgent("implement", "implement request")); err != nil {
		t.Fatal(err)
	}
	return store
}

// leasedPoll polls with fleet leasing required.
func leasedPoll(t *testing.T, store *Store, fleet string) (*protocol.RunSpec, error) {
	t.Helper()
	request := pollRequest("worker-a", []string{"codex"}, []string{"machinist"})
	request.Fleet = fleet
	return store.poll(t.Context(), request, 0, true)
}

func allowFleet(t *testing.T, store *Store, fleet string, until time.Duration) {
	t.Helper()
	if err := store.SetLease(t.Context(), Lease{
		Fleet: fleet, State: LeaseAllowed, ExpiresAt: time.Now().Add(until), Reason: "on shift",
	}); err != nil {
		t.Fatal(err)
	}
}

// refusal insists the error is a refusal and returns its sentence. A refusal
// that arrives as some other error would let a fleet be stood down and have the
// worker report the control plane as broken.
func refusal(t *testing.T, err error) string {
	t.Helper()
	var refused *ErrFleetRefused
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want an *ErrFleetRefused", err)
	}
	return refused.Error()
}

func TestAFleetWithNoLeaseIsRefused(t *testing.T) {
	store := leaseStore(t)
	run, err := leasedPoll(t, store, "workshop")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	if message := refusal(t, err); !strings.Contains(message, `fleet "workshop" holds no lease`) {
		t.Fatalf("refusal = %q", message)
	}
}

func TestAStoodDownFleetIsRefusedWithItsReason(t *testing.T) {
	store := leaseStore(t)
	if err := store.SetLease(t.Context(), Lease{
		Fleet: "workshop", State: LeaseStoodDown, ExpiresAt: time.Now().Add(time.Hour),
		Reason: "owner is at the keyboard",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := leasedPoll(t, store, "workshop")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	message := refusal(t, err)
	if !strings.Contains(message, "stood down") || !strings.Contains(message, "owner is at the keyboard") {
		t.Fatalf("refusal = %q, want the operator's reason", message)
	}
}

func TestAnExpiredAllowedLeaseIsRefused(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", -time.Minute)
	run, err := leasedPoll(t, store, "workshop")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	if message := refusal(t, err); !strings.Contains(message, "expired at") {
		t.Fatalf("refusal = %q, want an expiry", message)
	}
}

func TestAnUnreadableLeaseStateIsAnErrorNotPermission(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", time.Hour)
	if _, err := store.db.Exec(`UPDATE fleet_leases SET state='probably-fine' WHERE fleet='workshop'`); err != nil {
		t.Fatal(err)
	}
	run, err := leasedPoll(t, store, "workshop")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	var refused *ErrFleetRefused
	if errors.As(err, &refused) {
		t.Fatalf("unreadable state reported as a refusal (%v); it is a fault, not a decision", err)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown lease state") {
		t.Fatalf("error = %v, want an unreadable state", err)
	}
}

func TestAnUnreadableLeaseExpiryIsAnErrorNotPermission(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", time.Hour)
	if _, err := store.db.Exec(`UPDATE fleet_leases SET expires_at='whenever' WHERE fleet='workshop'`); err != nil {
		t.Fatal(err)
	}
	run, err := leasedPoll(t, store, "workshop")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	if err == nil || !strings.Contains(err.Error(), "unreadable lease expiry") {
		t.Fatalf("error = %v, want an unreadable expiry", err)
	}
}

func TestAWorkerWithNoFleetIsRefusedWhenLeasingIsRequired(t *testing.T) {
	store := leaseStore(t)
	run, err := leasedPoll(t, store, "")
	if run != nil {
		t.Fatalf("run = %#v, want no work offered", run)
	}
	if message := refusal(t, err); !strings.Contains(message, "did not say which fleet") {
		t.Fatalf("refusal = %q", message)
	}
}

func TestAnAllowedFleetIsOfferedWork(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", time.Hour)
	run, err := leasedPoll(t, store, "workshop")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("an allowed fleet was offered no work")
	}
}

func TestLeasingOffOffersWorkToAFleetWithNoLease(t *testing.T) {
	store := leaseStore(t)
	request := pollRequest("worker-a", []string{"codex"}, []string{"machinist"})
	request.Fleet = "workshop"
	run, err := store.poll(t.Context(), request, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("leasing is off and work was still withheld")
	}
}

func TestARefusedFleetStaysVisibleInTheRoster(t *testing.T) {
	store := leaseStore(t)
	if _, err := leasedPoll(t, store, "workshop"); err == nil {
		t.Fatal("poll was not refused")
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || snapshot.Workers[0].InstanceID != "worker-a" {
		t.Fatalf("workers = %#v, want the refused host still on the roster", snapshot.Workers)
	}
}

func TestARunAlreadyInFlightIsStillReturnedToARefusedFleet(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", time.Hour)
	first, err := leasedPoll(t, store, "workshop")
	if err != nil || first == nil {
		t.Fatalf("first poll = %#v, %v", first, err)
	}
	if err := store.SetLease(t.Context(), Lease{
		Fleet: "workshop", State: LeaseStoodDown, ExpiresAt: time.Now().Add(time.Hour), Reason: "owner is back",
	}); err != nil {
		t.Fatal(err)
	}
	again, err := leasedPoll(t, store, "workshop")
	if err != nil {
		t.Fatalf("a run already in flight was refused: %v", err)
	}
	if again == nil || again.ID != first.ID {
		t.Fatalf("second poll = %#v, want run %s handed back to be finished", again, first.ID)
	}
}

func TestSetLeaseRequiresEveryFieldThatMakesItReadable(t *testing.T) {
	store := leaseStore(t)
	full := Lease{Fleet: "workshop", State: LeaseAllowed, ExpiresAt: time.Now().Add(time.Hour), Reason: "on shift"}
	for name, mangle := range map[string]func(Lease) Lease{
		"fleet":  func(l Lease) Lease { l.Fleet = "  "; return l },
		"state":  func(l Lease) Lease { l.State = "maybe"; return l },
		"expiry": func(l Lease) Lease { l.ExpiresAt = time.Time{}; return l },
		"reason": func(l Lease) Lease { l.Reason = " "; return l },
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.SetLease(t.Context(), mangle(full)); err == nil {
				t.Fatalf("a lease with no %s was accepted", name)
			}
		})
	}
	if err := store.SetLease(t.Context(), full); err != nil {
		t.Fatalf("a complete lease was refused: %v", err)
	}
}

func TestAnExpiredLeaseIsStillListed(t *testing.T) {
	store := leaseStore(t)
	allowFleet(t, store, "workshop", -time.Hour)
	allowFleet(t, store, "cluster", time.Hour)
	leases, err := store.Leases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 || leases[0].Fleet != "cluster" || leases[1].Fleet != "workshop" {
		t.Fatalf("leases = %#v, want both, sorted by fleet", leases)
	}
	if leases[1].Allows(time.Now()) {
		t.Fatal("an expired lease still allows work")
	}
}

func TestAllowsNeedsBothPermissionAndTime(t *testing.T) {
	now := time.Now()
	for name, testCase := range map[string]struct {
		lease Lease
		want  bool
	}{
		"allowed and current":      {Lease{State: LeaseAllowed, ExpiresAt: now.Add(time.Minute)}, true},
		"allowed but expired":      {Lease{State: LeaseAllowed, ExpiresAt: now.Add(-time.Minute)}, false},
		"stood down and unexpired": {Lease{State: LeaseStoodDown, ExpiresAt: now.Add(time.Minute)}, false},
		"stood down and expired":   {Lease{State: LeaseStoodDown, ExpiresAt: now.Add(-time.Minute)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := testCase.lease.Allows(now); got != testCase.want {
				t.Fatalf("Allows = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestParseLeaseStateNamesWhatItWouldAccept(t *testing.T) {
	if _, err := ParseLeaseState("paused"); err == nil {
		t.Fatal("an unknown state was accepted")
	} else if !strings.Contains(err.Error(), "allowed") || !strings.Contains(err.Error(), "stood-down") {
		t.Fatalf("error = %v, want it to name the states it accepts", err)
	}
	if state, err := ParseLeaseState("  stood-down "); err != nil || state != LeaseStoodDown {
		t.Fatalf("ParseLeaseState = %q, %v", state, err)
	}
}

// leaseServer starts a control plane over HTTP with fleet leasing on, and hands
// back the headers a browser submission needs.
func leaseServer(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	web, headers, _ := leaseServerWithStore(t)
	return web, headers
}

func leaseServerWithStore(t *testing.T) (*httptest.Server, map[string]string, *Store) {
	t.Helper()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("Plan this:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath, []byte("[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServerWithOptions(store, definitionPath, "secret", 0, ServerOptions{RequireFleetLease: true})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)
	return web, map[string]string{"Origin": web.URL, "X-Machinist-CSRF": server.csrfToken}, store
}

type leaseListing struct {
	Leases   []leaseView `json:"leases"`
	Required bool        `json:"required"`
}

func listLeases(t *testing.T, web *httptest.Server) leaseListing {
	t.Helper()
	response, err := http.Get(web.URL + "/api/v1/leases")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var listing leaseListing
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	return listing
}

func decodeLease(t *testing.T, response *http.Response) leaseView {
	t.Helper()
	defer response.Body.Close()
	var view leaseView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestALeaseCanBeSetAndReadBack(t *testing.T) {
	web, headers := leaseServer(t)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	written := postJSON(t, web.URL+"/api/v1/leases", map[string]string{
		"fleet": "workshop", "state": "allowed", "expires_at": expires, "reason": "on shift",
	}, headers)
	if written.StatusCode != http.StatusOK {
		t.Fatalf("write status = %d", written.StatusCode)
	}
	if view := decodeLease(t, written); !view.Allowed || !view.Required || view.Fleet != "workshop" {
		t.Fatalf("written lease = %#v", view)
	}
	listing := listLeases(t, web)
	if !listing.Required || len(listing.Leases) != 1 || listing.Leases[0].Fleet != "workshop" || !listing.Leases[0].Allowed {
		t.Fatalf("listing = %#v", listing)
	}
}

func TestAnExpiredLeaseIsListedAsNotAllowed(t *testing.T) {
	web, headers := leaseServer(t)
	expires := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	written := postJSON(t, web.URL+"/api/v1/leases", map[string]string{
		"fleet": "workshop", "state": "allowed", "expires_at": expires, "reason": "yesterday's shift",
	}, headers)
	if written.StatusCode != http.StatusOK {
		t.Fatalf("write status = %d", written.StatusCode)
	}
	if view := decodeLease(t, written); view.Allowed {
		t.Fatal("the write echoed an already-expired lease as allowing work")
	}
	for _, listed := range listLeases(t, web).Leases {
		if listed.Fleet == "workshop" && listed.Allowed {
			t.Fatalf("the listing reported an expired lease as allowing work: %#v", listed)
		}
	}
}

func TestWritingALeaseNeedsMoreThanReachingThePort(t *testing.T) {
	web, _ := leaseServer(t)
	body := func() map[string]string {
		return map[string]string{"fleet": "workshop", "state": "allowed",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "reason": "let me in"}
	}
	for name, testCase := range map[string]struct {
		headers map[string]string
		want    int
	}{
		"no credentials at all": {nil, http.StatusForbidden},
		"a guessed token":       {map[string]string{"Authorization": "Bearer guessed"}, http.StatusUnauthorized},
		"another site's page": {map[string]string{
			"Origin": "https://evil.example", "X-Machinist-CSRF": "guessed"}, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			response := postJSON(t, web.URL+"/api/v1/leases", body(), testCase.headers)
			defer response.Body.Close()
			if response.StatusCode != testCase.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, testCase.want)
			}
		})
	}
	// The worker token is what the CLI carries, so it must be enough.
	accepted := postJSON(t, web.URL+"/api/v1/leases", body(), map[string]string{"Authorization": "Bearer secret"})
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("token-authenticated write status = %d", accepted.StatusCode)
	}
}

func TestALeaseTheControlPlaneCannotReadIsRefusedAtTheEdge(t *testing.T) {
	web, headers := leaseServer(t)
	for name, body := range map[string]map[string]string{
		"unknown state": {"fleet": "workshop", "state": "paused",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "reason": "why"},
		"unreadable expiry": {"fleet": "workshop", "state": "allowed", "expires_at": "tomorrow", "reason": "why"},
		"no reason":         {"fleet": "workshop", "state": "allowed", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
	} {
		t.Run(name, func(t *testing.T) {
			response := postJSON(t, web.URL+"/api/v1/leases", body, headers)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
		})
	}
}

func TestAFailedLeaseWriteIsNotBlamedOnTheOperator(t *testing.T) {
	web, headers, store := leaseServerWithStore(t)
	// A lease that is perfectly well formed, written to a store that can no
	// longer take it. Nothing about the request is wrong.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	response := postJSON(t, web.URL+"/api/v1/leases", map[string]string{
		"fleet": "workshop", "state": "allowed",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "reason": "on shift",
	}, headers)
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: a failed write is not the operator's mistake", response.StatusCode)
	}
}

func TestAPollRefusedByALeaseSaysSoRatherThanLookingEmpty(t *testing.T) {
	web, headers := leaseServer(t)
	written := postJSON(t, web.URL+"/api/v1/leases", map[string]string{
		"fleet": "workshop", "state": "stood-down",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "reason": "owner is at the keyboard",
	}, headers)
	written.Body.Close()
	response := postJSON(t, web.URL+"/api/v1/workers/poll", map[string]any{
		"instance_id": "worker-a", "name": "test-worker", "fleet": "workshop",
		"executors": []string{"test"}, "repositories": []string{"machinist"},
	}, map[string]string{"Authorization": "Bearer secret"})
	defer response.Body.Close()
	// A refusal is not a failure: the worker is behaving correctly and is being
	// told not to take work, so an error status would have it report the control
	// plane as broken.
	if response.StatusCode != http.StatusOK {
		t.Fatalf("refused poll status = %d, want 200", response.StatusCode)
	}
	var poll protocol.PollResponse
	if err := json.NewDecoder(response.Body).Decode(&poll); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(poll.Refused, "owner is at the keyboard") {
		t.Fatalf("refused = %q, want the operator's reason", poll.Refused)
	}
	if poll.Run != nil {
		t.Fatalf("a stood-down fleet was handed run %#v", poll.Run)
	}
}
