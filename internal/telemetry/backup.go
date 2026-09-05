package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackupTo writes a consistent copy of the telemetry database and returns where
// it went.
//
// This is a vacuum, not a file copy. The database is written to while a backup
// runs — providers poll on their own schedule and producers post whenever an
// agent does something — so copying the file and its write-ahead log with cp
// captures the two at different instants. That produces an archive which only
// fails to open on the day somebody needs it.
//
// The copy is built beside the destination and linked into place, so a backup
// never overwrites one that already exists and a reader never finds a
// half-written file under the name it was told to expect. It is staged inside a
// 0700 directory and narrowed to 0600 before the link, because the archive
// holds exactly what the database holds and would otherwise sit group- and
// world-readable for the length of the vacuum.
func (s *Store) BackupTo(ctx context.Context, destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", errors.New("backup destination is required")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve backup destination: %w", err)
	}
	// Checked before the vacuum as well as by the link below. Doing the work
	// first and refusing afterwards would spend minutes copying a database only
	// to throw the result away.
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("backup destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read backup destination: %w", err)
	}

	staging, err := os.MkdirTemp(filepath.Dir(destination), ".machinist-backup-")
	if err != nil {
		return "", fmt.Errorf("create backup staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	staged := filepath.Join(staging, filepath.Base(destination))
	if _, err := s.write.ExecContext(ctx, `VACUUM INTO ?`, staged); err != nil {
		return "", fmt.Errorf("copy telemetry database: %w", err)
	}
	if err := os.Chmod(staged, 0o600); err != nil {
		return "", fmt.Errorf("restrict backup: %w", err)
	}
	// Linked rather than renamed. A rename replaces whatever is at the
	// destination, and destroying an earlier backup is the one thing this must
	// never do; a link fails instead, and it fails in the filesystem rather
	// than between the check above and the write.
	if err := os.Link(staged, destination); err != nil {
		return "", fmt.Errorf("place backup at %s: %w", destination, err)
	}
	return destination, nil
}
