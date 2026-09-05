package controlplane

import (
	"context"
	"database/sql"

	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/environment"
	"github.com/owainlewis/machinist/internal/factoryrun"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestHerdrJobsOnlyDispatchToHerdrWorkersAndExposeTerminalBinding(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := config.ResolvedCommand{Name: "implement", Executor: "codex", Prompt: "work", Timeout: time.Minute, Hash: "hash"}
	jobID, err := store.CreateJobWithOptions(t.Context(), "work", "machinist", "implement", command, CreateJobOptions{ExecutionMode: "herdr", Origin: "herdr-plugin"})
	if err != nil {
		t.Fatal(err)
	}
	base := protocol.PollRequest{InstanceID: "process-worker", Name: "mac-mini", Executors: []string{"codex"}, Repositories: []string{"machinist"}, Transports: []string{"process"}}
	if run, err := store.Poll(t.Context(), base); err != nil || run != nil {
		t.Fatalf("process poll run=%#v err=%v", run, err)
	}
	herdrPoll := base
	herdrPoll.InstanceID = "herdr-worker"
	herdrPoll.Transports = []string{"herdr"}
	run, err := store.Poll(t.Context(), herdrPoll)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.ExecutionMode != "herdr" || run.Origin != "herdr-plugin" || run.JobID != jobID {
		t.Fatalf("run = %#v", run)
	}
	binding := protocol.TerminalBinding{Session: "machinist", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", AgentName: "machinist_attempt"}
	stale := protocol.BindTerminalRequest{InstanceID: herdrPoll.InstanceID, LeaseToken: "stale-lease", AttemptID: run.AttemptID, Terminal: binding}
	if err := store.BindTerminal(t.Context(), run.ID, stale); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale terminal binding error = %v", err)
	}
	if err := store.BindTerminal(t.Context(), run.ID, protocol.BindTerminalRequest{InstanceID: herdrPoll.InstanceID, LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, Terminal: binding}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.Jobs[0]
	if stored.ExecutionMode != "herdr" || stored.Origin != "herdr-plugin" || len(stored.Runs) != 1 || len(stored.Runs[0].Attempts) != 1 || stored.Runs[0].Attempts[0].Terminal == nil || *stored.Runs[0].Attempts[0].Terminal != binding {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestStoreRejectsContradictoryOutcomes(t *testing.T) {
	for _, test := range []struct {
		state    string
		exitCode int
	}{
		{state: "succeeded", exitCode: 1},
		{state: "failed", exitCode: 0},
		{state: "timed_out", exitCode: 1},
		{state: "cancelled", exitCode: 1},
	} {
		t.Run(test.state, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
			if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
				t.Fatal(err)
			}
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
			if err != nil {
				t.Fatal(err)
			}
			err = store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: test.state, ExitCode: test.exitCode})
			if err == nil {
				t.Fatal("expected contradictory outcome rejection")
			}
			snapshot, snapshotErr := store.Snapshot(t.Context())
			if snapshotErr != nil || snapshot.Jobs[0].Runs[0].State != "running" {
				t.Fatalf("snapshot = %#v, %v", snapshot, snapshotErr)
			}
		})
	}
}

func TestOpenStoreReplacesLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY, selection_kind TEXT); INSERT INTO jobs VALUES('legacy','pipeline')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count, version int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if count != 0 || version != schemaVersion {
		t.Fatalf("migrated database count=%d version=%d", count, version)
	}
}

func TestOpenStoreRejectsNewerSchemaWithoutDeletingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// One past the current schema, so this fixture keeps meaning "newer than we
	// know how to read" instead of quietly becoming the version we do read.
	future := fmt.Sprintf("CREATE TABLE future_data(value TEXT); INSERT INTO future_data VALUES('preserved'); PRAGMA user_version=%d;", schemaVersion+1)
	if _, err := db.Exec(future); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		if store != nil {
			store.Close()
		}
		t.Fatalf("open newer schema error = %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM future_data`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("future data = %q, %v", value, err)
	}
}

func TestOpenStoreUpgradesVersionTwoWorkerCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE workers (instance_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen_at TEXT NOT NULL);
INSERT INTO workers VALUES('legacy','legacy-worker','2026-01-01T00:00:00Z');
PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	var environmentJSON, profilesJSON string
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT environment_json,profiles_json FROM workers WHERE instance_id='legacy'`).Scan(&environmentJSON, &profilesJSON); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || environmentJSON != "{}" || profilesJSON != "{}" {
		t.Fatalf("version=%d environment=%q profiles=%q", version, environmentJSON, profilesJSON)
	}
}

func TestOpenStoreUpgradesVersionFourWithoutLosingJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(t.Context(), "preserve me", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	versionFour, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := versionFour.Exec(`ALTER TABLE runs DROP COLUMN cancel_requested; PRAGMA user_version=4;`); err != nil {
		versionFour.Close()
		t.Fatal(err)
	}
	if err := versionFour.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version, cancellationColumns, cancelRequested int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name='cancel_requested'`).Scan(&cancellationColumns); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT cancel_requested FROM runs WHERE job_id=?`, jobID).Scan(&cancelRequested); err != nil {
		t.Fatal(err)
	}
	snapshot, err := upgraded.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || cancellationColumns != 1 || cancelRequested != 0 || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID {
		t.Fatalf("version=%d columns=%d cancel=%d jobs=%#v", version, cancellationColumns, cancelRequested, snapshot.Jobs)
	}
}

func TestOpenStoreUpgradesVersionFiveLegacyAttemptBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(t.Context(), "recover once", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runs SET max_attempts=1 WHERE job_id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version, maxAttempts int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT max_attempts FROM runs WHERE job_id=?`, jobID).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || maxAttempts != defaultLegacyMaxAttempts {
		t.Fatalf("version=%d max_attempts=%d", version, maxAttempts)
	}
}

func TestOpenStoreUpgradesVersionSixTokenBudgetWithoutLosingJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(t.Context(), "preserve token policy", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	versionSix, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := versionSix.Exec(`ALTER TABLE runs DROP COLUMN max_total_tokens; PRAGMA user_version=6;`); err != nil {
		versionSix.Close()
		t.Fatal(err)
	}
	if err := versionSix.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version, budgetColumns int
	var maxTotalTokens int64
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name='max_total_tokens'`).Scan(&budgetColumns); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT max_total_tokens FROM runs WHERE job_id=?`, jobID).Scan(&maxTotalTokens); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || budgetColumns != 1 || maxTotalTokens != 0 {
		t.Fatalf("version=%d columns=%d max_total_tokens=%d", version, budgetColumns, maxTotalTokens)
	}
}

func TestPollPersistsWorkerEnvironmentAndProfiles(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	request := pollRequest("worker-a", []string{"local"}, []string{"machinist"})
	request.Environment = environment.Detect([]string{"trusted"})
	request.Profiles = map[string]protocol.ProfileCapability{
		"local": {Harness: "opencode", Provider: "openai_compatible", AuthMode: "local", Models: []string{"coder"}, Available: true},
	}
	if run, err := store.Poll(t.Context(), request); err != nil || run != nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || snapshot.Workers[0].Environment.Digest != request.Environment.Digest || !snapshot.Workers[0].Profiles["local"].Available {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
}

func TestPollSelectsFirstCompatibleRouteProfile(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"dgx-local", "codex-subscription", "deepseek"},
		MaxAttempts: 3, MaxTotalTokens: 100_000, FallbackOn: []string{"capacity", "rate_limit"}, Role: "implementer",
		Model: "coder", Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	incompatible := pollRequest("worker-a", []string{"codex-subscription"}, []string{"machinist"})
	incompatible.Models = map[string][]string{"codex-subscription": {"deep"}}
	if run, err := store.Poll(t.Context(), incompatible); err != nil || run != nil {
		t.Fatalf("incompatible poll = %#v, %v", run, err)
	}
	request := pollRequest("worker-b", []string{"deepseek", "dgx-local"}, []string{"machinist"})
	request.Models = map[string][]string{"deepseek": {"coder"}, "dgx-local": {"coder"}}
	request.Profiles = map[string]protocol.ProfileCapability{
		"dgx-local": {Harness: "opencode", Provider: "openai_compatible", AuthMode: "local", Available: true},
		"deepseek":  {Harness: "pi", Provider: "deepseek", AuthMode: "api_key", Available: true},
	}
	run, err := store.Poll(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Executor != "dgx-local" || run.Profile != "dgx-local" || run.Route != "implementation" || run.Harness != "opencode" || run.Provider != "openai_compatible" || run.AuthMode != "local" || run.Role != "implementer" || run.MaxAttempts != 3 || run.MaxTotalTokens != 100_000 {
		t.Fatalf("run = %#v", run)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.Jobs[0].Runs[0]
	if stored.Profile != "dgx-local" || stored.Route != "implementation" || stored.Harness != "opencode" || stored.Provider != "openai_compatible" || stored.Role != "implementer" || stored.MaxTotalTokens != 100_000 {
		t.Fatalf("stored run = %#v", stored)
	}
}

func TestPollDefersLowerPriorityRouteToConnectedPreferredWorker(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	local := pollRequest("worker-local", []string{"local"}, []string{"machinist"})
	local.Profiles = map[string]protocol.ProfileCapability{
		"local": {Harness: "deepseek", Provider: "openai_compatible", AuthMode: "local", Available: true},
	}
	if run, err := store.Poll(t.Context(), local); err != nil || run != nil {
		t.Fatalf("register preferred worker = %#v, %v", run, err)
	}

	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"local", "api"},
		MaxAttempts: 2, Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	api := pollRequest("worker-api", []string{"api"}, []string{"machinist"})
	api.Profiles = map[string]protocol.ProfileCapability{
		"api": {Harness: "codex", Provider: "openai", AuthMode: "api_key", Available: true},
	}
	if run, err := store.Poll(t.Context(), api); err != nil || run != nil {
		t.Fatalf("lower-priority poll = %#v, %v", run, err)
	}
	run, err := store.Poll(t.Context(), local)
	if err != nil || run == nil || run.Profile != "local" || run.Harness != "deepseek" {
		t.Fatalf("preferred poll = %#v, %v", run, err)
	}
}

func TestPollUsesLowerPriorityRouteWhilePreferredWorkerIsBusy(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	api := pollRequest("worker-api", []string{"api"}, []string{"machinist"})
	api.Profiles = map[string]protocol.ProfileCapability{
		"api": {Harness: "codex", Provider: "openai", AuthMode: "api_key", Available: true},
	}
	if run, err := store.Poll(t.Context(), api); err != nil || run != nil {
		t.Fatalf("register fallback worker = %#v, %v", run, err)
	}

	busyCommand := config.ResolvedCommand{Name: "occupy", Executor: "local", Prompt: "long task", Timeout: time.Minute}
	if _, err := store.CreateJob(t.Context(), "occupy local", "machinist", "occupy", busyCommand); err != nil {
		t.Fatal(err)
	}
	local := pollRequest("worker-local", []string{"local"}, []string{"machinist"})
	local.Profiles = map[string]protocol.ProfileCapability{
		"local": {Harness: "deepseek", Provider: "openai_compatible", AuthMode: "local", Available: true},
	}
	occupied, err := store.Poll(t.Context(), local)
	if err != nil || occupied == nil || occupied.Executor != "local" {
		t.Fatalf("occupy preferred worker = %#v, %v", occupied, err)
	}

	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"local", "api"},
		MaxAttempts: 2, Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), api)
	if err != nil || run == nil || run.Profile != "api" || run.Harness != "codex" {
		t.Fatalf("fallback poll = %#v, %v", run, err)
	}
}

func TestRetryCreatesFencedAttemptAndFallsBack(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"local", "subscription"},
		MaxAttempts: 2, FallbackOn: []string{"harness_crash"}, Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	request := pollRequest("worker-a", []string{"local", "subscription"}, []string{"machinist"})
	first, err := store.Poll(t.Context(), request)
	if err != nil || first == nil {
		t.Fatalf("first poll = %#v, %v", first, err)
	}
	if first.Profile != "local" || first.AttemptNumber != 1 || first.AttemptID == "" {
		t.Fatalf("first attempt = %#v", first)
	}
	firstCompletion := protocol.Completion{
		InstanceID: "worker-a", LeaseToken: first.LeaseToken, AttemptID: first.AttemptID,
		State: "failed", ExitCode: 1, Error: "harness exited", ErrorClass: "harness_crash",
		Result: []byte(`{"duration_millis":10,"token_usage":20}`),
	}
	if err := store.Complete(t.Context(), first.ID, firstCompletion); err != nil {
		t.Fatal(err)
	}
	second, err := store.Poll(t.Context(), request)
	if err != nil || second == nil {
		t.Fatalf("second poll = %#v, %v", second, err)
	}
	if second.Profile != "subscription" || second.AttemptNumber != 2 || second.AttemptID == "" || second.AttemptID == first.AttemptID {
		t.Fatalf("second attempt = %#v", second)
	}
	if second.PreviousErrorClass != "harness_crash" {
		t.Fatalf("retry handoff error class = %q", second.PreviousErrorClass)
	}
	if err := store.Complete(t.Context(), first.ID, firstCompletion); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale completion error = %v", err)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: second.LeaseToken, AttemptID: second.AttemptID,
		State: "succeeded", ExitCode: 0, Result: []byte(`{"duration_millis":30,"token_usage":40}`),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := snapshot.Jobs[0].Runs[0]
	if snapshot.Jobs[0].State != "succeeded" || run.State != "succeeded" || run.AttemptCount != 2 || len(run.Attempts) != 2 {
		t.Fatalf("run = %#v", run)
	}
	if run.Attempts[0].State != "failed" || run.Attempts[0].ErrorClass != "harness_crash" || run.Attempts[1].State != "succeeded" {
		t.Fatalf("attempts = %#v", run.Attempts)
	}
	if run.DurationMillis == nil || *run.DurationMillis != 40 || run.TokenUsage == nil || *run.TokenUsage != 60 {
		t.Fatalf("aggregate metrics = duration %v tokens %v", run.DurationMillis, run.TokenUsage)
	}
}

func TestRetryStopsAtReportedTokenBudget(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"first", "second", "third"},
		MaxAttempts: 3, MaxTotalTokens: 100, FallbackOn: []string{"harness_crash"}, Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	request := pollRequest("worker-a", []string{"first", "second", "third"}, []string{"machinist"})
	first, err := store.Poll(t.Context(), request)
	if err != nil || first == nil {
		t.Fatalf("first poll = %#v, %v", first, err)
	}
	if err := store.Complete(t.Context(), first.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: first.LeaseToken, AttemptID: first.AttemptID,
		State: "failed", ExitCode: 1, Error: "first crash", ErrorClass: "harness_crash",
		Result: []byte(`{"duration_millis":10,"token_usage":80}`),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Poll(t.Context(), request)
	if err != nil || second == nil || second.Profile != "second" {
		t.Fatalf("second poll = %#v, %v", second, err)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: second.LeaseToken, AttemptID: second.AttemptID,
		State: "failed", ExitCode: 1, Error: "second crash", ErrorClass: "harness_crash",
		Result: []byte(`{"duration_millis":20,"token_usage":30}`),
	}); err != nil {
		t.Fatal(err)
	}
	if third, err := store.Poll(t.Context(), request); err != nil || third != nil {
		t.Fatalf("third poll = %#v, %v", third, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := snapshot.Jobs[0].Runs[0]
	if run.State != "failed" || run.AttemptCount != 2 || len(run.Attempts) != 2 || run.TokenUsage == nil || *run.TokenUsage != 110 || run.DurationMillis == nil || *run.DurationMillis != 30 {
		t.Fatalf("budgeted run = %#v", run)
	}
	if !strings.Contains(run.Error, "reported token usage 110 reached max_total_tokens 100") {
		t.Fatalf("budget stop error = %q", run.Error)
	}
}

func TestRetryWithTokenBudgetRequiresCompleteUsageCoverage(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"first", "second"},
		MaxAttempts: 2, MaxTotalTokens: 100, FallbackOn: []string{"harness_crash"}, Prompt: "implement request", Timeout: time.Minute,
	}
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "implement", command); err != nil {
		t.Fatal(err)
	}
	request := pollRequest("worker-a", []string{"first", "second"}, []string{"machinist"})
	run, err := store.Poll(t.Context(), request)
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID,
		State: "failed", ExitCode: 1, Error: "crash", ErrorClass: "harness_crash",
		Result: []byte(`{"duration_millis":10}`),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.Jobs[0].Runs[0]
	if stored.State != "failed" || stored.AttemptCount != 1 || stored.TokenUsage != nil || !strings.Contains(stored.Error, "token usage coverage is incomplete") {
		t.Fatalf("coverage-gated run = %#v", stored)
	}
}

func TestPollRejectsTamperedEnvironmentManifest(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	request := pollRequest("worker-a", nil, nil)
	request.Environment = environment.Detect(nil)
	request.Environment.Arch = "tampered"
	if _, err := store.Poll(t.Context(), request); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("poll error = %v", err)
	}
}

func TestConcurrentPollsLeaseRunOnce(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run, err := store.Poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}))
			results <- run
			errorsChannel <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	leased := 0
	for run := range results {
		if run != nil {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased runs = %d, want 1", leased)
	}
}

func TestStoreConcurrentJobLimitLeavesAdditionalJobsQueued(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	firstJob, err := store.CreateJob(t.Context(), "first", "machinist", "plan", testAgent("plan", "First request"))
	if err != nil {
		t.Fatal(err)
	}
	secondJob, err := store.CreateJob(t.Context(), "second", "machinist", "plan", testAgent("plan", "Second request"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}), 1, false)
	if err != nil || first == nil || first.JobID != firstJob {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	blocked, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1, false)
	if err != nil || blocked != nil {
		t.Fatalf("poll at capacity = %#v, %v", blocked, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]string, len(snapshot.Jobs))
	for _, job := range snapshot.Jobs {
		states[job.ID] = job.State
	}
	if states[firstJob] != "running" || states[secondJob] != "queued" {
		t.Fatalf("jobs at capacity = %#v", snapshot.Jobs)
	}

	if err := store.Complete(t.Context(), first.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	second, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1, false)
	if err != nil || second == nil || second.JobID != secondJob {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
}

func TestStoreConcurrentJobLimitRedispatchesExpiredActiveJob(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	activeJob, err := store.CreateJob(t.Context(), "active", "machinist", "plan", testAgent("plan", "Active request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), "queued", "machinist", "review", testAgent("review", "Queued request")); err != nil {
		t.Fatal(err)
	}
	initial, err := store.poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}), 1, false)
	if err != nil || initial == nil || initial.JobID != activeJob {
		t.Fatalf("initial lease = %#v, %v", initial, err)
	}

	clock.Advance(leaseDuration)
	redispatched, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1, false)
	if err != nil || redispatched == nil || redispatched.ID != initial.ID || redispatched.LeaseToken == initial.LeaseToken {
		t.Fatalf("redispatched lease = %#v, %v", redispatched, err)
	}
	if redispatched.AttemptNumber != 2 || redispatched.PreviousErrorClass != "transient" {
		t.Fatalf("redispatch handoff = %#v", redispatched)
	}
}

func TestExpiredLeasesStopAtAttemptBudget(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "bounded", "machinist", "plan", testAgent("plan", "Bounded request"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	clock.Advance(leaseDuration)
	second, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}))
	if err != nil || second == nil || second.AttemptNumber != 2 {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
	clock.Advance(leaseDuration)
	third, err := store.Poll(t.Context(), pollRequest("worker-c", []string{"codex"}, []string{"machinist"}))
	if err != nil || third != nil {
		t.Fatalf("poll after exhausted budget = %#v, %v", third, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := snapshot.Jobs[0].Runs[0]
	if snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "failed" || run.State != "failed" || run.AttemptCount != 2 || run.MaxAttempts != 2 || len(run.Attempts) != 2 {
		t.Fatalf("exhausted run = %#v", snapshot.Jobs[0])
	}
	if run.Attempts[0].State != "abandoned" || run.Attempts[1].State != "abandoned" || !strings.Contains(run.Error, "attempt budget") {
		t.Fatalf("exhausted attempts = %#v; error=%q", run.Attempts, run.Error)
	}
}

func TestExpiredLeaseWithTokenBudgetStopsWhenUsageIsUnknown(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	command := config.ResolvedCommand{
		Name: "implement", Route: "implementation", Candidates: []string{"first", "second", "third"},
		MaxAttempts: 3, MaxTotalTokens: 100, FallbackOn: []string{"transient"}, Prompt: "implement request", Timeout: time.Minute,
	}
	jobID, err := store.CreateJob(t.Context(), "bounded", "machinist", "implement", command)
	if err != nil {
		t.Fatal(err)
	}
	request := pollRequest("worker-a", []string{"first", "second", "third"}, []string{"machinist"})
	first, err := store.Poll(t.Context(), request)
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	clock.Advance(leaseDuration)
	if second, err := store.Poll(t.Context(), request); err != nil || second != nil {
		t.Fatalf("poll after unobservable lease = %#v, %v", second, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := snapshot.Jobs[0].Runs[0]
	if snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "failed" || run.State != "failed" || run.AttemptCount != 1 || run.TokenUsage != nil || !strings.Contains(run.Error, "token usage coverage is incomplete") {
		t.Fatalf("coverage-gated lease recovery = %#v", snapshot.Jobs[0])
	}
}

func TestConcurrentPollsRespectGlobalJobLimit(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	for _, prompt := range []string{"first", "second"} {
		if _, err := store.CreateJob(t.Context(), prompt, "machinist", "plan", testAgent("plan", prompt)); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func(instance string) {
			defer group.Done()
			<-start
			run, err := store.poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}), 1, false)
			results <- run
			errorsChannel <- err
		}(instance)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	leased := 0
	for run := range results {
		if run != nil {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased jobs = %d, want 1", leased)
	}
}

func TestStoreRenewsAndRedispatchesExpiredLease(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))
	clock.Advance(9 * time.Second)
	repeated, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || repeated == nil || repeated.LeaseToken != first.LeaseToken {
		t.Fatalf("repeated lease = %#v, %v", repeated, err)
	}
	assertLeaseExpiry(t, store, first.ID, time.Date(2026, time.August, 25, 12, 0, 30, 0, time.UTC))

	clock.Advance(5 * time.Second)
	heartbeat := protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: first.LeaseToken}
	if _, err := store.Heartbeat(t.Context(), first.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))
	var lastSeen string
	if err := store.db.QueryRowContext(t.Context(), `SELECT last_seen_at FROM workers WHERE instance_id='worker-a'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen != clock.Now().Format(time.RFC3339Nano) {
		t.Fatalf("worker last seen = %q, want %q", lastSeen, clock.Now().Format(time.RFC3339Nano))
	}
	if _, err := store.Heartbeat(t.Context(), first.ID, protocol.Heartbeat{InstanceID: "worker-b", LeaseToken: first.LeaseToken}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("cross-worker heartbeat error = %v", err)
	}
	if _, err := store.Heartbeat(t.Context(), first.ID, protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: "stale"}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale heartbeat error = %v", err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))

	clock.Advance(leaseDuration)
	if _, err := store.Heartbeat(t.Context(), first.ID, heartbeat); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expired heartbeat error = %v", err)
	}
	staleCompletion := protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0, Result: []byte(`{"stale":true}`)}
	if err := store.Complete(t.Context(), first.ID, staleCompletion); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expired completion error = %v", err)
	}

	incompatible, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"other"}, []string{"machinist"}))
	if err != nil || incompatible != nil {
		t.Fatalf("incompatible poll = %#v, %v", incompatible, err)
	}
	assertReclaimedRun(t, store, first.ID, jobID)

	redispatched, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}))
	if err != nil || redispatched == nil || redispatched.ID != first.ID || redispatched.LeaseToken == first.LeaseToken {
		t.Fatalf("redispatched lease = %#v, %v", redispatched, err)
	}
	if err := store.Complete(t.Context(), first.ID, staleCompletion); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale completion after redispatch error = %v", err)
	}
	output, err := store.RunOutput(t.Context(), first.ID)
	if err != nil || output.Result != "" || output.Events != "" {
		t.Fatalf("stale output = %#v, %v", output, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || snapshot.Jobs[0].State != "running" || len(snapshot.Jobs[0].Runs) != 1 {
		t.Fatalf("stale completion changed job: %#v, %v", snapshot, err)
	}
}

func TestConcurrentPollsReclaimExpiredRunOnce(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(leaseDuration)

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-b", "worker-c"} {
		group.Add(1)
		go func(instance string) {
			defer group.Done()
			<-start
			run, pollErr := store.Poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}))
			results <- run
			errorsChannel <- pollErr
		}(instance)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for pollErr := range errorsChannel {
		if pollErr != nil {
			t.Fatal(pollErr)
		}
	}
	leases := 0
	for run := range results {
		if run != nil {
			leases++
			if run.ID != initial.ID || run.LeaseToken == initial.LeaseToken {
				t.Fatalf("reclaimed lease = %#v", run)
			}
		}
	}
	if leases != 1 {
		t.Fatalf("reclaimed leases = %d, want 1", leases)
	}
}

func TestStorePersistsCurrentWorkerRepositories(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"other", "machinist", "machinist"})); err != nil || run != nil {
		t.Fatalf("first poll = %#v, %v", run, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || len(snapshot.Workers[0].Repositories) != 2 || snapshot.Workers[0].Repositories[0] != "machinist" || snapshot.Workers[0].Repositories[1] != "other" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
	if run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"})); err != nil || run != nil {
		t.Fatalf("second poll = %#v, %v", run, err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers[0].Repositories) != 1 || snapshot.Workers[0].Repositories[0] != "machinist" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
	repositories, err := store.KnownRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repositories, []string{"machinist"}) {
		t.Fatalf("known repositories = %#v", repositories)
	}
}

func TestStorePrunesSupersededWorkerAndPreservesRunWorkerName(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-old", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist", "retired"}})
	if err != nil || run == nil {
		t.Fatalf("old worker poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-old", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if _, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-new", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist"}}); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneSupersededWorkers(t.Context(), clock.Now().Add(-workerAvailabilityWindow))
	if err != nil || pruned != 1 {
		t.Fatalf("pruned workers = %d, %v", pruned, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || snapshot.Workers[0].InstanceID != "worker-new" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].Runs[0].WorkerName != "builder" {
		t.Fatalf("jobs = %#v", snapshot.Jobs)
	}
	repositories, err := store.KnownRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repositories, []string{"machinist"}) {
		t.Fatalf("known repositories = %#v", repositories)
	}
}

func TestStoreRetainsLatestDisconnectedWorkerRegistration(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-only", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneSupersededWorkers(t.Context(), time.Now().Add(-time.Minute))
	if err != nil || pruned != 0 {
		t.Fatalf("pruned workers = %d, %v", pruned, err)
	}
}

func TestStoreReclaimsExpiredLeaseWithoutWorkerPoll(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	clock.Advance(leaseDuration + time.Second)
	reclaimed, err := store.ReclaimExpiredLeases(t.Context())
	if err != nil || reclaimed != 1 {
		t.Fatalf("reclaimed leases = %d, %v", reclaimed, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "running" || snapshot.Jobs[0].Runs[0].State != "queued" || snapshot.Jobs[0].Runs[0].WorkerName != "" || !snapshot.Jobs[0].Runs[0].StartedAt.IsZero() {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestStoreDeletesTerminalJobAndRejectsActiveJob(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	jobID, err := store.CreateJob(t.Context(), "active", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); !errors.Is(err, ErrJobActive) {
		t.Fatalf("active delete error = %v", err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0, Result: []byte(`{"answer":42}`), Events: "event\n"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunOutput(t.Context(), run.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted run output error = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 0 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
}

func TestCancelQueuedJobIsTerminalAndNeverDispatched(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CancelJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run != nil {
		t.Fatalf("poll after cancellation = %#v, %v", run, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != "cancelled" || len(snapshot.Jobs[0].Runs) != 1 || snapshot.Jobs[0].Runs[0].State != "cancelled" || snapshot.Jobs[0].Runs[0].ExitCode == nil || *snapshot.Jobs[0].Runs[0].ExitCode != 130 {
		t.Fatalf("cancelled snapshot = %#v", snapshot)
	}
}

func TestCancelRunningJobSignalsWorkerAndOverridesRacingSuccess(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.CancelJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := store.Heartbeat(t.Context(), run.ID, protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: run.LeaseToken})
	if err != nil || !heartbeat.CancelRequested {
		t.Fatalf("heartbeat after cancellation = %#v, %v", heartbeat, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := snapshot.Jobs[0]
	if stored.State != "cancelled" || stored.Runs[0].State != "cancelled" || stored.Runs[0].LastErrorClass != "cancelled" || len(stored.Runs[0].Attempts) != 1 || stored.Runs[0].Attempts[0].State != "cancelled" {
		t.Fatalf("completion after cancellation = %#v", stored)
	}
}

func TestStoreDeletingTriggeredJobPreservesPendingReconciliation(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "github/intake", Family: "github", ConfigSignature: "v1"}}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{
		Identity: "github/intake", Family: "github", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "github/intake"),
		OccurrenceKey: "github.com/event/1", Subject: "https://github.com/owainlewis/machinist/issues/396", ScheduledAt: time.Now().UTC(),
		Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
		GitHubRepository: "owainlewis/machinist", GitHubIssueNumber: 396, RequestActor: "owner", RequestLabel: "machinist:requested",
	}
	jobID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("triggered job = %q, %v, %v", jobID, created, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	reconciliations, err := store.GitHubTriggerReconciliations(t.Context(), admission.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciliations) != 1 || reconciliations[0].OccurrenceKey != admission.OccurrenceKey || reconciliations[0].State != "admitted" || reconciliations[0].JobID != "" {
		t.Fatalf("reconciliations = %#v", reconciliations)
	}
}

func TestAvailableRepositoriesExcludesStaleWorkerInstances(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"removed"})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id='worker-old'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Poll(t.Context(), pollRequest("worker-new", []string{"codex"}, []string{"machinist"})); err != nil {
		t.Fatal(err)
	}
	repositories, err := store.AvailableRepositories(t.Context(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != "machinist" {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestAvailableRepositoriesIncludesWorkerWithRunningExecution(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-busy", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id='worker-busy'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	repositories, err := store.AvailableRepositories(t.Context(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != "machinist" {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestStoreSyncsDurableTriggerStateAcrossRestartAndConfigurationChanges(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, database)
	store.now = clock.Now
	firstDue := clock.Now().Add(time.Hour)
	definitions := []TriggerDefinition{
		{Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", NextDueAt: firstDue},
		{Identity: "github/intake", Family: "github", ConfigSignature: "v1"},
	}
	if err := store.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/intake", mustTriggerGeneration(t, store, "github/intake"), 3, errors.New(strings.Repeat("x", maxTriggerErrorLength+10))); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := store.SetTriggerNextDue(t.Context(), "interval/audit", mustTriggerGeneration(t, store, "interval/audit"), firstDue.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, database)
	reopened.now = clock.Now
	if err := reopened.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	statuses, err := reopened.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].Identity != "github/intake" || statuses[1].Identity != "interval/audit" {
		t.Fatalf("trigger statuses = %#v", statuses)
	}
	if statuses[0].Health != "failed" || statuses[0].CandidateCount != 3 || len([]rune(statuses[0].LatestError)) != maxTriggerErrorLength {
		t.Fatalf("GitHub status = %#v", statuses[0])
	}
	if statuses[1].NextDueAt == nil || !statuses[1].NextDueAt.Equal(firstDue.Add(time.Hour)) {
		t.Fatalf("unchanged next due = %#v", statuses[1].NextDueAt)
	}

	changedDue := clock.Now().Add(4 * time.Hour)
	if err := reopened.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "github/intake", Family: "github", ConfigSignature: "v2", NextDueAt: changedDue}}); err != nil {
		t.Fatal(err)
	}
	statuses, err = reopened.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Identity != "github/intake" || statuses[0].Health != "healthy" || statuses[0].CandidateCount != 0 || statuses[0].LastAttemptAt != nil || statuses[0].LatestError != "" {
		t.Fatalf("changed trigger status = %#v", statuses)
	}
	if statuses[0].NextDueAt == nil || !statuses[0].NextDueAt.Equal(changedDue) {
		t.Fatalf("changed next due = %#v", statuses[0].NextDueAt)
	}
}

func TestStoreAdmitsUniqueFixedOccurrencesAndCoalescesOverlap(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", NextDueAt: clock.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{
		Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "interval/audit"), ScheduledAt: clock.Now().Add(time.Hour), NextDueAt: clock.Now().Add(2 * time.Hour),
		Prompt: "Audit", Repository: "machinist", SelectionName: "audit", Command: testAgent("audit", "Audit"),
	}
	jobID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("first admission = %q, %v, %v", jobID, created, err)
	}
	duplicateID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || created || duplicateID != jobID {
		t.Fatalf("duplicate admission = %q, %v, %v", duplicateID, created, err)
	}
	later := admission
	later.ScheduledAt = clock.Now().Add(2 * time.Hour)
	later.NextDueAt = clock.Now().Add(3 * time.Hour)
	activeID, created, err := store.CreateTriggeredJob(t.Context(), later)
	if err != nil || created || activeID != jobID {
		t.Fatalf("coalesced admission = %q, %v, %v", activeID, created, err)
	}
	if err := store.AddTriggerCoalesced(t.Context(), admission.Identity, admission.ConfigGeneration, 2); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Triggers) != 1 || snapshot.Triggers[0].ActiveJobID != jobID || snapshot.Triggers[0].AdmissionCount != 1 || snapshot.Triggers[0].CoalescedCount != 3 || snapshot.Triggers[0].Health != "coalesced" {
		t.Fatalf("active trigger snapshot = %#v", snapshot.Triggers)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].TriggerID != admission.Identity || snapshot.Jobs[0].OccurrenceKey != admission.ScheduledAt.Format(time.RFC3339Nano) {
		t.Fatalf("triggered job snapshot = %#v", snapshot.Jobs)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	clock.Advance(time.Second)
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].ActiveJobID != "" || statuses[0].LastSuccessAt == nil || !statuses[0].LastSuccessAt.Equal(clock.Now()) || statuses[0].Health != "healthy" {
		t.Fatalf("completed trigger status = %#v", statuses[0])
	}
	secondID, created, err := store.CreateTriggeredJob(t.Context(), later)
	if err != nil || !created || secondID == jobID {
		t.Fatalf("catch-up admission = %q, %v, %v", secondID, created, err)
	}
}

func TestStorePreservesLatestFailedJobHealthAcrossSuccessfulPolls(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	definitions := []TriggerDefinition{
		{Identity: "github/jobs", Family: "github", ConfigSignature: "jobs"},
		{Identity: "github/polls", Family: "github", ConfigSignature: "polls"},
	}
	if err := store.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
		Identity: "github/jobs", Family: "github", ConfigSignature: "jobs", ConfigGeneration: mustTriggerGeneration(t, store, "github/jobs"),
		OccurrenceKey: "github.com:event:1", Subject: "https://github.com/o/r/issues/1", ScheduledAt: clock.Now(),
		Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
		GitHubRepository: "o/r", GitHubIssueNumber: 1, RequestActor: "owner", RequestLabel: "machinist:requested",
	})
	if err != nil || !created {
		t.Fatalf("admit github job = %v, %v", created, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll github job = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1, Error: "agent failed"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := store.RecordTriggerAttempt(t.Context(), "github/jobs", mustTriggerGeneration(t, store, "github/jobs"), 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/polls", mustTriggerGeneration(t, store, "github/polls"), 0, errors.New("temporary poll failure")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/polls", mustTriggerGeneration(t, store, "github/polls"), 0, nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byIdentity := map[string]TriggerStatus{}
	for _, status := range statuses {
		byIdentity[status.Identity] = status
	}
	if status := byIdentity["github/jobs"]; status.Health != "failed" || status.LatestError != "agent failed" {
		t.Fatalf("failed job health was erased by successful poll: %#v", status)
	}
	if status := byIdentity["github/polls"]; status.Health != "healthy" || status.LatestError != "" {
		t.Fatalf("recovered poll error was retained: %#v", status)
	}
}

func TestStorePreservesTriggerHealthFromActualCompletionOrder(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	identity := "github/jobs"
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "github", ConfigSignature: "jobs"}}); err != nil {
		t.Fatal(err)
	}
	generation := mustTriggerGeneration(t, store, identity)
	for issue := 1; issue <= 2; issue++ {
		_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
			Identity: identity, Family: "github", ConfigSignature: "jobs", ConfigGeneration: generation,
			OccurrenceKey: fmt.Sprintf("github.com:event:%d", issue), Subject: fmt.Sprintf("https://github.com/o/r/issues/%d", issue), ScheduledAt: clock.Now(),
			Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
			GitHubRepository: "o/r", GitHubIssueNumber: issue, RequestActor: "owner", RequestLabel: "machinist:requested",
		})
		if err != nil || !created {
			t.Fatalf("admit github job %d = %v, %v", issue, created, err)
		}
	}
	older, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || older == nil {
		t.Fatalf("poll older job = %#v, %v", older, err)
	}
	newer, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}))
	if err != nil || newer == nil || newer.JobID == older.JobID {
		t.Fatalf("poll newer job = %#v, %v", newer, err)
	}
	if err := store.Complete(t.Context(), newer.ID, protocol.Completion{InstanceID: "worker-b", LeaseToken: newer.LeaseToken, State: "failed", ExitCode: 1, Error: "newer job failed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), older.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: older.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), identity, generation, 0, nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Health != "healthy" || statuses[0].LatestError != "" {
		t.Fatalf("completion-order health = %#v, want healthy", statuses)
	}
}

func TestStoreIgnoresCompletionFromPreviousTriggerConfiguration(t *testing.T) {
	for _, completion := range []protocol.Completion{
		{State: "succeeded", ExitCode: 0},
		{State: "failed", ExitCode: 1, Error: "old configuration failed"},
	} {
		t.Run(completion.State, func(t *testing.T) {
			clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
			store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
			store.now = clock.Now
			identity := "interval/audit"
			if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v1", NextDueAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
				Identity: identity, Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, identity),
				ScheduledAt: clock.Now(), NextDueAt: clock.Now().Add(time.Hour),
				Prompt: "Audit", Repository: "machinist", SelectionName: "audit",
				Command: testAgent("audit", "Audit"),
			})
			if err != nil || !created {
				t.Fatalf("admit v1 trigger = %v, %v", created, err)
			}
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
			if err != nil || run == nil {
				t.Fatalf("poll v1 trigger = %#v, %v", run, err)
			}
			if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v2", NextDueAt: clock.Now().Add(2 * time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			completion.InstanceID = "worker-a"
			completion.LeaseToken = run.LeaseToken
			if err := store.Complete(t.Context(), run.ID, completion); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordTriggerAttempt(t.Context(), identity, mustTriggerGeneration(t, store, identity), 0, nil); err != nil {
				t.Fatal(err)
			}
			statuses, err := store.TriggerSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(statuses) != 1 || statuses[0].ConfigSignature != "v2" || statuses[0].Health != "healthy" || statuses[0].LastSuccessAt != nil || statuses[0].LatestError != "" {
				t.Fatalf("v2 status changed by v1 completion: %#v", statuses)
			}
		})
	}
}

func TestStoreRejectsSchedulerWritesFromPreviousTriggerConfiguration(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	identity := "interval/audit"
	v2Due := clock.Now().Add(2 * time.Hour)
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v2", NextDueAt: v2Due}}); err != nil {
		t.Fatal(err)
	}
	staleWrites := []func() error{
		func() error {
			return store.RecordTriggerAttempt(t.Context(), identity, "v1", 1, errors.New("stale failure"))
		},
		func() error { return store.SetTriggerNextDue(t.Context(), identity, "v1", clock.Now().Add(time.Hour)) },
		func() error { return store.SetTriggerPendingOccurrence(t.Context(), identity, "v1", clock.Now()) },
		func() error { return store.AddTriggerCoalesced(t.Context(), identity, "v1", 1) },
	}
	for index, write := range staleWrites {
		if err := write(); !errors.Is(err, ErrTriggerStale) {
			t.Fatalf("stale write %d error = %v, want ErrTriggerStale", index, err)
		}
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].ConfigSignature != "v2" || statuses[0].NextDueAt == nil || !statuses[0].NextDueAt.Equal(v2Due) || statuses[0].PendingOccurrenceAt != nil || statuses[0].LastAttemptAt != nil || statuses[0].Health != "healthy" || statuses[0].CandidateCount != 0 || statuses[0].CoalescedCount != 0 || statuses[0].LatestError != "" {
		t.Fatalf("v2 status changed by stale scheduler: %#v", statuses)
	}
}

func TestStoreUsesDistinctTriggerGenerationsAcrossABAAndRecreation(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	identity := "interval/audit"
	sync := func(signature string) string {
		t.Helper()
		if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: signature, NextDueAt: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
		return mustTriggerGeneration(t, store, identity)
	}
	a1 := sync("a")
	if got := sync("a"); got != a1 {
		t.Fatalf("unchanged configuration generation changed: %q then %q", a1, got)
	}
	b := sync("b")
	a2 := sync("a")
	if a1 == b || b == a2 || a1 == a2 {
		t.Fatalf("ABA generations are not unique: a1=%q b=%q a2=%q", a1, b, a2)
	}
	if err := store.SyncTriggers(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	a3 := sync("a")
	if a3 == a2 || a3 == a1 {
		t.Fatalf("recreated trigger reused a generation: a1=%q a2=%q a3=%q", a1, a2, a3)
	}
}

func TestStoreRetriesUncommittedAdmissionAndPreventsSubjectOverlap(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{
		{Identity: "github/intake", Family: "github", ConfigSignature: "v1"},
		{Identity: "github/security", Family: "github", ConfigSignature: "v1"},
	}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{Identity: "github/intake", Family: "github", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "github/intake"), OccurrenceKey: "github.com/event/1", Subject: "https://github.com/owainlewis/machinist/issues/396", Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", GitHubRepository: "owainlewis/machinist", GitHubIssueNumber: 396, RequestActor: "owner", RequestLabel: "machinist:requested", ScheduledAt: time.Now().UTC()}
	if _, _, err := store.CreateTriggeredJob(t.Context(), admission); err == nil {
		t.Fatal("expected incomplete admission to fail")
	}
	admission.Command = testAgent("foreman", "Complete issue")
	firstID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("retried admission = %q, %v, %v", firstID, created, err)
	}
	exists, err := store.TriggerOccurrenceExists(t.Context(), admission.Identity, admission.OccurrenceKey)
	if err != nil || !exists {
		t.Fatalf("committed occurrence exists = %v, %v", exists, err)
	}
	reapplied := admission
	reapplied.Identity = "github/security"
	reapplied.ConfigSignature = "v1"
	reapplied.ConfigGeneration = mustTriggerGeneration(t, store, "github/security")
	reapplied.OccurrenceKey = "github.com/event/2"
	activeID, created, err := store.CreateTriggeredJob(t.Context(), reapplied)
	if err != nil || created || activeID != firstID {
		t.Fatalf("overlapping subject = %q, %v, %v", activeID, created, err)
	}
	exists, err = store.TriggerOccurrenceExists(t.Context(), reapplied.Identity, reapplied.OccurrenceKey)
	if err != nil || exists {
		t.Fatalf("blocked occurrence exists = %v, %v", exists, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1, Error: "work failed"}); err != nil {
		t.Fatal(err)
	}
	duplicateID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || created || duplicateID != firstID {
		t.Fatalf("terminal duplicate occurrence = %q, %v, %v", duplicateID, created, err)
	}
	secondID, created, err := store.CreateTriggeredJob(t.Context(), reapplied)
	if err != nil || !created || secondID == firstID {
		t.Fatalf("reapplied occurrence = %q, %v, %v", secondID, created, err)
	}
}

func mustTriggerGeneration(t *testing.T, store *Store, identity string) string {
	t.Helper()
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Identity == identity {
			return status.ConfigGeneration
		}
	}
	t.Fatalf("trigger %q has no generation", identity)
	return ""
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testAgent(name, prompt string) config.ResolvedCommand {
	return config.ResolvedCommand{Name: name, Executor: "codex", Prompt: prompt, Timeout: time.Minute, Hash: name + "-hash"}
}

func pollRequest(instance string, executors, repositories []string) protocol.PollRequest {
	return protocol.PollRequest{InstanceID: instance, Name: "test", Executors: executors, Repositories: repositories}
}

func assertStoredMetrics(t *testing.T, store *Store, jobID string) {
	t.Helper()
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || len(snapshot.Jobs[0].Runs) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	reported, missing := snapshot.Jobs[0].Runs[0], snapshot.Jobs[0].Runs[1]
	if reported.DurationMillis == nil || *reported.DurationMillis != 1250 || reported.TokenUsage == nil || *reported.TokenUsage != 987 {
		t.Fatalf("reported metrics = %#v", reported)
	}
	if missing.DurationMillis == nil || *missing.DurationMillis != 2500 || missing.TokenUsage != nil {
		t.Fatalf("missing token metrics = %#v", missing)
	}
}

func findRun(t *testing.T, snapshot Snapshot, runID string) Run {
	t.Helper()
	for _, job := range snapshot.Jobs {
		for _, run := range job.Runs {
			if run.ID == runID {
				return run
			}
		}
	}
	t.Fatalf("run %q not found", runID)
	return Run{}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func assertLeaseExpiry(t *testing.T, store *Store, runID string, want time.Time) {
	t.Helper()
	var got sql.NullInt64
	if err := store.db.QueryRowContext(t.Context(), `SELECT lease_expires_at FROM runs WHERE id=?`, runID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != want.UnixNano() {
		t.Fatalf("lease expiry = %v, want %d", got, want.UnixNano())
	}
}

func assertReclaimedRun(t *testing.T, store *Store, runID, jobID string) {
	t.Helper()
	var state, workerName string
	var worker, token, expiry, started any
	if err := store.db.QueryRowContext(t.Context(), `SELECT state,worker_instance,worker_name,lease_token,lease_expires_at,started_at FROM runs WHERE id=?`, runID).Scan(&state, &worker, &workerName, &token, &expiry, &started); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || worker != nil || workerName != "" || token != nil || expiry != nil || started != nil {
		t.Fatalf("reclaimed run = state %q worker %v worker name %q token %v expiry %v started %v", state, worker, workerName, token, expiry, started)
	}
	var jobState string
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "running" {
		t.Fatalf("job state = %q, want running", jobState)
	}
}

func TestOpenStoreUpgradesVersionOneSchema(t *testing.T) {
	for name, partial := range map[string]string{
		"complete version one": "",
		"interrupted upgrade":  "DROP INDEX jobs_active_shepherd_repository; ALTER TABLE jobs DROP COLUMN has_shepherd;",
	} {
		t.Run(name, func(t *testing.T) { testVersionOneUpgrade(t, partial) })
	}
}

func TestOpenStoreUpgradeDrainsScheduledShepherdJobs(t *testing.T) {
	path := openVersionOneDatabase(t, `INSERT INTO jobs(id,prompt,repository,command,schedule_name,has_shepherd,state,created_at,updated_at) VALUES('job_queued','p','api','shepherd','api',1,'queued','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state) VALUES('run_queued','job_queued','shepherd','h','codex','api','p',1000,'queued');`)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != "failed" || len(snapshot.Jobs[0].Runs) != 1 || snapshot.Jobs[0].Runs[0].State != "failed" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if !strings.Contains(snapshot.Jobs[0].Runs[0].Error, "shepherd schedules were removed") {
		t.Fatalf("run error = %q", snapshot.Jobs[0].Runs[0].Error)
	}
}

func TestOpenStoreUpgradeWaitsForRunningScheduledShepherdJob(t *testing.T) {
	path := openVersionOneDatabase(t, `INSERT INTO jobs(id,prompt,repository,command,schedule_name,has_shepherd,state,created_at,updated_at) VALUES('job_running','p','api','shepherd','api',1,'running','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	store, err := OpenStore(path)
	if store != nil {
		store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("open error = %v, want running job refusal", err)
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var version, columns int
	if err := legacy.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema version = %d, %v, want untouched version 1", version, err)
	}
	if err := legacy.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name IN ('has_shepherd','schedule_name')`).Scan(&columns); err != nil || columns != 2 {
		t.Fatalf("legacy columns = %d, %v, want both retained", columns, err)
	}
}

func testVersionOneUpgrade(t *testing.T, partial string) {
	path := openVersionOneDatabase(t, `INSERT INTO jobs(id,prompt,repository,command,schedule_name,has_shepherd,state,created_at,updated_at) VALUES('job_1','p','api','shepherd','api',1,'succeeded','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`+partial)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, %v, want 11", version, err)
	}
	var columns int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name IN ('has_shepherd','schedule_name')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("legacy columns = %d, %v, want 0", columns, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != "job_1" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
}

// openVersionOneDatabase creates a schema version 1 database and runs extra
// statements against it before returning its path.
func openVersionOneDatabase(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machinist.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL, command TEXT NOT NULL,
 schedule_name TEXT NOT NULL DEFAULT '', has_shepherd INTEGER NOT NULL DEFAULT 0,
 trigger_identity TEXT NOT NULL DEFAULT '', trigger_config_signature TEXT NOT NULL DEFAULT '',
 trigger_generation_id TEXT NOT NULL DEFAULT '', occurrence_key TEXT NOT NULL DEFAULT '',
 trigger_subject TEXT NOT NULL DEFAULT '', github_issue_title TEXT NOT NULL DEFAULT '',
 fixed_trigger INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE runs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id), command TEXT NOT NULL, command_hash TEXT NOT NULL,
 executor TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL,
 timeout_ms INTEGER NOT NULL, state TEXT NOT NULL, worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
 lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT, result TEXT, events TEXT,
 started_at TEXT, completed_at TEXT, duration_millis INTEGER, token_usage INTEGER);
CREATE UNIQUE INDEX jobs_active_shepherd_repository ON jobs(repository) WHERE has_shepherd=1 AND state IN ('queued','running');
CREATE TABLE schedule_state (name TEXT PRIMARY KEY, next_run_at TEXT NOT NULL);
PRAGMA user_version=1;` + extra); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGitHubMarkerTargetsFollowTheRunAndStopWhenRecorded(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock,
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := store.GitHubMarkerTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("a control plane with no admitted work has nothing to publish: %#v", targets)
	}

	candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{candidate},
		details: GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{
			ID: "123", Actor: "owner", CreatedAt: clock, OccurrenceKey: "github.com:123",
		}},
		permission: "write",
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}

	targets, err = store.GitHubMarkerTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Repository != "owainlewis/machinist" || targets[0].IssueNumber != 396 || targets[0].RunState != "queued" {
		t.Fatalf("admitted work is not a marker target: %#v", targets)
	}
	if err := store.RecordPublishedMarker(t.Context(), targets[0], factoryrun.StageClaimed); err != nil {
		t.Fatal(err)
	}
	targets, err = store.GitHubMarkerTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("work whose marker is current is not a target: %#v", targets)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	targets, err = store.GitHubMarkerTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].RunState != "running" || targets[0].AttemptID == "" {
		t.Fatalf("a started run is not a marker target: %#v", targets)
	}
}

// Turning marker publication on must not retroactively comment on every issue
// the control plane has ever worked, so finished work is seeded as already
// described. Work still in flight is not: it gets its marker on the next pass.
func TestUpgradeSeedsPublishedMarkersForFinishedWorkOnly(t *testing.T) {
	clock := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return clock }
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, number := range []int{100, 200} {
		candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: number, State: "open", CreatedAt: clock}
		client := &fakeGitHubTriggerClient{
			candidates: []GitHubCandidate{candidate},
			details: GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{
				ID: fmt.Sprintf("%d", number), Actor: "owner", CreatedAt: clock, OccurrenceKey: fmt.Sprintf("github.com:%d", number),
			}},
			permission: "write",
		}
		server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}
		if err := processManagedTriggers(t.Context(), server); err != nil {
			t.Fatal(err)
		}
		if number == 100 {
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
			if err != nil || run == nil {
				t.Fatalf("poll = %#v, %v", run, err)
			}
			if err := store.Complete(t.Context(), run.ID, protocol.Completion{
				InstanceID: "worker-a", LeaseToken: run.LeaseToken, AttemptID: run.AttemptID, State: "succeeded", ExitCode: 0,
			}); err != nil {
				t.Fatal(err)
			}
		}
		clock = clock.Add(trigger.Every)
	}

	// Return the database to the state it had before markers were published.
	if _, err := store.db.ExecContext(t.Context(), `DROP TABLE github_run_markers; PRAGMA user_version=8;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	targets, err := upgraded.GitHubMarkerTargets(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected only the unfinished run to want a marker: %#v", targets)
	}
	if targets[0].IssueNumber != 200 || targets[0].RunState != "queued" {
		t.Fatalf("wrong work was left to describe: %#v", targets[0])
	}
}
