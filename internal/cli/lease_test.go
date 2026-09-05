package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/controlplane"
)

// leaseControlPlane starts a real control plane and returns a worker config
// pointed at it. The lease verbs are tested against the store they actually
// write to, because a lease that the CLI reports as set and the control plane
// never enforces is the exact failure this whole mechanism exists to prevent.
func leaseControlPlane(t *testing.T, requireFleetLease bool) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("Plan:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	definition := "[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := controlplane.OpenStore(filepath.Join(directory, "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, err := controlplane.NewServerWithOptions(store, definitionPath, "secret", 0,
		controlplane.ServerOptions{RequireFleetLease: requireFleetLease})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	workerDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(workerDirectory, "token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(workerDirectory, "worker.toml")
	body := "[control_plane]\nurl = " + strconv.Quote(web.URL) + "\ntoken_file = \"token\"\n"
	if err := os.WriteFile(workerPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return workerPath
}

func runLease(t *testing.T, workerPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), append([]string{"lease", "--config", workerPath}, args...),
		strings.NewReader(""), &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), code
}

func TestStandingAFleetDownIsVisibleInTheListing(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	stdout, stderr, code := runLease(t, workerPath,
		"stand-down", "--fleet", "workshop", "--reason", "owner is at the keyboard", "--for", "8h")
	if code != 0 {
		t.Fatalf("stand-down exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "stood down") || !strings.Contains(stdout, "owner is at the keyboard") {
		t.Fatalf("stand-down said %q", stdout)
	}
	listed, stderr, code := runLease(t, workerPath, "list")
	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(listed, "workshop") || !strings.Contains(listed, "stood-down") {
		t.Fatalf("listing = %q", listed)
	}
	// The column an operator actually reads is whether the fleet is taking
	// work, so it must not say yes for a fleet that has been stood down.
	for _, line := range strings.Split(listed, "\n") {
		if strings.HasPrefix(line, "workshop") && !strings.Contains(line, "no") {
			t.Fatalf("stood-down fleet listed as taking work: %q", line)
		}
	}
}

func TestAllowingAFleetLetsItTakeWorkAgain(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	if _, stderr, code := runLease(t, workerPath,
		"stand-down", "--fleet", "workshop", "--reason", "owner is at the keyboard", "--for", "8h"); code != 0 {
		t.Fatalf("stand-down exit = %d, stderr = %q", code, stderr)
	}
	stdout, stderr, code := runLease(t, workerPath,
		"allow", "--fleet", "workshop", "--reason", "owner has gone out", "--for", "2h")
	if code != 0 {
		t.Fatalf("allow exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "taking work") {
		t.Fatalf("allow said %q", stdout)
	}
	listed, _, _ := runLease(t, workerPath, "list")
	for _, line := range strings.Split(listed, "\n") {
		if strings.HasPrefix(line, "workshop") && !strings.Contains(line, "yes") {
			t.Fatalf("allowed fleet not listed as taking work: %q", line)
		}
	}
}

func TestALeaseSetByTheCLIIsTheOneThePollHonours(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	if _, stderr, code := runLease(t, workerPath,
		"stand-down", "--fleet", "workshop", "--reason", "owner is at the keyboard", "--for", "8h"); code != 0 {
		t.Fatalf("stand-down exit = %d, stderr = %q", code, stderr)
	}
	endpoint := leaseEndpoint(t, workerPath)
	response := postLeaseJSON(t, endpoint+"/api/v1/workers/poll", `{"instance_id":"worker-a","name":"w","fleet":"workshop","executors":["test"],"repositories":["machinist"]}`)
	defer response.Body.Close()
	body := readAll(t, response)
	if !strings.Contains(body, "owner is at the keyboard") {
		t.Fatalf("poll response = %q, want the lease the CLI set to be the one refusing", body)
	}
}

func TestTheCLIReportsTheStoredLeaseRatherThanWhatItSent(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	stdout, stderr, code := runLease(t, workerPath,
		"stand-down", "--fleet", "  workshop  ", "--reason", "  owner is at the keyboard  ", "--for", "8h")
	if code != 0 {
		t.Fatalf("stand-down exit = %d, stderr = %q", code, stderr)
	}
	// The store keeps "workshop", so that is the name a worker's poll will be
	// matched against and the name the operator must be shown.
	if strings.HasPrefix(stdout, " ") || !strings.HasPrefix(stdout, "workshop:") {
		t.Fatalf("stand-down said %q, want the stored fleet name", stdout)
	}
	if strings.Contains(stdout, "  owner") {
		t.Fatalf("stand-down echoed the request instead of the stored reason: %q", stdout)
	}
}

func TestListingSaysWhenNothingIsBeingEnforced(t *testing.T) {
	workerPath := leaseControlPlane(t, false)
	if _, stderr, code := runLease(t, workerPath,
		"allow", "--fleet", "workshop", "--reason", "on shift", "--for", "8h"); code != 0 {
		t.Fatalf("allow exit = %d, stderr = %q", code, stderr)
	}
	listed, _, _ := runLease(t, workerPath, "list")
	// A listing of allowed fleets reads as a fleet under control. It is not one
	// unless the control plane is holding anyone to it.
	if !strings.Contains(listed, "fleet leasing is off") {
		t.Fatalf("listing = %q, want it to say leasing is not enforced", listed)
	}
}

func TestAnEmptyListingIsNotAnEmptyTable(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	listed, stderr, code := runLease(t, workerPath, "list")
	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(listed, "no fleet leases") {
		t.Fatalf("listing = %q", listed)
	}
}

func TestALeaseWithNoEndIsRefused(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	for name, window := range map[string]string{"none": "0s", "backwards": "-1h", "not a duration": "forever"} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runLease(t, workerPath,
				"allow", "--fleet", "workshop", "--reason", "on shift", "--for", window)
			if code == 0 {
				t.Fatalf("a lease lasting %q was accepted", window)
			}
			if !strings.Contains(stderr, "--for") {
				t.Fatalf("stderr = %q, want it to name the flag", stderr)
			}
		})
	}
}

func TestEveryLeaseDecisionNeedsAFleetAReasonAndAnEnd(t *testing.T) {
	workerPath := leaseControlPlane(t, true)
	full := map[string]string{"--fleet": "workshop", "--reason": "on shift", "--for": "8h"}
	for missing := range full {
		t.Run(strings.TrimPrefix(missing, "--"), func(t *testing.T) {
			args := []string{"allow"}
			for flag, value := range full {
				if flag != missing {
					args = append(args, flag, value)
				}
			}
			_, stderr, code := runLease(t, workerPath, args...)
			if code == 0 {
				t.Fatalf("a lease with no %s was accepted (stderr %q)", missing, stderr)
			}
			// Refused by the CLI, before a half-specified decision is sent
			// anywhere: the operator is told which flag they left out, not
			// handed the control plane's opinion of the result.
			if !strings.Contains(stderr, "required flag") || !strings.Contains(stderr, strings.TrimPrefix(missing, "--")) {
				t.Fatalf("stderr = %q, want it to name the missing flag %s", stderr, missing)
			}
		})
	}
}

func leaseEndpoint(t *testing.T, workerPath string) string {
	t.Helper()
	body, err := os.ReadFile(workerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if rest, found := strings.CutPrefix(line, "url = "); found {
			url, err := strconv.Unquote(strings.TrimSpace(rest))
			if err != nil {
				t.Fatal(err)
			}
			return url
		}
	}
	t.Fatalf("no control plane URL in %s", workerPath)
	return ""
}

func postLeaseJSON(t *testing.T, endpoint, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readAll(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// startedControlPlane runs `machinist start` against the given config and
// returns the address it reported listening on. Fleet leasing is only real if
// the setting in a file reaches the dispatch path, and the only way to know
// that is to start the thing the way an operator does.
func startedControlPlane(t *testing.T, configPath string) string {
	t.Helper()
	listening := make(chan string, 1)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	stderr := &addressWriter{reported: listening}
	done := make(chan int, 1)
	go func() {
		done <- Execute(ctx, []string{"start", "--config=" + configPath, "--listen=127.0.0.1:0"},
			strings.NewReader(""), &bytes.Buffer{}, stderr, "test")
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("control plane did not stop")
		}
	})
	select {
	case address := <-listening:
		return address
	case <-time.After(5 * time.Second):
		t.Fatal("control plane did not report listening")
		return ""
	}
}

type addressWriter struct {
	mutex    sync.Mutex
	body     strings.Builder
	reported chan string
	sent     bool
}

func (w *addressWriter) Write(chunk []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.body.Write(chunk)
	const prefix = "machinist: control plane listening on http://"
	text := w.body.String()
	if !w.sent && strings.Contains(text, prefix) {
		rest := text[strings.Index(text, prefix)+len(prefix):]
		if line, _, found := strings.Cut(rest, "\n"); found {
			w.sent = true
			w.reported <- "http://" + line
		}
	}
	return len(chunk), nil
}

func writeLeasingStartConfig(t *testing.T, requireFleetLease bool) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "worker.token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "[server]\ndatabase = \"machinist.db\"\nworker_token_file = \"worker.token\"\n" +
		"require_fleet_lease = " + strconv.FormatBool(requireFleetLease) + "\n"
	configPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestTheRequireFleetLeaseSettingReachesTheDispatchPath(t *testing.T) {
	endpoint := startedControlPlane(t, writeLeasingStartConfig(t, true))
	response := postLeaseJSON(t, endpoint+"/api/v1/workers/poll",
		`{"instance_id":"worker-a","name":"w","fleet":"workshop","executors":["test"],"repositories":["machinist"]}`)
	defer response.Body.Close()
	if body := readAll(t, response); !strings.Contains(body, "holds no lease") {
		t.Fatalf("poll = %q, want the configured setting to hold the fleet down", body)
	}
}

func TestLeavingTheSettingOutChangesNothing(t *testing.T) {
	endpoint := startedControlPlane(t, writeLeasingStartConfig(t, false))
	response := postLeaseJSON(t, endpoint+"/api/v1/workers/poll",
		`{"instance_id":"worker-a","name":"w","fleet":"workshop","executors":["test"],"repositories":["machinist"]}`)
	defer response.Body.Close()
	if body := readAll(t, response); strings.Contains(body, "lease") {
		t.Fatalf("poll = %q, want no leasing when the setting is off", body)
	}
}
