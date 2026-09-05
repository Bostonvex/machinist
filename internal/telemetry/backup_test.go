package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup is a database, not a file with some bytes in it. Opening the copy is
// the only check that distinguishes a consistent vacuum from a torn read, which
// is the whole reason this is not a file copy.
func TestABackupOpensAndHoldsWhatTheOriginalHeld(t *testing.T) {
	store := openTestStore(t)
	insert(t, store, event(t, "backed-up", EventTurnCompleted, nil))
	destination := filepath.Join(t.TempDir(), "backup.db")

	written, err := store.BackupTo(t.Context(), destination)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if written != destination {
		t.Fatalf("backup went to %q", written)
	}

	restored, err := OpenStore(destination)
	if err != nil {
		t.Fatalf("open the backup: %v", err)
	}
	defer restored.Close()
	health, err := restored.Health(t.Context())
	if err != nil {
		t.Fatalf("health of the backup: %v", err)
	}
	if health["events"] != 1 {
		t.Fatalf("the backup holds %v event(s)", health["events"])
	}
}

// The archive holds exactly what the database holds, so it is never left
// readable by anything else on the machine.
func TestABackupIsNotReadableByOthers(t *testing.T) {
	store := openTestStore(t)
	destination := filepath.Join(t.TempDir(), "backup.db")
	if _, err := store.BackupTo(t.Context(), destination); err != nil {
		t.Fatalf("backup: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("backup mode is %s", info.Mode())
	}
}

// Destroying an earlier backup is the one thing this must never do.
func TestABackupRefusesToReplaceWhatIsAlreadyThere(t *testing.T) {
	store := openTestStore(t)
	directory := t.TempDir()
	destination := filepath.Join(directory, "backup.db")
	if err := os.WriteFile(destination, []byte("earlier"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.BackupTo(t.Context(), destination); err == nil {
		t.Fatal("the backup replaced an existing file")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "earlier" {
		t.Fatalf("the earlier file changed: %q", contents)
	}
	// A failed backup leaves nothing behind either. Staging directories that
	// survived a refusal would accumulate a copy of the database per attempt.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a refused backup left %d entries: %v", len(entries), entries)
	}
}

func TestABackupNeedsSomewhereToGo(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.BackupTo(t.Context(), "   "); err == nil {
		t.Fatal("the backup accepted a blank destination")
	}
}

// The journal mode is a property of the database rather than of the code that
// opened it, and nothing else in the health report would reveal that the pragma
// failed to take on this file.
func TestHealthReportsTheJournalMode(t *testing.T) {
	store := openTestStore(t)
	health, err := store.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := health["journal_mode"].(string); !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %v", health["journal_mode"])
	}
}
