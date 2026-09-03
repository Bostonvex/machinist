package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/environment"
	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

var (
	ErrLeaseConflict                   = errors.New("run lease does not match")
	ErrRunState                        = errors.New("run is not active")
	ErrInvalidCompletion               = errors.New("invalid run completion")
	ErrJobActive                       = errors.New("active job cannot be deleted")
	ErrTriggerMissing                  = errors.New("trigger state does not exist")
	ErrTriggerStale                    = errors.New("trigger state configuration changed")
	ErrTriggerPreviousGenerationActive = errors.New("previous trigger configuration still has active work")
)

const leaseDuration = 30 * time.Second
const maxTriggerErrorLength = 2000
const defaultLegacyMaxAttempts = 2
const routeAvailabilityWindow = 15 * time.Second

// reclaimExpiredLeasesSQL returns running runs whose lease lapsed to the queue
// only while their attempt budget still permits another fenced execution.
const reclaimExpiredLeasesSQL = `UPDATE runs SET state='queued',worker_instance=NULL,worker_name='',lease_token=NULL,lease_expires_at=NULL,started_at=NULL,current_attempt_id='',error='worker lease expired; redispatching within attempt budget',last_error_class='transient',duration_millis=NULL,token_usage=NULL WHERE state='running' AND cancel_requested=0 AND attempt_count<max_attempts AND (lease_expires_at IS NULL OR lease_expires_at<=?)`

const abandonExpiredAttemptsSQL = `UPDATE attempts SET state='abandoned',error='worker lease expired',error_class='transient',completed_at=? WHERE state='running' AND run_id IN (SELECT id FROM runs WHERE state='running' AND cancel_requested=0 AND (lease_expires_at IS NULL OR lease_expires_at<=?))`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Job struct {
	ID               string    `json:"id"`
	Prompt           string    `json:"prompt"`
	Repository       string    `json:"repository"`
	GitHubIssueTitle string    `json:"github_issue_title,omitempty"`
	Command          string    `json:"command"`
	ExecutionMode    string    `json:"execution_mode"`
	Origin           string    `json:"origin"`
	TriggerID        string    `json:"trigger_id,omitempty"`
	OccurrenceKey    string    `json:"occurrence_key,omitempty"`
	TriggerSubject   string    `json:"trigger_subject,omitempty"`
	State            string    `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Runs             []Run     `json:"runs"`
}

type Run struct {
	ID              string    `json:"id"`
	Command         string    `json:"command"`
	Executor        string    `json:"executor"`
	Profile         string    `json:"profile,omitempty"`
	Route           string    `json:"route,omitempty"`
	Harness         string    `json:"harness,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	AuthMode        string    `json:"auth_mode,omitempty"`
	Role            string    `json:"role,omitempty"`
	AttemptCount    int       `json:"attempt_count"`
	MaxAttempts     int       `json:"max_attempts"`
	MaxTotalTokens  int64     `json:"max_total_tokens,omitempty,string"`
	LastErrorClass  string    `json:"last_error_class,omitempty"`
	Attempts        []Attempt `json:"attempts"`
	Model           string    `json:"model,omitempty"`
	State           string    `json:"state"`
	WorkerName      string    `json:"worker_name,omitempty"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	DurationMillis  *int64    `json:"duration_millis,omitempty"`
	TokenUsage      *int64    `json:"token_usage,omitempty,string"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
}

type Attempt struct {
	ID             string                    `json:"id"`
	Number         int                       `json:"number"`
	Profile        string                    `json:"profile,omitempty"`
	Harness        string                    `json:"harness,omitempty"`
	Provider       string                    `json:"provider,omitempty"`
	Model          string                    `json:"model,omitempty"`
	WorkerName     string                    `json:"worker_name,omitempty"`
	State          string                    `json:"state"`
	ExitCode       *int                      `json:"exit_code,omitempty"`
	Error          string                    `json:"error,omitempty"`
	ErrorClass     string                    `json:"error_class,omitempty"`
	StartedAt      time.Time                 `json:"started_at,omitempty"`
	CompletedAt    time.Time                 `json:"completed_at,omitempty"`
	DurationMillis *int64                    `json:"duration_millis,omitempty"`
	TokenUsage     *int64                    `json:"token_usage,omitempty,string"`
	Terminal       *protocol.TerminalBinding `json:"terminal,omitempty"`
}

type Worker struct {
	InstanceID   string                                `json:"instance_id"`
	Name         string                                `json:"name"`
	LastSeenAt   time.Time                             `json:"last_seen_at"`
	Repositories []string                              `json:"repositories"`
	Environment  environment.Manifest                  `json:"environment,omitempty"`
	Profiles     map[string]protocol.ProfileCapability `json:"profiles,omitempty"`
	Connected    bool                                  `json:"connected"`
	Transports   []string                              `json:"transports,omitempty"`
}

type Snapshot struct {
	Jobs     []Job           `json:"jobs"`
	Workers  []Worker        `json:"workers"`
	Triggers []TriggerStatus `json:"triggers"`
}

// TriggerDefinition is the durable identity and schedule state for one resolved trigger.
// A changed signature resets scheduling from NextDueAt; an unchanged signature preserves
// its existing due time across restarts.
type TriggerDefinition struct {
	Identity        string
	Family          string
	ConfigSignature string
	NextDueAt       time.Time
}

// TriggerAdmission contains an already resolved job. Trigger scheduling code remains
// responsible for validating configuration and rendering the prompt before admission.
type TriggerAdmission struct {
	Identity          string
	Family            string
	ConfigSignature   string
	ConfigGeneration  string
	OccurrenceKey     string
	Subject           string
	ScheduledAt       time.Time
	NextDueAt         time.Time
	Prompt            string
	Repository        string
	SelectionName     string
	Command           config.ResolvedCommand
	GitHubRepository  string
	GitHubIssueNumber int
	GitHubIssueTitle  string
	RequestActor      string
	RequestLabel      string
}

type TriggerStatus struct {
	Identity            string     `json:"identity"`
	Family              string     `json:"family"`
	ConfigSignature     string     `json:"config_signature,omitempty"`
	ConfigGeneration    string     `json:"-"`
	NextDueAt           *time.Time `json:"next_due,omitempty"`
	PendingOccurrenceAt *time.Time `json:"-"`
	LastAttemptAt       *time.Time `json:"last_attempt,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success,omitempty"`
	ActiveJobID         string     `json:"active_job,omitempty"`
	CandidateCount      int64      `json:"candidate_count,omitempty"`
	AdmissionCount      int64      `json:"admission_count,omitempty"`
	CoalescedCount      int64      `json:"coalesced_count,omitempty"`
	Health              string     `json:"health"`
	LatestError         string     `json:"error,omitempty"`
}

type GitHubTriggerRequest struct {
	TriggerIdentity  string
	OccurrenceKey    string
	ConfigGeneration string
	Repository       string
	IssueNumber      int
	Subject          string
	Actor            string
	RequestLabel     string
	RequestedAt      time.Time
	State            string
	JobID            string
}

type RunOutput struct {
	Result string
	Events string
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	const schemaVersion = 8
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version < 1 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF;
DROP TABLE IF EXISTS github_trigger_requests; DROP TABLE IF EXISTS trigger_state; DROP TABLE IF EXISTS schedule_state;
DROP TABLE IF EXISTS known_repositories; DROP TABLE IF EXISTS worker_repositories; DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS runs; DROP TABLE IF EXISTS jobs; PRAGMA foreign_keys=ON;`); err != nil {
			return fmt.Errorf("replace legacy database schema: %w", err)
		}
	}
	if version == 1 {
		if err := s.upgradeToVersionTwo(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 2: %w", err)
		}
		version = 2
	}
	if version == 2 {
		if err := s.upgradeToVersionThree(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 3: %w", err)
		}
		version = 3
	}
	if version == 3 {
		if err := s.upgradeToVersionFour(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 4: %w", err)
		}
		version = 4
	}
	if version == 4 {
		if err := s.upgradeToVersionFive(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 5: %w", err)
		}
		version = 5
	}
	if version == 5 {
		if err := s.upgradeToVersionSix(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 6: %w", err)
		}
		version = 6
	}
	if version == 6 {
		if err := s.upgradeToVersionSeven(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 7: %w", err)
		}
		version = 7
	}
	if version == 7 {
		if err := s.upgradeToVersionEight(ctx); err != nil {
			return fmt.Errorf("upgrade database schema to version 8: %w", err)
		}
	}
	const schema = `
PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL, command TEXT NOT NULL,
 trigger_identity TEXT NOT NULL DEFAULT '', trigger_config_signature TEXT NOT NULL DEFAULT '',
 trigger_generation_id TEXT NOT NULL DEFAULT '', occurrence_key TEXT NOT NULL DEFAULT '',
 trigger_subject TEXT NOT NULL DEFAULT '', github_issue_title TEXT NOT NULL DEFAULT '',
 fixed_trigger INTEGER NOT NULL DEFAULT 0, execution_mode TEXT NOT NULL DEFAULT 'process', origin TEXT NOT NULL DEFAULT 'machinist', state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id), command TEXT NOT NULL, command_hash TEXT NOT NULL,
 executor TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL,
 timeout_ms INTEGER NOT NULL, state TEXT NOT NULL, worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
 lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT, result TEXT, events TEXT,
 started_at TEXT, completed_at TEXT, duration_millis INTEGER, token_usage INTEGER,
 route TEXT NOT NULL DEFAULT '', profile TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL DEFAULT '', auth_mode TEXT NOT NULL DEFAULT '', role TEXT NOT NULL DEFAULT '',
 candidate_profiles TEXT NOT NULL DEFAULT '[]', max_attempts INTEGER NOT NULL DEFAULT 1,
 max_total_tokens INTEGER NOT NULL DEFAULT 0,
 fallback_on TEXT NOT NULL DEFAULT '[]', current_attempt_id TEXT NOT NULL DEFAULT '',
 attempt_count INTEGER NOT NULL DEFAULT 0, last_error_class TEXT NOT NULL DEFAULT '',
 next_candidate INTEGER NOT NULL DEFAULT 0, cancel_requested INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS runs_dispatch ON runs(state, job_id);
CREATE TABLE IF NOT EXISTS workers (instance_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen_at TEXT NOT NULL, environment_json TEXT NOT NULL DEFAULT '{}', profiles_json TEXT NOT NULL DEFAULT '{}', transports_json TEXT NOT NULL DEFAULT '["process"]');
CREATE TABLE IF NOT EXISTS worker_repositories (worker_instance TEXT NOT NULL REFERENCES workers(instance_id) ON DELETE CASCADE, repository TEXT NOT NULL, PRIMARY KEY(worker_instance,repository));
CREATE TABLE IF NOT EXISTS known_repositories (repository TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS attempts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL,
 profile TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '', worker_instance TEXT NOT NULL DEFAULT '', worker_name TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, lease_token TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT,
 exit_code INTEGER, error TEXT NOT NULL DEFAULT '', error_class TEXT NOT NULL DEFAULT '', result TEXT, events TEXT,
 duration_millis INTEGER, token_usage INTEGER, herdr_session TEXT NOT NULL DEFAULT '', herdr_workspace_id TEXT NOT NULL DEFAULT '', herdr_tab_id TEXT NOT NULL DEFAULT '', herdr_pane_id TEXT NOT NULL DEFAULT '', herdr_agent_name TEXT NOT NULL DEFAULT '', UNIQUE(run_id,ordinal));
CREATE INDEX IF NOT EXISTS attempts_run ON attempts(run_id,ordinal);
CREATE TABLE IF NOT EXISTS trigger_state (identity TEXT PRIMARY KEY,family TEXT NOT NULL,config_signature TEXT NOT NULL,generation_id TEXT NOT NULL,next_due_at TEXT,pending_occurrence_at TEXT,last_attempt_at TEXT,last_success_at TEXT,last_job_state TEXT NOT NULL DEFAULT '',last_job_error TEXT NOT NULL DEFAULT '',health TEXT NOT NULL DEFAULT 'healthy',latest_error TEXT NOT NULL DEFAULT '',candidate_count INTEGER NOT NULL DEFAULT 0,admission_count INTEGER NOT NULL DEFAULT 0,coalesced_count INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS github_trigger_requests (trigger_identity TEXT NOT NULL,occurrence_key TEXT NOT NULL,config_generation TEXT NOT NULL,repository TEXT NOT NULL,issue_number INTEGER NOT NULL,subject TEXT NOT NULL,actor TEXT NOT NULL,request_label TEXT NOT NULL,requested_at TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','admitted','rejected')),job_id TEXT,needs_reconciliation INTEGER NOT NULL DEFAULT 1,updated_at TEXT NOT NULL,PRIMARY KEY(trigger_identity,occurrence_key));
CREATE UNIQUE INDEX IF NOT EXISTS jobs_trigger_occurrence ON jobs(trigger_identity,occurrence_key) WHERE trigger_identity<>'' AND occurrence_key<>'';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_fixed_trigger ON jobs(trigger_identity) WHERE fixed_trigger=1 AND state IN ('queued','running');
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_trigger_subject ON jobs(trigger_subject) WHERE trigger_subject<>'' AND state IN ('queued','running');
CREATE INDEX IF NOT EXISTS github_trigger_requests_reconciliation ON github_trigger_requests(trigger_identity,needs_reconciliation,requested_at);
PRAGMA user_version=8;`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	return nil
}

func (s *Store) upgradeToVersionThree(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workersTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workers'`).Scan(&workersTable); err != nil {
		return err
	}
	for _, column := range []string{"environment_json", "profiles_json"} {
		if workersTable == 0 {
			break
		}
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('workers') WHERE name=?`, column).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE workers ADD COLUMN `+column+` TEXT NOT NULL DEFAULT '{}'`); err != nil {
				return err
			}
		}
	}
	var runsTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&runsTable); err != nil {
		return err
	}
	if runsTable > 0 {
		for _, definition := range []string{
			"route TEXT NOT NULL DEFAULT ''", "profile TEXT NOT NULL DEFAULT ''",
			"harness TEXT NOT NULL DEFAULT ''", "provider TEXT NOT NULL DEFAULT ''",
			"auth_mode TEXT NOT NULL DEFAULT ''", "role TEXT NOT NULL DEFAULT ''",
			"candidate_profiles TEXT NOT NULL DEFAULT '[]'", "max_attempts INTEGER NOT NULL DEFAULT 1",
			"fallback_on TEXT NOT NULL DEFAULT '[]'",
		} {
			column := strings.Fields(definition)[0]
			var present int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name=?`, column).Scan(&present); err != nil {
				return err
			}
			if present == 0 {
				if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN `+definition); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) upgradeToVersionFour(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runsTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&runsTable); err != nil {
		return err
	}
	for _, definition := range []string{
		"current_attempt_id TEXT NOT NULL DEFAULT ''",
		"attempt_count INTEGER NOT NULL DEFAULT 0",
		"last_error_class TEXT NOT NULL DEFAULT ''",
		"next_candidate INTEGER NOT NULL DEFAULT 0",
	} {
		if runsTable == 0 {
			break
		}
		column := strings.Fields(definition)[0]
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name=?`, column).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN `+definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attempts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL,
 profile TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '', worker_instance TEXT NOT NULL DEFAULT '', worker_name TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, lease_token TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT,
 exit_code INTEGER, error TEXT NOT NULL DEFAULT '', error_class TEXT NOT NULL DEFAULT '', result TEXT, events TEXT,
 duration_millis INTEGER, token_usage INTEGER, UNIQUE(run_id,ordinal));
CREATE INDEX IF NOT EXISTS attempts_run ON attempts(run_id,ordinal);
PRAGMA user_version=4;`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) upgradeToVersionFive(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runsTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&runsTable); err != nil {
		return err
	}
	if runsTable > 0 {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name='cancel_requested'`).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) upgradeToVersionSix(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runsTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&runsTable); err != nil {
		return err
	}
	if runsTable > 0 {
		// Version 5 stored one attempt for legacy commands. Version 6 keeps one
		// bounded lease-loss recovery without changing explicit route budgets.
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET max_attempts=? WHERE route='' AND max_attempts=1 AND state IN ('queued','running')`, defaultLegacyMaxAttempts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=6`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) upgradeToVersionSeven(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runsTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&runsTable); err != nil {
		return err
	}
	if runsTable > 0 {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name='max_total_tokens'`).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN max_total_tokens INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=7`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) upgradeToVersionEight(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	columns := map[string][]string{
		"jobs": {
			"execution_mode TEXT NOT NULL DEFAULT 'process'",
			"origin TEXT NOT NULL DEFAULT 'machinist'",
		},
		"workers": {"transports_json TEXT NOT NULL DEFAULT '[\"process\"]'"},
		"attempts": {
			"herdr_session TEXT NOT NULL DEFAULT ''", "herdr_workspace_id TEXT NOT NULL DEFAULT ''",
			"herdr_tab_id TEXT NOT NULL DEFAULT ''", "herdr_pane_id TEXT NOT NULL DEFAULT ''",
			"herdr_agent_name TEXT NOT NULL DEFAULT ''",
		},
	}
	for table, definitions := range columns {
		var presentTable int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&presentTable); err != nil {
			return err
		}
		if presentTable == 0 {
			continue
		}
		for _, definition := range definitions {
			column := strings.Fields(definition)[0]
			var present int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name=?`, column).Scan(&present); err != nil {
				return err
			}
			if present == 0 {
				if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+definition); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=8`); err != nil {
		return err
	}
	return tx.Commit()
}

// upgradeToVersionTwo removes the Shepherd schedule bookkeeping. Finished jobs
// keep their rows. Queued schedule jobs are failed so they cannot overlap a
// replacement trigger, and a running one blocks the upgrade until it finishes.
// The steps run in one transaction and skip columns that are already gone, so
// an interrupted upgrade completes on the next start.
func (s *Store) upgradeToVersionTwo(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS jobs_active_shepherd_repository; DROP TABLE IF EXISTS schedule_state;`); err != nil {
		return err
	}
	for _, column := range []string{"schedule_name", "has_shepherd"} {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name=?`, column).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			continue
		}
		if column == "schedule_name" {
			if err := s.drainScheduledJobs(ctx, tx); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE jobs DROP COLUMN `+column); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=2`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) drainScheduledJobs(ctx context.Context, tx *sql.Tx) error {
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE schedule_name<>'' AND state='running'`).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return fmt.Errorf("%d scheduled Shepherd jobs are still running; let them finish and start again", running)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	const reason = "cancelled by schema upgrade: shepherd schedules were removed"
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='failed',exit_code=1,error=?,completed_at=? WHERE state='queued' AND job_id IN (SELECT id FROM jobs WHERE schedule_name<>'' AND state='queued')`, reason, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',updated_at=? WHERE schedule_name<>'' AND state='queued'`, now)
	return err
}

type CreateJobOptions struct {
	ExecutionMode string
	Origin        string
}

func (s *Store) CreateJob(ctx context.Context, prompt, repository, name string, command config.ResolvedCommand) (string, error) {
	return s.CreateJobWithOptions(ctx, prompt, repository, name, command, CreateJobOptions{})
}

func (s *Store) CreateJobWithOptions(ctx context.Context, prompt, repository, name string, command config.ResolvedCommand, options CreateJobOptions) (string, error) {
	if command.Name == "" {
		return "", errors.New("job must contain one command")
	}
	executionMode := strings.ToLower(strings.TrimSpace(options.ExecutionMode))
	if executionMode == "" {
		executionMode = "process"
	}
	if executionMode != "process" && executionMode != "herdr" {
		return "", errors.New("execution mode must be process or herdr")
	}
	origin := strings.ToLower(strings.TrimSpace(options.Origin))
	if origin == "" {
		origin = "machinist"
	}
	if len(origin) > 64 || !validPortableIdentifier(origin) {
		return "", errors.New("job origin must be a portable identifier")
	}
	jobID, err := randomID("job", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,execution_mode,origin,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'queued',?,?)`, jobID, prompt, repository, name, executionMode, origin, now, now); err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	runID, err := randomID("run", 12)
	if err != nil {
		return "", err
	}
	candidates, fallbackOn, err := routeFields(command)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state,route,profile,harness,provider,auth_mode,role,candidate_profiles,max_attempts,max_total_tokens,fallback_on) VALUES(?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?,?,?,?)`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, repository, command.Prompt, command.Timeout.Milliseconds(), command.Route, command.Profile, command.Harness, command.Provider, command.AuthMode, command.Role, candidates, runMaxAttempts(command), command.MaxTotalTokens, fallbackOn); err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit job: %w", err)
	}
	return jobID, nil
}

func validPortableIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

// SyncTriggers makes the durable trigger set match the resolved configuration. Existing
// state is preserved only when both family and configuration signature are unchanged.
func (s *Store) SyncTriggers(ctx context.Context, definitions []TriggerDefinition) error {
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.Identity == "" || definition.Family == "" || definition.ConfigSignature == "" {
			return errors.New("trigger identity, family, and configuration signature are required")
		}
		if seen[definition.Identity] {
			return fmt.Errorf("duplicate trigger identity %q", definition.Identity)
		}
		seen[definition.Identity] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, 0, len(definitions))
	identities := make([]any, 0, len(definitions))
	for _, definition := range definitions {
		placeholders = append(placeholders, "?")
		identities = append(identities, definition.Identity)
		generationID, err := randomID("trigger", 12)
		if err != nil {
			return fmt.Errorf("generate trigger %q configuration generation: %w", definition.Identity, err)
		}
		// A changed family or signature discards every piece of durable state, so the
		// row is recreated from scratch. An unchanged trigger keeps its schedule and
		// generation, gaining a generation only if it never had one.
		if _, err := tx.ExecContext(ctx, `DELETE FROM trigger_state WHERE identity=? AND (family<>? OR config_signature<>?)`, definition.Identity, definition.Family, definition.ConfigSignature); err != nil {
			return fmt.Errorf("sync trigger %q: %w", definition.Identity, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO trigger_state(identity,family,config_signature,generation_id,next_due_at,health,updated_at)
VALUES(?,?,?,?,?,'healthy',?)
ON CONFLICT(identity) DO UPDATE SET
  generation_id=CASE WHEN trigger_state.generation_id='' THEN excluded.generation_id ELSE trigger_state.generation_id END,
  updated_at=excluded.updated_at`, definition.Identity, definition.Family, definition.ConfigSignature, generationID, nullableTimeText(definition.NextDueAt), now); err != nil {
			return fmt.Errorf("sync trigger %q: %w", definition.Identity, err)
		}
	}
	removeStale := `DELETE FROM trigger_state`
	if len(identities) > 0 {
		removeStale += ` WHERE identity NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, removeStale, identities...); err != nil {
		return fmt.Errorf("remove stale triggers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trigger sync: %w", err)
	}
	return nil
}

// CreateTriggeredJob durably admits an occurrence and its job in one transaction.
// Duplicate occurrences are idempotent. Fixed schedules coalesce a new occurrence while
// active work exists; subject-based triggers wait so a later retry can admit the event.
func (s *Store) CreateTriggeredJob(ctx context.Context, admission TriggerAdmission) (string, bool, error) {
	if admission.Identity == "" || admission.Family == "" {
		return "", false, errors.New("trigger identity and family are required")
	}
	if admission.ConfigSignature == "" {
		return "", false, errors.New("trigger config signature is required")
	}
	if admission.ConfigGeneration == "" {
		return "", false, errors.New("trigger config generation is required")
	}
	if admission.Command.Name == "" {
		return "", false, errors.New("triggered job must contain one command")
	}
	fixed := fixedTriggerFamily(admission.Family)
	if admission.OccurrenceKey == "" && fixed && !admission.ScheduledAt.IsZero() {
		admission.OccurrenceKey = admission.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	if admission.OccurrenceKey == "" {
		return "", false, errors.New("trigger occurrence key is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var family, configSignature, configGeneration string
	if err := tx.QueryRowContext(ctx, `SELECT family,config_signature,generation_id FROM trigger_state WHERE identity=?`, admission.Identity).Scan(&family, &configSignature, &configGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("%w: %s", ErrTriggerMissing, admission.Identity)
		}
		return "", false, fmt.Errorf("read trigger %q: %w", admission.Identity, err)
	}
	if family != admission.Family {
		return "", false, fmt.Errorf("trigger %q family is %q, not %q", admission.Identity, family, admission.Family)
	}
	if configSignature != admission.ConfigSignature {
		return "", false, fmt.Errorf("%w: trigger %q configuration signature changed before admission", ErrTriggerStale, admission.Identity)
	}
	if configGeneration != admission.ConfigGeneration {
		return "", false, fmt.Errorf("%w: %s", ErrTriggerStale, admission.Identity)
	}
	if admission.Family == "github" {
		if admission.GitHubRepository == "" || admission.GitHubIssueNumber <= 0 || admission.Subject == "" || admission.RequestActor == "" || admission.RequestLabel == "" || admission.ScheduledAt.IsZero() {
			return "", false, errors.New("github trigger request metadata is required")
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO github_trigger_requests(trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,needs_reconciliation,updated_at)
VALUES(?,?,?,?,?,?,?,?,?, 'pending',1,?)
ON CONFLICT(trigger_identity,occurrence_key) DO UPDATE SET
  config_generation=excluded.config_generation,
  repository=excluded.repository,
  issue_number=excluded.issue_number,
  subject=excluded.subject,
  actor=excluded.actor,
  request_label=excluded.request_label,
  requested_at=excluded.requested_at,
  needs_reconciliation=1,
  updated_at=excluded.updated_at
WHERE github_trigger_requests.state='pending'`, admission.Identity, admission.OccurrenceKey, admission.ConfigGeneration, admission.GitHubRepository, admission.GitHubIssueNumber, admission.Subject, admission.RequestActor, admission.RequestLabel, admission.ScheduledAt.UTC().Format(time.RFC3339Nano), now); err != nil {
			return "", false, fmt.Errorf("persist github trigger request: %w", err)
		}
	}

	var existingJob string
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE trigger_identity=? AND occurrence_key=?`, admission.Identity, admission.OccurrenceKey).Scan(&existingJob)
	if err == nil {
		if admission.Family == "github" {
			if admission.GitHubIssueTitle != "" {
				if _, updateErr := tx.ExecContext(ctx, `UPDATE jobs SET github_issue_title=? WHERE id=? AND github_issue_title=''`, admission.GitHubIssueTitle, existingJob); updateErr != nil {
					return "", false, fmt.Errorf("update existing github job title: %w", updateErr)
				}
			}
			if _, updateErr := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET state='admitted',job_id=?,config_generation=?,needs_reconciliation=1,updated_at=? WHERE trigger_identity=? AND occurrence_key=?`, existingJob, admission.ConfigGeneration, s.now().UTC().Format(time.RFC3339Nano), admission.Identity, admission.OccurrenceKey); updateErr != nil {
				return "", false, fmt.Errorf("repair github trigger request: %w", updateErr)
			}
		}
		if fixed {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE trigger_state SET pending_occurrence_at=NULL,next_due_at=COALESCE(?,next_due_at),updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), s.now().UTC().Format(time.RFC3339Nano), admission.Identity); updateErr != nil {
				return "", false, fmt.Errorf("finish duplicate trigger occurrence: %w", updateErr)
			}
		}
		return existingJob, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("read trigger occurrence: %w", err)
	}

	if fixed {
		var activeGeneration string
		err = tx.QueryRowContext(ctx, `SELECT id,trigger_generation_id FROM jobs WHERE trigger_identity=? AND fixed_trigger=1 AND state IN ('queued','running')`, admission.Identity).Scan(&existingJob, &activeGeneration)
		if err == nil {
			if activeGeneration != admission.ConfigGeneration {
				return existingJob, false, fmt.Errorf("%w: %s", ErrTriggerPreviousGenerationActive, admission.Identity)
			}
			now := s.now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=COALESCE(?,next_due_at),pending_occurrence_at=NULL,last_attempt_at=?,health='coalesced',latest_error='',coalesced_count=coalesced_count+1,updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), now, now, admission.Identity); err != nil {
				return "", false, fmt.Errorf("coalesce trigger %q: %w", admission.Identity, err)
			}
			return existingJob, false, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("read active trigger job: %w", err)
		}
	}
	if admission.Subject != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE trigger_subject=? AND state IN ('queued','running')`, admission.Subject).Scan(&existingJob)
		if err == nil {
			if admission.Family == "github" && admission.GitHubIssueTitle != "" {
				if _, updateErr := tx.ExecContext(ctx, `UPDATE jobs SET github_issue_title=? WHERE id=? AND github_issue_title=''`, admission.GitHubIssueTitle, existingJob); updateErr != nil {
					return "", false, fmt.Errorf("update existing github job title: %w", updateErr)
				}
			}
			return existingJob, false, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("read active trigger subject: %w", err)
		}
	}

	jobID, err := randomID("job", 12)
	if err != nil {
		return "", false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,trigger_identity,trigger_config_signature,trigger_generation_id,occurrence_key,trigger_subject,github_issue_title,fixed_trigger,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, jobID, admission.Prompt, admission.Repository, admission.SelectionName, admission.Identity, admission.ConfigSignature, admission.ConfigGeneration, admission.OccurrenceKey, admission.Subject, admission.GitHubIssueTitle, fixed, now, now); err != nil {
		return "", false, fmt.Errorf("insert triggered job: %w", err)
	}
	if admission.Family == "github" {
		if _, err := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET state='admitted',job_id=?,needs_reconciliation=1,updated_at=? WHERE trigger_identity=? AND occurrence_key=?`, jobID, now, admission.Identity, admission.OccurrenceKey); err != nil {
			return "", false, fmt.Errorf("admit github trigger request: %w", err)
		}
	}
	runID, err := randomID("run", 12)
	if err != nil {
		return "", false, err
	}
	command := admission.Command
	candidates, fallbackOn, err := routeFields(command)
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state,route,profile,harness,provider,auth_mode,role,candidate_profiles,max_attempts,max_total_tokens,fallback_on) VALUES(?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?,?,?,?)`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, admission.Repository, command.Prompt, command.Timeout.Milliseconds(), command.Route, command.Profile, command.Harness, command.Provider, command.AuthMode, command.Role, candidates, runMaxAttempts(command), command.MaxTotalTokens, fallbackOn); err != nil {
		return "", false, fmt.Errorf("insert triggered run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=COALESCE(?,next_due_at),pending_occurrence_at=NULL,last_attempt_at=?,health='active',latest_error='',admission_count=admission_count+1,updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), now, now, admission.Identity)
	if err != nil {
		return "", false, fmt.Errorf("update trigger admission: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if changed != 1 {
		return "", false, fmt.Errorf("%w: %s", ErrTriggerMissing, admission.Identity)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit triggered job: %w", err)
	}
	return jobID, true, nil
}

// RecordTriggerAttempt records one poll or scheduling attempt. Candidate counts are
// cumulative. Failed attempts do not advance the occurrence or next due time.
func (s *Store) RecordTriggerAttempt(ctx context.Context, identity, configGeneration string, candidates int, attemptErr error) error {
	if candidates < 0 {
		return errors.New("trigger candidate count cannot be negative")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	health := "healthy"
	latestError := ""
	if attemptErr != nil {
		health = "failed"
		latestError = boundedTriggerError(attemptErr.Error())
	}
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET
  last_attempt_at=?,
  candidate_count=candidate_count+?,
  health=CASE
    WHEN ?='healthy' AND health='coalesced' THEN 'coalesced'
	WHEN ?='healthy' AND EXISTS (SELECT 1 FROM jobs WHERE jobs.trigger_identity=trigger_state.identity AND jobs.trigger_generation_id=trigger_state.generation_id AND jobs.state IN ('queued','running')) THEN 'active'
	WHEN ?='healthy' AND last_job_state='failed' THEN 'failed'
    ELSE ?
  END,
  latest_error=CASE
	WHEN ?='healthy' AND last_job_state='failed' THEN last_job_error
    ELSE ?
  END,
  updated_at=?
WHERE identity=? AND generation_id=?`, now, candidates, health, health, health, health, health, latestError, now, identity, configGeneration)
	if err != nil {
		return fmt.Errorf("record trigger %q attempt: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) SetTriggerNextDue(ctx context.Context, identity, configGeneration string, nextDue time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=?,updated_at=? WHERE identity=? AND generation_id=?`, nullableTimeText(nextDue), s.now().UTC().Format(time.RFC3339Nano), identity, configGeneration)
	if err != nil {
		return fmt.Errorf("set trigger %q next due time: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) SetTriggerPendingOccurrence(ctx context.Context, identity, configGeneration string, occurrence time.Time) error {
	if occurrence.IsZero() {
		return errors.New("trigger pending occurrence is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET pending_occurrence_at=?,updated_at=? WHERE identity=? AND generation_id=?`, nullableTimeText(occurrence), s.now().UTC().Format(time.RFC3339Nano), identity, configGeneration)
	if err != nil {
		return fmt.Errorf("set trigger %q pending occurrence: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

// TriggerOccurrenceExists distinguishes an idempotent admission from an occurrence that
// is still waiting behind active work for the same subject.
func (s *Store) TriggerOccurrenceExists(ctx context.Context, identity, occurrenceKey string) (bool, error) {
	if identity == "" || occurrenceKey == "" {
		return false, errors.New("trigger identity and occurrence key are required")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE trigger_identity=? AND occurrence_key=?)`, identity, occurrenceKey).Scan(&exists); err != nil {
		return false, fmt.Errorf("read trigger occurrence: %w", err)
	}
	return exists == 1, nil
}

// RejectGitHubTriggerRequest durably consumes an unauthorized request before
// its label is removed. Reconciliation remains pending until GitHub confirms
// that no newer request event was hidden by the label transition.
func (s *Store) RejectGitHubTriggerRequest(ctx context.Context, request GitHubTriggerRequest) error {
	if request.TriggerIdentity == "" || request.OccurrenceKey == "" || request.ConfigGeneration == "" || request.Repository == "" || request.IssueNumber <= 0 || request.Subject == "" || request.Actor == "" || request.RequestLabel == "" || request.RequestedAt.IsZero() {
		return errors.New("complete github trigger request metadata is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation string
	if err := tx.QueryRowContext(ctx, `SELECT generation_id FROM trigger_state WHERE identity=?`, request.TriggerIdentity).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrTriggerMissing, request.TriggerIdentity)
		}
		return err
	}
	if generation != request.ConfigGeneration {
		return fmt.Errorf("%w: %s", ErrTriggerStale, request.TriggerIdentity)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_trigger_requests(trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,needs_reconciliation,updated_at)
VALUES(?,?,?,?,?,?,?,?,?, 'rejected',1,?)
ON CONFLICT(trigger_identity,occurrence_key) DO UPDATE SET
  state=CASE WHEN github_trigger_requests.state='admitted' THEN 'admitted' ELSE 'rejected' END,
  config_generation=excluded.config_generation,
  request_label=excluded.request_label,
  needs_reconciliation=1,
  updated_at=excluded.updated_at`, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration, request.Repository, request.IssueNumber, request.Subject, request.Actor, request.RequestLabel, request.RequestedAt.UTC().Format(time.RFC3339Nano), now); err != nil {
		return fmt.Errorf("persist rejected github trigger request: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GitHubTriggerReconciliations(ctx context.Context, identity string) ([]GitHubTriggerRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,COALESCE(job_id,'')
FROM github_trigger_requests
WHERE trigger_identity=? AND needs_reconciliation=1
ORDER BY requested_at, occurrence_key`, identity)
	if err != nil {
		return nil, fmt.Errorf("read github trigger reconciliations: %w", err)
	}
	defer rows.Close()
	var requests []GitHubTriggerRequest
	for rows.Next() {
		var request GitHubTriggerRequest
		var requestedAt string
		if err := rows.Scan(&request.TriggerIdentity, &request.OccurrenceKey, &request.ConfigGeneration, &request.Repository, &request.IssueNumber, &request.Subject, &request.Actor, &request.RequestLabel, &requestedAt, &request.State, &request.JobID); err != nil {
			return nil, fmt.Errorf("read github trigger reconciliation: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, requestedAt)
		if err != nil {
			return nil, fmt.Errorf("parse github trigger request time: %w", err)
		}
		request.RequestedAt = parsed
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s *Store) CompleteGitHubTriggerReconciliation(ctx context.Context, identity, occurrenceKey, generation string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE github_trigger_requests SET needs_reconciliation=0,updated_at=? WHERE trigger_identity=? AND occurrence_key=? AND config_generation=?`, s.now().UTC().Format(time.RFC3339Nano), identity, occurrenceKey, generation)
	if err != nil {
		return fmt.Errorf("complete github trigger reconciliation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) AddTriggerCoalesced(ctx context.Context, identity, configGeneration string, count int64) error {
	if count < 0 {
		return errors.New("trigger coalesced count cannot be negative")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET coalesced_count=coalesced_count+?,health=CASE WHEN ?>0 THEN 'coalesced' ELSE health END,updated_at=? WHERE identity=? AND generation_id=?`, count, count, now, identity, configGeneration)
	if err != nil {
		return fmt.Errorf("record trigger %q coalesced occurrences: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) Poll(ctx context.Context, request protocol.PollRequest) (*protocol.RunSpec, error) {
	return s.poll(ctx, request, 0)
}

// poll allows at most maxConcurrentJobs running jobs. Zero leaves concurrency
// unlimited. Expired leases remain eligible so interrupted work can make progress.
func (s *Store) poll(ctx context.Context, request protocol.PollRequest, maxConcurrentJobs int) (*protocol.RunSpec, error) {
	if maxConcurrentJobs < 0 {
		return nil, errors.New("max concurrent jobs cannot be negative")
	}
	if err := request.Environment.Validate(); err != nil {
		return nil, err
	}
	environmentJSON, err := json.Marshal(request.Environment)
	if err != nil {
		return nil, fmt.Errorf("encode worker environment: %w", err)
	}
	profilesJSON, err := json.Marshal(request.Profiles)
	if err != nil {
		return nil, fmt.Errorf("encode worker profiles: %w", err)
	}
	if len(profilesJSON) > 64<<10 {
		return nil, errors.New("worker profile capabilities exceed 65536 bytes")
	}
	transports := request.Transports
	if len(transports) == 0 {
		transports = []string{"process"}
	}
	transportSet := stringSet(transports)
	for transport := range transportSet {
		if transport != "process" && transport != "herdr" {
			return nil, fmt.Errorf("unsupported worker transport %q", transport)
		}
	}
	transportsJSON, err := json.Marshal(sortedSet(transportSet))
	if err != nil {
		return nil, fmt.Errorf("encode worker transports: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers(instance_id,name,last_seen_at,environment_json,profiles_json,transports_json) VALUES(?,?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET name=excluded.name,last_seen_at=excluded.last_seen_at,environment_json=excluded.environment_json,profiles_json=excluded.profiles_json,transports_json=excluded.transports_json`, request.InstanceID, request.Name, now, string(environmentJSON), string(profilesJSON), string(transportsJSON)); err != nil {
		return nil, fmt.Errorf("update worker: %w", err)
	}
	if _, err := recoverExpiredRuns(ctx, tx, nowTime); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_repositories WHERE worker_instance=?`, request.InstanceID); err != nil {
		return nil, fmt.Errorf("clear worker repositories: %w", err)
	}
	repositories := stringSet(request.Repositories)
	for repository := range repositories {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO known_repositories(repository) VALUES(?)`, repository); err != nil {
			return nil, fmt.Errorf("store known repository: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_repositories(worker_instance,repository) VALUES(?,?)`, request.InstanceID, repository); err != nil {
			return nil, fmt.Errorf("store worker repository: %w", err)
		}
	}
	if err := reconcileKnownRepositories(ctx, tx); err != nil {
		return nil, err
	}
	active, err := scanRunSpec(tx.QueryRowContext(ctx, `SELECT r.id,r.job_id,r.command,r.command_hash,r.executor,r.model,r.repository,r.rendered_prompt,r.timeout_ms,r.lease_token,r.route,r.profile,r.harness,r.provider,r.auth_mode,r.role,r.candidate_profiles,r.max_attempts,r.max_total_tokens,r.fallback_on,r.current_attempt_id,r.attempt_count,r.last_error_class,j.execution_mode,j.origin FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.worker_instance=? AND r.state='running' LIMIT 1`, request.InstanceID))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	atCapacity := false
	if maxConcurrentJobs > 0 {
		var runningJobs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state='running'`).Scan(&runningJobs); err != nil {
			return nil, fmt.Errorf("count running jobs: %w", err)
		}
		atCapacity = runningJobs >= maxConcurrentJobs
	}

	executors := stringSet(request.Executors)
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.job_id,r.command,r.command_hash,r.executor,r.model,r.repository,r.rendered_prompt,r.timeout_ms,r.route,r.profile,r.harness,r.provider,r.auth_mode,r.role,r.candidate_profiles,r.max_attempts,r.max_total_tokens,r.fallback_on,r.attempt_count,r.next_candidate,r.last_error_class,j.state,j.execution_mode,j.origin FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.state='queued' ORDER BY r.rowid`)
	if err != nil {
		return nil, err
	}
	var selected protocol.RunSpec
	routeAvailability := make(map[string]map[string]routeProfileAvailability)
	for rows.Next() {
		var candidate protocol.RunSpec
		var jobState, candidateProfiles, fallbackOn string
		var nextCandidate int
		if err := rows.Scan(&candidate.ID, &candidate.JobID, &candidate.Command, &candidate.CommandHash, &candidate.Executor, &candidate.Model, &candidate.Repository, &candidate.RenderedPrompt, &candidate.TimeoutMillis, &candidate.Route, &candidate.Profile, &candidate.Harness, &candidate.Provider, &candidate.AuthMode, &candidate.Role, &candidateProfiles, &candidate.MaxAttempts, &candidate.MaxTotalTokens, &fallbackOn, &candidate.AttemptNumber, &nextCandidate, &candidate.PreviousErrorClass, &jobState, &candidate.ExecutionMode, &candidate.Origin); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(candidateProfiles), &candidate.Candidates); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode run candidate profiles: %w", err)
		}
		if err := json.Unmarshal([]byte(fallbackOn), &candidate.FallbackOn); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode run fallback policy: %w", err)
		}
		if atCapacity && jobState != "running" {
			continue
		}
		if !transportSet[candidate.ExecutionMode] {
			continue
		}
		if candidate.Route != "" {
			availabilityKey := candidate.Repository + "\x00" + candidate.ExecutionMode
			available, ok := routeAvailability[availabilityKey]
			if !ok {
				available, err = connectedRouteProfiles(ctx, tx, candidate.Repository, candidate.ExecutionMode, nowTime.Add(-routeAvailabilityWindow))
				if err != nil {
					rows.Close()
					return nil, err
				}
				for executor := range executors {
					capability := available[executor]
					if capability.models == nil {
						capability.models = make(map[string]bool)
					}
					for _, model := range request.Models[executor] {
						capability.models[model] = true
					}
					available[executor] = capability
				}
				routeAvailability[availabilityKey] = available
			}
			preferred := selectPreferredRouteProfile(candidate.Candidates, nextCandidate, candidate.Model, available)
			candidate.Executor = selectRouteProfile(candidate.Candidates, nextCandidate, executors, request.Models, candidate.Model)
			if preferred == "" || candidate.Executor != preferred {
				continue
			}
			candidate.Profile = candidate.Executor
		}
		if executors[candidate.Executor] && repositories[candidate.Repository] && supportsModel(request.Models, candidate.Executor, candidate.Model) {
			if profile, ok := request.Profiles[candidate.Executor]; ok {
				candidate.Profile = candidate.Executor
				candidate.Harness = profile.Harness
				candidate.Provider = profile.Provider
				candidate.AuthMode = profile.AuthMode
			}
			selected = candidate
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if selected.ID == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	selected.LeaseToken, err = randomID("lease", 24)
	if err != nil {
		return nil, err
	}
	selected.AttemptID, err = randomID("attempt", 12)
	if err != nil {
		return nil, err
	}
	selected.AttemptNumber++
	expiresAt := nowTime.Add(leaseDuration).UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET state='running',worker_instance=?,worker_name=?,lease_token=?,lease_expires_at=?,started_at=?,executor=?,profile=?,harness=?,provider=?,auth_mode=?,current_attempt_id=?,attempt_count=? WHERE id=? AND state='queued'`, request.InstanceID, request.Name, selected.LeaseToken, expiresAt, now, selected.Executor, selected.Profile, selected.Harness, selected.Provider, selected.AuthMode, selected.AttemptID, selected.AttemptNumber, selected.ID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, fmt.Errorf("lease run: concurrent state change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempts(id,run_id,ordinal,profile,harness,provider,model,worker_instance,worker_name,state,lease_token,started_at) VALUES(?,?,?,?,?,?,?,?,?,'running',?,?)`, selected.AttemptID, selected.ID, selected.AttemptNumber, selected.Profile, selected.Harness, selected.Provider, selected.Model, request.InstanceID, request.Name, selected.LeaseToken, now); err != nil {
		return nil, fmt.Errorf("create run attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='running',updated_at=? WHERE id=? AND state='queued'`, now, selected.JobID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &selected, nil
}

func (s *Store) Complete(ctx context.Context, runID string, completion protocol.Completion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var jobID, state, instanceID, leaseToken, startedAt, triggerIdentity, triggerGeneration string
	var currentAttemptID, profile, candidateProfiles, fallbackOn string
	var attemptCount, maxAttempts int
	var maxTotalTokens int64
	var cancelRequested bool
	var leaseExpiresAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT r.job_id,r.state,COALESCE(r.worker_instance,''),COALESCE(r.lease_token,''),r.lease_expires_at,COALESCE(r.started_at,''),COALESCE(j.trigger_identity,''),COALESCE(j.trigger_generation_id,''),r.current_attempt_id,r.profile,r.candidate_profiles,r.fallback_on,r.attempt_count,r.max_attempts,r.max_total_tokens,r.cancel_requested FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.id=?`, runID).Scan(&jobID, &state, &instanceID, &leaseToken, &leaseExpiresAt, &startedAt, &triggerIdentity, &triggerGeneration, &currentAttemptID, &profile, &candidateProfiles, &fallbackOn, &attemptCount, &maxAttempts, &maxTotalTokens, &cancelRequested); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(instanceID), []byte(completion.InstanceID)) != 1 || subtle.ConstantTimeCompare([]byte(leaseToken), []byte(completion.LeaseToken)) != 1 {
		return ErrLeaseConflict
	}
	if completion.AttemptID != "" && subtle.ConstantTimeCompare([]byte(currentAttemptID), []byte(completion.AttemptID)) != 1 {
		return ErrLeaseConflict
	}
	if terminalRunState(state) {
		return tx.Commit()
	}
	if state != "running" {
		return ErrRunState
	}
	if !leaseExpiresAt.Valid || leaseExpiresAt.Int64 <= s.now().UTC().UnixNano() {
		return ErrLeaseConflict
	}
	if cancelRequested {
		completion.State = "cancelled"
		completion.ExitCode = 130
		completion.ErrorClass = "cancelled"
		completion.Error = "cancelled by operator"
	}
	if !terminalRunState(completion.State) {
		return fmt.Errorf("%w: invalid terminal run state %q", ErrInvalidCompletion, completion.State)
	}
	if err := validateOutcome(completion.State, completion.ExitCode); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCompletion, err)
	}
	completion.ErrorClass = strings.TrimSpace(completion.ErrorClass)
	if completion.State == "succeeded" && completion.ErrorClass != "" {
		return fmt.Errorf("%w: succeeded run cannot have an error class", ErrInvalidCompletion)
	}
	if completion.State != "succeeded" && completion.ErrorClass == "" {
		completion.ErrorClass = "unknown"
	}
	if completion.ErrorClass != "" && !validErrorClass(completion.ErrorClass) {
		return fmt.Errorf("%w: invalid error class %q", ErrInvalidCompletion, completion.ErrorClass)
	}
	durationMillis, tokenUsage := resultMetrics(completion.Result)
	completedAt := s.now().UTC()
	now := completedAt.Format(time.RFC3339Nano)
	if durationMillis == nil {
		durationMillis = elapsedMillis(startedAt, now)
	}
	if currentAttemptID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE attempts SET state=?,exit_code=?,error=?,error_class=?,result=?,events=?,completed_at=?,duration_millis=?,token_usage=? WHERE id=? AND state='running' AND lease_token=?`, completion.State, completion.ExitCode, completion.Error, completion.ErrorClass, string(completion.Result), completion.Events, now, durationMillis, tokenUsage, currentAttemptID, completion.LeaseToken)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrLeaseConflict
		}
	}
	aggregateDuration, aggregateTokens, aggregateCount, err := attemptAggregateMetrics(ctx, tx, runID)
	if err != nil {
		return err
	}
	if aggregateCount > 0 {
		durationMillis = aggregateDuration
		tokenUsage = aggregateTokens
	}
	retry := retryAllowed(completion.State, completion.ErrorClass, fallbackOn, attemptCount, maxAttempts)
	if retry {
		var stopReason string
		retry, stopReason = tokenRetryAllowed(maxTotalTokens, tokenUsage)
		if stopReason != "" {
			completion.Error = appendRunError(completion.Error, stopReason)
		}
	}
	if retry {
		nextCandidate, err := nextRouteCandidate(candidateProfiles, profile)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='queued',worker_instance=NULL,worker_name='',lease_token=NULL,lease_expires_at=NULL,started_at=NULL,current_attempt_id='',exit_code=NULL,error=?,result=NULL,events=NULL,completed_at=NULL,duration_millis=?,token_usage=?,last_error_class=?,next_candidate=? WHERE id=?`, completion.Error, durationMillis, tokenUsage, completion.ErrorClass, nextCandidate, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='running',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state=?,exit_code=?,error=?,result=?,events=?,lease_expires_at=NULL,completed_at=?,duration_millis=?,token_usage=?,current_attempt_id='',last_error_class=? WHERE id=?`, completion.State, completion.ExitCode, completion.Error, string(completion.Result), completion.Events, now, durationMillis, tokenUsage, completion.ErrorClass, runID); err != nil {
		return err
	}
	if completion.State == "succeeded" {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
		if triggerIdentity != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_success_at=?,last_job_state='succeeded',last_job_error='',health='healthy',latest_error='',updated_at=? WHERE identity=? AND generation_id=?`, now, now, triggerIdentity, triggerGeneration); err != nil {
				return fmt.Errorf("record trigger success: %w", err)
			}
		}
	} else {
		jobState := "failed"
		if completion.State == "cancelled" {
			jobState = "cancelled"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state=?,updated_at=? WHERE id=?`, jobState, now, jobID); err != nil {
			return err
		}
		if triggerIdentity != "" {
			latestError := completion.Error
			if latestError == "" {
				latestError = "triggered job " + completion.State
			}
			latestError = boundedTriggerError(latestError)
			health := "failed"
			if completion.State == "cancelled" {
				health = "healthy"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_job_state=?,last_job_error=?,health=?,latest_error=?,updated_at=? WHERE identity=? AND generation_id=?`, jobState, latestError, health, latestError, now, triggerIdentity, triggerGeneration); err != nil {
				return fmt.Errorf("record trigger failure: %w", err)
			}
		}
	}
	return tx.Commit()
}

func (s *Store) BindTerminal(ctx context.Context, runID string, request protocol.BindTerminalRequest) error {
	for label, value := range map[string]string{
		"workspace_id": request.Terminal.WorkspaceID, "pane_id": request.Terminal.PaneID,
		"agent_name": request.Terminal.AgentName,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid terminal %s", label)
		}
	}
	for label, value := range map[string]string{"session": request.Terminal.Session, "tab_id": request.Terminal.TabID} {
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid terminal %s", label)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, instanceID, leaseToken, attemptID string
	var leaseExpiresAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at,current_attempt_id FROM runs WHERE id=?`, runID).Scan(&state, &instanceID, &leaseToken, &leaseExpiresAt, &attemptID); err != nil {
		return err
	}
	if state != "running" {
		return ErrRunState
	}
	if subtle.ConstantTimeCompare([]byte(instanceID), []byte(request.InstanceID)) != 1 || subtle.ConstantTimeCompare([]byte(leaseToken), []byte(request.LeaseToken)) != 1 || subtle.ConstantTimeCompare([]byte(attemptID), []byte(request.AttemptID)) != 1 {
		return ErrLeaseConflict
	}
	if !leaseExpiresAt.Valid || leaseExpiresAt.Int64 <= s.now().UTC().UnixNano() {
		return ErrLeaseConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE attempts SET herdr_session=?,herdr_workspace_id=?,herdr_tab_id=?,herdr_pane_id=?,herdr_agent_name=? WHERE id=? AND state='running' AND lease_token=?`, request.Terminal.Session, request.Terminal.WorkspaceID, request.Terminal.TabID, request.Terminal.PaneID, request.Terminal.AgentName, request.AttemptID, request.LeaseToken)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrLeaseConflict
	}
	return tx.Commit()
}

func validErrorClass(value string) bool {
	switch value {
	case "configuration", "authentication", "policy", "rate_limit", "capacity", "transient", "transport", "harness_crash", "timeout", "test_failure", "model_unavailable", "unknown", "cancelled":
		return true
	default:
		return false
	}
}

func retryAllowed(state, errorClass, fallbackJSON string, attemptCount, maxAttempts int) bool {
	if state != "failed" && state != "timed_out" {
		return false
	}
	if maxAttempts <= 1 || attemptCount >= maxAttempts {
		return false
	}
	var allowed []string
	if json.Unmarshal([]byte(fallbackJSON), &allowed) != nil {
		return false
	}
	return slices.Contains(allowed, errorClass)
}

func tokenRetryAllowed(maxTotalTokens int64, tokenUsage *int64) (bool, string) {
	if maxTotalTokens <= 0 {
		return true, ""
	}
	if tokenUsage == nil {
		return false, "retry stopped: max_total_tokens is configured but token usage coverage is incomplete"
	}
	if *tokenUsage >= maxTotalTokens {
		return false, fmt.Sprintf("retry stopped: reported token usage %d reached max_total_tokens %d", *tokenUsage, maxTotalTokens)
	}
	return true, ""
}

func appendRunError(current, detail string) string {
	current = strings.TrimSpace(current)
	if current == "" {
		return detail
	}
	return current + "; " + detail
}

func nextRouteCandidate(candidateJSON, profile string) (int, error) {
	var candidates []string
	if err := json.Unmarshal([]byte(candidateJSON), &candidates); err != nil {
		return 0, fmt.Errorf("decode run candidate profiles: %w", err)
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	for index, candidate := range candidates {
		if candidate == profile {
			return (index + 1) % len(candidates), nil
		}
	}
	return 0, nil
}

func (s *Store) Heartbeat(ctx context.Context, runID string, heartbeat protocol.Heartbeat) (protocol.HeartbeatResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	defer tx.Rollback()
	var state, instanceID, leaseToken string
	var cancelRequested bool
	var leaseExpiresAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at,cancel_requested FROM runs WHERE id=?`, runID).Scan(&state, &instanceID, &leaseToken, &leaseExpiresAt, &cancelRequested); err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	if state != "running" {
		return protocol.HeartbeatResponse{}, ErrRunState
	}
	if subtle.ConstantTimeCompare([]byte(instanceID), []byte(heartbeat.InstanceID)) != 1 || subtle.ConstantTimeCompare([]byte(leaseToken), []byte(heartbeat.LeaseToken)) != 1 {
		return protocol.HeartbeatResponse{}, ErrLeaseConflict
	}
	now := s.now().UTC()
	if !leaseExpiresAt.Valid || leaseExpiresAt.Int64 <= now.UnixNano() {
		return protocol.HeartbeatResponse{}, ErrLeaseConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET lease_expires_at=? WHERE id=? AND state='running' AND worker_instance=? AND lease_token=?`, now.Add(leaseDuration).UnixNano(), runID, heartbeat.InstanceID, heartbeat.LeaseToken)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	if changed != 1 {
		return protocol.HeartbeatResponse{}, ErrLeaseConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workers SET last_seen_at=? WHERE instance_id=?`, now.Format(time.RFC3339Nano), heartbeat.InstanceID); err != nil {
		return protocol.HeartbeatResponse{}, fmt.Errorf("update worker heartbeat: %w", err)
	}
	return protocol.HeartbeatResponse{CancelRequested: cancelRequested}, tx.Commit()
}

func (s *Store) CancelJob(ctx context.Context, jobID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, triggerIdentity, triggerGeneration string
	if err := tx.QueryRowContext(ctx, `SELECT state,trigger_identity,trigger_generation_id FROM jobs WHERE id=?`, jobID).Scan(&state, &triggerIdentity, &triggerGeneration); err != nil {
		return err
	}
	if terminalJobState(state) {
		return tx.Commit()
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	const reason = "cancelled by operator"
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='cancelled',cancel_requested=1,exit_code=130,error=?,last_error_class='cancelled',completed_at=? WHERE job_id=? AND state='queued'`, reason, now, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET cancel_requested=1 WHERE job_id=? AND state='running'`, jobID); err != nil {
		return err
	}
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE job_id=? AND state='running'`, jobID).Scan(&running); err != nil {
		return err
	}
	if running == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
		if triggerIdentity != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_job_state='cancelled',last_job_error=?,health='healthy',latest_error=?,updated_at=? WHERE identity=? AND generation_id=?`, reason, reason, now, triggerIdentity, triggerGeneration); err != nil {
				return err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE jobs SET updated_at=? WHERE id=?`, now, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteJob(ctx context.Context, jobID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&state); err != nil {
		return err
	}
	if !terminalJobState(state) {
		return ErrJobActive
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET job_id=NULL,updated_at=? WHERE job_id=?`, now, jobID); err != nil {
		return fmt.Errorf("clear deleted job trigger links: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE job_id=?`, jobID); err != nil {
		return fmt.Errorf("delete job runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, jobID); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ReclaimExpiredLeases(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	recovered, err := recoverExpiredRuns(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recovered, nil
}

func recoverExpiredRuns(ctx context.Context, tx *sql.Tx, now time.Time) (int64, error) {
	cancelled, err := finalizeExpiredCancellations(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	completedAt := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, abandonExpiredAttemptsSQL, completedAt, now.UnixNano()); err != nil {
		return 0, fmt.Errorf("abandon expired attempts: %w", err)
	}
	const unknownBudgetReason = "worker lease expired; retry stopped because max_total_tokens is configured but token usage coverage is incomplete"
	unknownBudgetResult, err := tx.ExecContext(ctx, `UPDATE runs SET state='failed',exit_code=1,error=?,last_error_class='transient',completed_at=?,lease_token=NULL,lease_expires_at=NULL,current_attempt_id='',duration_millis=NULL,token_usage=NULL WHERE state='running' AND cancel_requested=0 AND max_total_tokens>0 AND attempt_count<max_attempts AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, unknownBudgetReason, completedAt, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("fail token-budget-unobservable runs: %w", err)
	}
	unknownBudget, err := unknownBudgetResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	if unknownBudget > 0 {
		if err := recordExpiredRunFailures(ctx, tx, completedAt, unknownBudgetReason); err != nil {
			return 0, err
		}
	}
	const exhaustedReason = "worker lease expired and attempt budget was exhausted"
	exhaustedResult, err := tx.ExecContext(ctx, `UPDATE runs SET state='failed',exit_code=1,error=?,last_error_class='transient',completed_at=?,lease_token=NULL,lease_expires_at=NULL,current_attempt_id='',duration_millis=NULL,token_usage=NULL WHERE state='running' AND cancel_requested=0 AND attempt_count>=max_attempts AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, exhaustedReason, completedAt, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("fail attempt-budget-exhausted runs: %w", err)
	}
	exhausted, err := exhaustedResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	if exhausted > 0 {
		if err := recordExpiredRunFailures(ctx, tx, completedAt, exhaustedReason); err != nil {
			return 0, err
		}
	}
	reclaimedResult, err := tx.ExecContext(ctx, reclaimExpiredLeasesSQL, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("reclaim expired leases: %w", err)
	}
	reclaimed, err := reclaimedResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	return cancelled + unknownBudget + exhausted + reclaimed, nil
}

func recordExpiredRunFailures(ctx context.Context, tx *sql.Tx, completedAt, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',updated_at=? WHERE state='running' AND id IN (SELECT job_id FROM runs WHERE state='failed' AND completed_at=? AND error=?)`, completedAt, completedAt, reason); err != nil {
		return fmt.Errorf("fail jobs after expired worker lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_job_state='failed',last_job_error=?,health='failed',latest_error=?,updated_at=? WHERE EXISTS (SELECT 1 FROM jobs j JOIN runs r ON r.job_id=j.id WHERE j.trigger_identity=trigger_state.identity AND j.trigger_generation_id=trigger_state.generation_id AND r.state='failed' AND r.completed_at=? AND r.error=?)`, reason, reason, completedAt, completedAt, reason); err != nil {
		return fmt.Errorf("record trigger failure after expired worker lease: %w", err)
	}
	return nil
}

func finalizeExpiredCancellations(ctx context.Context, tx *sql.Tx, now time.Time) (int64, error) {
	completedAt := now.Format(time.RFC3339Nano)
	expiresAt := now.UnixNano()
	const reason = "cancelled by operator after worker lease expired"
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET state='cancelled',exit_code=130,error=?,error_class='cancelled',completed_at=? WHERE state='running' AND run_id IN (SELECT id FROM runs WHERE state='running' AND cancel_requested=1 AND (lease_expires_at IS NULL OR lease_expires_at<=?))`, reason, completedAt, expiresAt); err != nil {
		return 0, fmt.Errorf("cancel expired attempts: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET state='cancelled',exit_code=130,error=?,last_error_class='cancelled',completed_at=?,lease_token=NULL,lease_expires_at=NULL,current_attempt_id='' WHERE state='running' AND cancel_requested=1 AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, reason, completedAt, expiresAt)
	if err != nil {
		return 0, fmt.Errorf("cancel expired runs: %w", err)
	}
	cancelled, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if cancelled > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',updated_at=? WHERE state='running' AND id IN (SELECT job_id FROM runs WHERE state='cancelled' AND cancel_requested=1)`, completedAt); err != nil {
			return 0, fmt.Errorf("cancel jobs after worker lease expiry: %w", err)
		}
	}
	return cancelled, nil
}

func (s *Store) PruneSupersededWorkers(ctx context.Context, seenAfter time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM workers AS old
WHERE julianday(old.last_seen_at) < julianday(?)
  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.worker_instance=old.instance_id AND r.state='running')
  AND EXISTS (
    SELECT 1 FROM workers newer
    WHERE newer.name=old.name
      AND (julianday(newer.last_seen_at) > julianday(old.last_seen_at)
        OR (newer.last_seen_at=old.last_seen_at AND newer.instance_id>old.instance_id))
  )`, seenAfter.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune superseded workers: %w", err)
	}
	if err := reconcileKnownRepositories(ctx, tx); err != nil {
		return 0, err
	}
	pruned, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return pruned, nil
}

type contextExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// reconcileKnownRepositories makes the admission catalog the union of current
// worker registrations. The latest disconnected worker remains registered, so
// work can still be queued while it is offline. A repository explicitly removed
// by a worker configuration disappears once no retained worker declares it.
func reconcileKnownRepositories(ctx context.Context, executor contextExecutor) error {
	if _, err := executor.ExecContext(ctx, `DELETE FROM known_repositories
WHERE NOT EXISTS (
  SELECT 1 FROM worker_repositories wr
  WHERE wr.repository=known_repositories.repository
)`); err != nil {
		return fmt.Errorf("reconcile known repositories: %w", err)
	}
	return nil
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	jobs, err := s.listJobs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	workers, err := s.listWorkers(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	triggers, err := s.TriggerSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Jobs: jobs, Workers: workers, Triggers: triggers}, nil
}

func (s *Store) TriggerSnapshot(ctx context.Context) ([]TriggerStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.identity,t.family,t.config_signature,t.generation_id,COALESCE(t.next_due_at,''),COALESCE(t.pending_occurrence_at,''),COALESCE(t.last_attempt_at,''),COALESCE(t.last_success_at,''),COALESCE((SELECT j.id FROM jobs j WHERE j.trigger_identity=t.identity AND j.trigger_generation_id=t.generation_id AND j.state IN ('queued','running') ORDER BY j.created_at LIMIT 1),''),t.candidate_count,t.admission_count,t.coalesced_count,t.health,t.latest_error FROM trigger_state t ORDER BY t.identity`)
	if err != nil {
		return nil, fmt.Errorf("read trigger snapshot: %w", err)
	}
	defer rows.Close()
	statuses := []TriggerStatus{}
	for rows.Next() {
		var status TriggerStatus
		var nextDue, pendingOccurrence, lastAttempt, lastSuccess string
		if err := rows.Scan(&status.Identity, &status.Family, &status.ConfigSignature, &status.ConfigGeneration, &nextDue, &pendingOccurrence, &lastAttempt, &lastSuccess, &status.ActiveJobID, &status.CandidateCount, &status.AdmissionCount, &status.CoalescedCount, &status.Health, &status.LatestError); err != nil {
			return nil, fmt.Errorf("read trigger snapshot: %w", err)
		}
		status.NextDueAt = parseOptionalTime(nextDue)
		status.PendingOccurrenceAt = parseOptionalTime(pendingOccurrence)
		status.LastAttemptAt = parseOptionalTime(lastAttempt)
		status.LastSuccessAt = parseOptionalTime(lastSuccess)
		if status.Health == "healthy" && status.ActiveJobID == "" && status.PendingOccurrenceAt == nil && status.NextDueAt != nil && status.NextDueAt.Before(s.now().UTC()) {
			status.Health = "stale"
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trigger snapshot: %w", err)
	}
	return statuses, nil
}

func (s *Store) AvailableRepositories(ctx context.Context, seenAfter time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT wr.repository
FROM worker_repositories wr
JOIN workers w ON w.instance_id=wr.worker_instance
WHERE julianday(w.last_seen_at) >= julianday(?)
   OR EXISTS (SELECT 1 FROM runs r WHERE r.worker_instance=w.instance_id AND r.state='running')
ORDER BY wr.repository`, seenAfter.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) KnownRepositories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository FROM known_repositories ORDER BY repository`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) RunOutput(ctx context.Context, runID string) (RunOutput, error) {
	var output RunOutput
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(result,''),COALESCE(events,'') FROM runs WHERE id=?`, runID).Scan(&output.Result, &output.Events)
	return output, err
}

func (s *Store) listJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.prompt,j.repository,j.github_issue_title,j.command,j.execution_mode,j.origin,j.trigger_identity,j.occurrence_key,j.trigger_subject,j.state,j.created_at,j.updated_at,
COALESCE(r.id,''),COALESCE(r.command,''),COALESCE(r.executor,''),COALESCE(r.profile,''),COALESCE(r.route,''),COALESCE(r.harness,''),COALESCE(r.provider,''),COALESCE(r.auth_mode,''),COALESCE(r.role,''),COALESCE(r.attempt_count,0),COALESCE(r.max_attempts,1),COALESCE(r.max_total_tokens,0),COALESCE(r.last_error_class,''),COALESCE(r.model,''),COALESCE(r.state,''),COALESCE(NULLIF(r.worker_name,''),w.name,''),r.exit_code,COALESCE(r.error,''),COALESCE(r.started_at,''),COALESCE(r.completed_at,''),r.duration_millis,r.token_usage,COALESCE(r.cancel_requested,0)
FROM jobs j LEFT JOIN runs r ON r.job_id=j.id LEFT JOIN workers w ON w.instance_id=r.worker_instance ORDER BY j.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job := Job{Runs: []Run{}}
		var run Run
		var created, updated, started, completed string
		if err := rows.Scan(&job.ID, &job.Prompt, &job.Repository, &job.GitHubIssueTitle, &job.Command, &job.ExecutionMode, &job.Origin, &job.TriggerID, &job.OccurrenceKey, &job.TriggerSubject, &job.State, &created, &updated,
			&run.ID, &run.Command, &run.Executor, &run.Profile, &run.Route, &run.Harness, &run.Provider, &run.AuthMode, &run.Role, &run.AttemptCount, &run.MaxAttempts, &run.MaxTotalTokens, &run.LastErrorClass, &run.Model, &run.State, &run.WorkerName, &run.ExitCode, &run.Error, &started, &completed, &run.DurationMillis, &run.TokenUsage, &run.CancelRequested); err != nil {
			return nil, err
		}
		job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if run.ID != "" {
			run.Attempts = []Attempt{}
			run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
			run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed)
			job.Runs = append(job.Runs, run)
		}
		jobs = append(jobs, job)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return s.attachAttempts(ctx, jobs)
}

func (s *Store) attachAttempts(ctx context.Context, jobs []Job) ([]Job, error) {
	byRun := make(map[string]*Run)
	for jobIndex := range jobs {
		for runIndex := range jobs[jobIndex].Runs {
			byRun[jobs[jobIndex].Runs[runIndex].ID] = &jobs[jobIndex].Runs[runIndex]
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,ordinal,profile,harness,provider,model,worker_name,state,exit_code,error,error_class,started_at,COALESCE(completed_at,''),duration_millis,token_usage,herdr_session,herdr_workspace_id,herdr_tab_id,herdr_pane_id,herdr_agent_name FROM attempts ORDER BY run_id,ordinal`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt Attempt
		var runID, started, completed string
		var terminal protocol.TerminalBinding
		if err := rows.Scan(&attempt.ID, &runID, &attempt.Number, &attempt.Profile, &attempt.Harness, &attempt.Provider, &attempt.Model, &attempt.WorkerName, &attempt.State, &attempt.ExitCode, &attempt.Error, &attempt.ErrorClass, &started, &completed, &attempt.DurationMillis, &attempt.TokenUsage, &terminal.Session, &terminal.WorkspaceID, &terminal.TabID, &terminal.PaneID, &terminal.AgentName); err != nil {
			return nil, err
		}
		if terminal.PaneID != "" {
			attempt.Terminal = &terminal
		}
		attempt.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		attempt.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed)
		if run := byRun[runID]; run != nil {
			run.Attempts = append(run.Attempts, attempt)
		}
	}
	return jobs, rows.Err()
}

func resultMetrics(result []byte) (*int64, *int64) {
	var fields map[string]json.RawMessage
	if len(result) == 0 || json.Unmarshal(result, &fields) != nil {
		return nil, nil
	}
	return nonNegativeInteger(fields["duration_millis"]), nonNegativeInteger(fields["token_usage"])
}

func attemptAggregateMetrics(ctx context.Context, tx *sql.Tx, runID string) (*int64, *int64, int, error) {
	var count, durationCount, tokenCount int
	var durationSum, tokenSum int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(duration_millis),COALESCE(SUM(duration_millis),0),COUNT(token_usage),COALESCE(SUM(token_usage),0) FROM attempts WHERE run_id=?`, runID).Scan(&count, &durationCount, &durationSum, &tokenCount, &tokenSum); err != nil {
		return nil, nil, 0, fmt.Errorf("aggregate attempt metrics: %w", err)
	}
	var duration, tokens *int64
	if count > 0 && durationCount == count {
		duration = &durationSum
	}
	if count > 0 && tokenCount == count {
		tokens = &tokenSum
	}
	return duration, tokens, count, nil
}

func nonNegativeInteger(raw json.RawMessage) *int64 {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil
	}
	return &value
}

func elapsedMillis(startedAt, completedAt string) *int64 {
	started, startErr := time.Parse(time.RFC3339Nano, startedAt)
	completed, completionErr := time.Parse(time.RFC3339Nano, completedAt)
	if startErr != nil || completionErr != nil {
		return nil
	}
	duration := completed.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	return &duration
}

func (s *Store) listWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id,name,last_seen_at,environment_json,profiles_json,transports_json FROM workers ORDER BY name,instance_id`)
	if err != nil {
		return nil, err
	}
	workers := []Worker{}
	for rows.Next() {
		worker := Worker{Repositories: []string{}, Profiles: map[string]protocol.ProfileCapability{}}
		var lastSeen, environmentJSON, profilesJSON, transportsJSON string
		if err := rows.Scan(&worker.InstanceID, &worker.Name, &lastSeen, &environmentJSON, &profilesJSON, &transportsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		worker.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
		if err := json.Unmarshal([]byte(environmentJSON), &worker.Environment); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode worker %q environment: %w", worker.InstanceID, err)
		}
		if err := json.Unmarshal([]byte(profilesJSON), &worker.Profiles); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode worker %q profiles: %w", worker.InstanceID, err)
		}
		if err := json.Unmarshal([]byte(transportsJSON), &worker.Transports); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode worker %q transports: %w", worker.InstanceID, err)
		}
		workers = append(workers, worker)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	repositories, err := s.db.QueryContext(ctx, `SELECT worker_instance,repository FROM worker_repositories ORDER BY worker_instance,repository`)
	if err != nil {
		return nil, err
	}
	defer repositories.Close()
	byInstance := make(map[string]int, len(workers))
	for index, worker := range workers {
		byInstance[worker.InstanceID] = index
	}
	for repositories.Next() {
		var instanceID, repository string
		if err := repositories.Scan(&instanceID, &repository); err != nil {
			return nil, err
		}
		if index, ok := byInstance[instanceID]; ok {
			workers[index].Repositories = append(workers[index].Repositories, repository)
		}
	}
	return workers, repositories.Err()
}

func scanRunSpec(row *sql.Row) (protocol.RunSpec, error) {
	var run protocol.RunSpec
	var candidates, fallbackOn string
	err := row.Scan(&run.ID, &run.JobID, &run.Command, &run.CommandHash, &run.Executor, &run.Model, &run.Repository, &run.RenderedPrompt, &run.TimeoutMillis, &run.LeaseToken, &run.Route, &run.Profile, &run.Harness, &run.Provider, &run.AuthMode, &run.Role, &candidates, &run.MaxAttempts, &run.MaxTotalTokens, &fallbackOn, &run.AttemptID, &run.AttemptNumber, &run.PreviousErrorClass, &run.ExecutionMode, &run.Origin)
	if err == nil {
		err = errors.Join(json.Unmarshal([]byte(candidates), &run.Candidates), json.Unmarshal([]byte(fallbackOn), &run.FallbackOn))
	}
	return run, err
}

func selectRouteProfile(candidates []string, offset int, executors map[string]bool, models map[string][]string, model string) string {
	for index := range candidates {
		profile := candidates[(offset+index)%len(candidates)]
		if executors[profile] && supportsModel(models, profile, model) {
			return profile
		}
	}
	return ""
}

type routeProfileAvailability struct {
	models map[string]bool
}

func connectedRouteProfiles(ctx context.Context, tx *sql.Tx, repository, executionMode string, seenAfter time.Time) (map[string]routeProfileAvailability, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.last_seen_at,w.profiles_json,w.transports_json FROM workers w JOIN worker_repositories wr ON wr.worker_instance=w.instance_id WHERE wr.repository=? AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.worker_instance=w.instance_id AND r.state='running')`, repository)
	if err != nil {
		return nil, fmt.Errorf("read connected route profiles: %w", err)
	}
	defer rows.Close()
	available := make(map[string]routeProfileAvailability)
	for rows.Next() {
		var lastSeen, profilesJSON, transportsJSON string
		if err := rows.Scan(&lastSeen, &profilesJSON, &transportsJSON); err != nil {
			return nil, err
		}
		seen, err := time.Parse(time.RFC3339Nano, lastSeen)
		if err != nil || seen.Before(seenAfter) {
			continue
		}
		var transports []string
		if json.Unmarshal([]byte(transportsJSON), &transports) != nil || !slices.Contains(transports, executionMode) {
			continue
		}
		profiles := make(map[string]protocol.ProfileCapability)
		if err := json.Unmarshal([]byte(profilesJSON), &profiles); err != nil {
			return nil, fmt.Errorf("decode connected worker profiles: %w", err)
		}
		for name, profile := range profiles {
			if !profile.Available {
				continue
			}
			capability := available[name]
			if capability.models == nil {
				capability.models = make(map[string]bool)
			}
			for _, model := range profile.Models {
				capability.models[model] = true
			}
			available[name] = capability
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return available, nil
}

func selectPreferredRouteProfile(candidates []string, offset int, model string, available map[string]routeProfileAvailability) string {
	for index := range candidates {
		profile := candidates[(offset+index)%len(candidates)]
		capability, ok := available[profile]
		if ok && (model == "" || capability.models[model]) {
			return profile
		}
	}
	return ""
}

func routeFields(command config.ResolvedCommand) (string, string, error) {
	candidates, err := json.Marshal(command.Candidates)
	if err != nil {
		return "", "", fmt.Errorf("encode route candidates: %w", err)
	}
	fallbackOn, err := json.Marshal(command.FallbackOn)
	if err != nil {
		return "", "", fmt.Errorf("encode route fallback policy: %w", err)
	}
	return string(candidates), string(fallbackOn), nil
}

func runMaxAttempts(command config.ResolvedCommand) int {
	if command.MaxAttempts > 0 {
		return command.MaxAttempts
	}
	return defaultLegacyMaxAttempts
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func sortedSet(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

func supportsModel(capabilities map[string][]string, executor, model string) bool {
	if model == "" {
		return true
	}
	models, ok := capabilities[executor]
	if !ok {
		return false
	}
	if len(models) == 0 {
		return true
	}
	return stringSet(models)[model]
}

func fixedTriggerFamily(family string) bool {
	return family == "interval" || family == "cron"
}

func nullableTimeText(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func boundedTriggerError(message string) string {
	runes := []rune(message)
	if len(runes) > maxTriggerErrorLength {
		runes = runes[:maxTriggerErrorLength]
	}
	return string(runes)
}

func terminalRunState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "timed_out" || state == "cancelled"
}

func terminalJobState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func validateOutcome(state string, exitCode int) error {
	switch state {
	case "succeeded":
		if exitCode != 0 {
			return errors.New("succeeded run must have exit code 0")
		}
	case "failed":
		if exitCode == 0 {
			return errors.New("failed run must have a non-zero exit code")
		}
	case "timed_out":
		if exitCode != 124 {
			return errors.New("timed out run must have exit code 124")
		}
	case "cancelled":
		if exitCode != 130 {
			return errors.New("cancelled run must have exit code 130")
		}
	}
	return nil
}

func randomID(prefix string, byteCount int) (string, error) {
	body := make([]byte, byteCount)
	if _, err := rand.Read(body); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(body), nil
}
