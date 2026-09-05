package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

// enabledCollectorAt writes a config for a collector rooted at directory and
// returns its path. Nothing in the directory is created — which of the files
// exist is what each test below is about.
func enabledCollectorAt(t *testing.T, directory string, extra string) string {
	t.Helper()
	return collectorSetup(t, `[collector]
enabled = true
listen = "127.0.0.1:0"
database = "`+filepath.Join(directory, "telemetry.db")+`"
token_file = "`+filepath.Join(directory, "ingest.token")+`"
identity_salt_file = "`+filepath.Join(directory, "identity.salt")+`"
`+extra)
}

// run invokes one collector verb and returns its exit code with both streams.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr, "test")
	return code, stdout.String(), stderr.String()
}

// started brings a collector up once so its secrets and database exist, then
// stops it. Every verb below acts on a deployment that has run, because that is
// the only kind there is anything to inspect, back up, or purge.
func started(t *testing.T, path string) {
	t.Helper()
	loaded := loadedCollector(t, path)
	startedCollector(t, loaded)
}

func TestDoctorReportsAHealthyCollector(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)

	code, stdout, stderr := run(t, "collector", "doctor", "--config", path)
	if code != 0 {
		t.Fatalf("doctor exit %d: %s%s", code, stdout, stderr)
	}
	report := decodedReport(t, stdout)
	if !report.Healthy {
		t.Fatalf("report = %+v", report)
	}
	for _, check := range report.Checks {
		if check.Status != "ok" {
			t.Errorf("%s: %s (%s)", check.Name, check.Status, check.Detail)
		}
	}
	if len(report.Checks) != 3 {
		t.Fatalf("checks = %+v", report.Checks)
	}
}

// Nothing is created by looking. A doctor that made the token it went looking
// for would report a healthy deployment it had just repaired, and would answer
// for a collector that has never started as though it had.
func TestDoctorCreatesNothingItDidNotFind(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")

	code, stdout, _ := run(t, "collector", "doctor", "--config", path)
	if code == 0 {
		t.Fatalf("doctor passed on a collector that has never run: %s", stdout)
	}
	report := decodedReport(t, stdout)
	if report.Healthy {
		t.Fatalf("report = %+v", report)
	}
	for _, name := range []string{"ingest.token", "identity.salt", "telemetry.db"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Errorf("doctor created %s", name)
		}
	}
}

// Every check runs. An operator repairing a collector wants the list of what is
// wrong, not one item of it per invocation.
func TestDoctorReportsEveryFailureNotTheFirst(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")

	_, stdout, _ := run(t, "collector", "doctor", "--config", path)
	report := decodedReport(t, stdout)
	failed := 0
	for _, check := range report.Checks {
		if check.Status == "failed" {
			failed++
		}
	}
	if failed != 3 {
		t.Fatalf("failed checks = %d in %+v", failed, report.Checks)
	}
}

// A secret file anything else on the machine can read is refused, not repaired.
// Widening it is something that happened; narrowing it silently would hide
// that, which is the fact the operator needs.
func TestDoctorRefusesAWidenedSecret(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)
	if err := os.Chmod(filepath.Join(directory, "ingest.token"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := run(t, "collector", "doctor", "--config", path)
	if code == 0 {
		t.Fatalf("doctor passed a world-readable token: %s", stdout)
	}
	if !strings.Contains(stdout, "permissions") {
		t.Fatalf("report = %s", stdout)
	}
}

func TestDoctorRefusesWhenTheCollectorIsNotEnabled(t *testing.T) {
	path := collectorSetup(t, "[collector]\nenabled = false\n")
	code, _, stderr := run(t, "collector", "doctor", "--config", path)
	if code == 0 || !strings.Contains(stderr, "not enabled") {
		t.Fatalf("exit %d, stderr = %q", code, stderr)
	}
}

func TestBackupWritesAReadableCopy(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)
	destination := filepath.Join(t.TempDir(), "telemetry-backup.db")

	code, stdout, stderr := run(t, "collector", "backup", "--config", path, "--output", destination)
	if code != 0 {
		t.Fatalf("backup exit %d: %s%s", code, stdout, stderr)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the backup is readable by others: %s", info.Mode())
	}
	if info.Size() == 0 {
		t.Error("the backup is empty")
	}
	// A staging directory left behind would accumulate a copy of the database
	// per backup, which is the opposite of what a backup is for.
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("backup left %d entries behind: %v", len(entries), entries)
	}
}

// Destroying an earlier backup is the one thing a backup must never do.
func TestBackupRefusesToReplaceOne(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)
	destination := filepath.Join(t.TempDir(), "telemetry-backup.db")
	if err := os.WriteFile(destination, []byte("an earlier backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := run(t, "collector", "backup", "--config", path, "--output", destination)
	if code == 0 {
		t.Fatal("backup overwrote an existing file")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("stderr = %q", stderr)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "an earlier backup" {
		t.Fatalf("the earlier backup was changed: %q", contents)
	}
}

// Opening the way the collector does would create an empty database on a typo
// in the path, and then backup would archive nothing and report success.
func TestBackupRefusesADatabaseThatDoesNotExist(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")

	code, _, stderr := run(t, "collector", "backup", "--config", path, "--output", filepath.Join(t.TempDir(), "backup.db"))
	if code == 0 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("exit %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(directory, "telemetry.db")); !os.IsNotExist(err) {
		t.Error("backup created the database it was asked to copy")
	}
}

func TestBackupRequiresAnOutput(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	code, _, stderr := run(t, "collector", "backup", "--config", path)
	if code == 0 || !strings.Contains(stderr, "--output is required") {
		t.Fatalf("exit %d, stderr = %q", code, stderr)
	}
}

func TestPurgeRequiresItsConfirmation(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)

	code, _, stderr := run(t, "collector", "purge", "--config", path, "--before", "2026-01-01T00:00:00Z")
	if code == 0 || !strings.Contains(stderr, "--confirm-delete-raw-events") {
		t.Fatalf("exit %d, stderr = %q", code, stderr)
	}
}

// Every observed_at is UTC. Reading a zoneless cutoff as local time would, east
// of Greenwich, delete hours of events that have not expired, and nothing
// afterwards would show it.
func TestPurgeRefusesACutoffWithoutAZone(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)

	for _, cutoff := range []string{"2026-01-01T00:00:00", "2026-01-01", "yesterday", ""} {
		arguments := []string{"collector", "purge", "--config", path, "--confirm-delete-raw-events"}
		if cutoff != "" {
			arguments = append(arguments, "--before", cutoff)
		}
		code, _, stderr := run(t, arguments...)
		if code == 0 {
			t.Errorf("purge accepted --before %q", cutoff)
		}
		if !strings.Contains(stderr, "--before") {
			t.Errorf("--before %q: stderr = %q", cutoff, stderr)
		}
	}
}

func TestPurgeDeletesNothingBeforeTheFirstEvent(t *testing.T) {
	directory := t.TempDir()
	path := enabledCollectorAt(t, directory, "")
	started(t, path)

	code, stdout, stderr := run(t, "collector", "purge", "--config", path,
		"--before", "2000-01-01T00:00:00Z", "--confirm-delete-raw-events")
	if code != 0 {
		t.Fatalf("purge exit %d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "deleted 0 raw telemetry event(s)") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// loadedCollector reads the [collector] section the way the verbs do.
func loadedCollector(t *testing.T, path string) config.Collector {
	t.Helper()
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Collector
}

func decodedReport(t *testing.T, stdout string) collectorReport {
	t.Helper()
	var report collectorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report %q: %v", stdout, err)
	}
	return report
}
