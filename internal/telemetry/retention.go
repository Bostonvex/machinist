package telemetry

import (
	"context"
	"fmt"
	"time"
)

// DefaultRetention is how long raw events are kept.
//
// Raw events are the highest-volume thing here and the least useful once
// they have been derived from: a week is long enough to investigate an
// incident and short enough that a machine left running does not fill its own
// disk. agents and turns are not purged — they are small, and they are what a
// reader actually asks about.
const DefaultRetention = 7 * 24 * time.Hour

// Purge deletes raw events and samples observed before the cutoff, returning
// how many events went.
//
// The cutoff must carry a timezone. A zoneless one would be read as the
// collector's local time while every observed_at is UTC, and on a machine east
// of Greenwich that silently deletes hours of events that are not yet expired.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge cutoff is required")
	}
	cutoff := before.UTC().Format(timeLayout)

	transaction, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin purge: %w", err)
	}
	defer transaction.Rollback()

	if _, err := transaction.ExecContext(ctx, `DELETE FROM infrastructure_samples WHERE observed_at < ?`, cutoff); err != nil {
		return 0, fmt.Errorf("purge samples: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM events WHERE observed_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge events: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge events: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit purge: %w", err)
	}
	return removed, nil
}

// Health reports what a caller needs to know before trusting a reading: that
// the database answers, and how much it holds.
func (s *Store) Health(ctx context.Context) (map[string]any, error) {
	counts := map[string]any{"status": "ok", "schema_version": schemaVersion}
	for table, key := range map[string]string{"events": "events", "agents": "agents", "turns": "turns"} {
		var count int
		if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			return nil, fmt.Errorf("read %s count: %w", table, err)
		}
		counts[key] = count
	}
	// The journal mode is reported because it is the difference between a
	// reader that never blocks ingestion and one that does. It is set on every
	// open, so an answer other than WAL means the pragma did not take on this
	// file — which is a property of the database, not of the code that opened
	// it, and nothing else would reveal it.
	var journal string
	if err := s.read.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		return nil, fmt.Errorf("read journal mode: %w", err)
	}
	counts["journal_mode"] = journal
	return counts, nil
}
