package telemetry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestATokenIsCreatedOnceAndThenReturnedUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest-token")
	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(first) < 32 {
		t.Fatalf("generated token is %d characters", len(first))
	}
	second, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// A token that changed on reload would revoke every producer's credential
	// every time the collector restarted.
	if first != second {
		t.Error("the token changed between loads")
	}
}

func TestAFreshSecretIsNotReadableByAnyoneElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest-token")
	if _, err := LoadOrCreateToken(path); err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		t.Errorf("a fresh token file is mode %#o", permissions)
	}
}

// The check runs on every load, not only at creation. A file this process wrote
// correctly can be widened afterwards, and a check that only ever sees a
// just-created file is worth nothing.
func TestAWidenedSecretIsRefusedRatherThanRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest-token")
	if _, err := LoadOrCreateToken(path); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	value, err := LoadOrCreateToken(path)
	if err == nil {
		t.Fatal("a world-readable token was accepted")
	}
	if !errors.Is(err, ErrSecretFile) {
		t.Errorf("error is not an ErrSecretFile: %v", err)
	}
	if value != "" {
		t.Error("a refused load returned a token anyway")
	}
	// Repairing the mode would hide that something else on this machine had the
	// file open, which is the fact the operator needs.
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Error("the refusal silently repaired the permissions")
	}
}

// Following a symlink would let whoever could create it choose where a fresh
// secret is written, or aim the read at a file they can already see.
func TestASymlinkedSecretIsRefused(t *testing.T) {
	directory := t.TempDir()
	real := filepath.Join(directory, "elsewhere")
	if err := os.WriteFile(real, []byte("0123456789012345678901234567890123456789\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(directory, "ingest-token")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadOrCreateToken(link); !errors.Is(err, ErrSecretFile) {
		t.Errorf("a symlinked token file was accepted: %v", err)
	}
}

// A truncated or half-written file would otherwise become a short token that
// still authenticates.
func TestATruncatedSecretIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest-token")
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateToken(path); !errors.Is(err, ErrSecretFile) {
		t.Errorf("a five-character token was accepted: %v", err)
	}
}

func TestADirectoryIsNotASecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest-token")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := LoadOrCreateToken(path); !errors.Is(err, ErrSecretFile) {
		t.Errorf("a directory was read as a token: %v", err)
	}
}

func TestNoConfiguredPathIsAnError(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if _, err := LoadOrCreateToken(path); !errors.Is(err, ErrSecretFile) {
			t.Errorf("an empty path produced %v", err)
		}
	}
}

// The token and the salt are different secrets and must not be the same value:
// a salt that equalled the token would make every pseudonym derivable by
// anyone holding the credential to send events.
func TestTheTokenAndTheSaltAreDifferentSecrets(t *testing.T) {
	directory := t.TempDir()
	token, err := LoadOrCreateToken(filepath.Join(directory, "ingest-token"))
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	salt, err := LoadOrCreateIdentitySalt(filepath.Join(directory, "identity-salt"))
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	if token == salt {
		t.Error("the identity salt is the ingest token")
	}
}
