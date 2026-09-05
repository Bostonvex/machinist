package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists validated events and the state derived from them.
//
// Telemetry lives in its own database, not alongside jobs and runs. It arrives
// in bursts of hundreds of samples a minute from every endpoint at once, and
// SQLite serialises writers: sharing a file would make a GPU poller able to
// delay a job being handed to a worker. Losing telemetry is a gap in a chart.
// Losing a job is losing work. They should not be able to block each other, so
// they do not share a lock.
type Store struct {
	write *sql.DB
	read  *sql.DB
	now   func() time.Time
}

// schemaVersion is the version this build writes and can read.
const schemaVersion = 1

// OpenStore opens, creating and migrating as needed.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create telemetry database directory: %w", err)
	}

	// One writer, because SQLite has one. Declaring it is better than
	// discovering it: with a pool the database returns SQLITE_BUSY under load
	// and the caller sees intermittent write failures that look like a bug in
	// whatever was running at the time.
	write, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open telemetry database: %w", err)
	}
	write.SetMaxOpenConns(1)

	store := &Store{write: write, now: time.Now}
	if err := store.initialize(context.Background()); err != nil {
		write.Close()
		return nil, err
	}

	// Readers get their own pool. In WAL a reader never blocks the writer and
	// the writer never blocks a reader, so a dashboard aggregating a month of
	// events cannot stall ingestion behind it.
	read, err := sql.Open("sqlite", path+"?_pragma=query_only(true)&_pragma=busy_timeout(5000)")
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("open telemetry database for reading: %w", err)
	}
	store.read = read
	return store, nil
}

func (s *Store) Close() error {
	var readErr error
	if s.read != nil {
		readErr = s.read.Close()
	}
	return errors.Join(readErr, s.write.Close())
}

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.write.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read telemetry schema version: %w", err)
	}
	// A newer database is refused rather than used. Its tables may hold columns
	// this build does not write, and opening it read-write would quietly
	// degrade rows a later build put there.
	if version > schemaVersion {
		return fmt.Errorf("telemetry database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		_, err := s.write.ExecContext(ctx, sessionPragmas)
		return err
	}
	if _, err := s.write.ExecContext(ctx, sessionPragmas+schema); err != nil {
		return fmt.Errorf("create telemetry schema: %w", err)
	}
	return nil
}

// sessionPragmas are set on every open, not only at creation: they are
// connection state, not stored schema, so a database created by an earlier
// process still needs them applied here.
const sessionPragmas = `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA synchronous=NORMAL;
`

const schema = `
CREATE TABLE IF NOT EXISTS events (
  event_id TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  received_at TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  agent_display_name TEXT NOT NULL,
  harness TEXT, model TEXT, endpoint_id TEXT, session_id TEXT, turn_id TEXT,
  payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_observed_idx ON events(observed_at);
CREATE INDEX IF NOT EXISTS events_type_observed_idx ON events(event_type, observed_at DESC);
CREATE INDEX IF NOT EXISTS events_agent_observed_idx ON events(agent_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS events_turn_observed_idx ON events(turn_id, observed_at);
CREATE INDEX IF NOT EXISTS events_turn_type_idx ON events(turn_id, event_type, observed_at);
CREATE INDEX IF NOT EXISTS events_dimensions_idx ON events(harness, model, endpoint_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS infrastructure_samples (
  event_id TEXT PRIMARY KEY,
  observed_at TEXT NOT NULL, event_type TEXT NOT NULL,
  endpoint_id TEXT, provider_id TEXT, node_id TEXT,
  metric_name TEXT NOT NULL, unit TEXT, measurement_quality TEXT,
  value REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS samples_observed_idx ON infrastructure_samples(observed_at);
CREATE INDEX IF NOT EXISTS samples_series_idx ON infrastructure_samples(metric_name, node_id, observed_at);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
  harness TEXT, model TEXT, endpoint_id TEXT,
  current_state TEXT NOT NULL, current_turn_id TEXT
);

CREATE TABLE IF NOT EXISTS turns (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  session_id TEXT,
  started_at TEXT NOT NULL, ended_at TEXT, outcome TEXT,
  ttfa_ms REAL, ttfvt_ms REAL, first_tool_ms REAL, duration_ms REAL, max_stall_ms REAL,
  tool_count INTEGER, tool_observation_mode TEXT, measurement_quality TEXT,
  error_category TEXT, error_code TEXT, cancellation_reason TEXT,
  harness TEXT, model TEXT, endpoint_id TEXT,
  FOREIGN KEY(agent_id) REFERENCES agents(id)
);
CREATE INDEX IF NOT EXISTS turns_agent_started_idx ON turns(agent_id, started_at DESC);
CREATE INDEX IF NOT EXISTS turns_started_idx ON turns(started_at DESC);

PRAGMA user_version=1;
`
