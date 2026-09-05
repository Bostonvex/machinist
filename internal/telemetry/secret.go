package telemetry

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the two private values the collector needs: the ingest token
// a producer authenticates with, and the salt that turns an identity into a
// stable pseudonym. Both are created on first use rather than configured,
// because a value an operator has to invent is a value an operator will reuse.
//
// Neither ever enters Machinist configuration or reaches the control plane.
// Configuration names the file; the file holds the secret. That is what keeps a
// token out of a config that gets copied between machines and pasted into
// issues.

// ErrSecretFile reports a secret file that cannot be trusted. It is returned
// rather than repaired: silently fixing permissions would hide that something
// else on the machine had them open, which is the fact the operator needs.
var ErrSecretFile = errors.New("secret file")

// LoadOrCreateToken returns the ingest token at path, creating one if absent.
func LoadOrCreateToken(path string) (string, error) { return loadOrCreate(path, "token") }

// LoadOrCreateIdentitySalt returns the identity salt at path, creating one if
// absent. The salt is what makes a pseudonym stable on one machine and
// meaningless off it: the same agent hashes to the same id here and to nothing
// recognisable anywhere else, so telemetry can be correlated locally without
// becoming a directory of who ran what.
func LoadOrCreateIdentitySalt(path string) (string, error) {
	return loadOrCreate(path, "identity salt")
}

func loadOrCreate(path, label string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: no %s file configured", ErrSecretFile, label)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("%w: create %s directory: %v", ErrSecretFile, label, err)
	}

	// A symlink is refused before anything is read or written. Following one
	// would let whoever could create it choose where a fresh secret gets
	// written, or point the read at a file they can already see.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s file must not be a symbolic link", ErrSecretFile, label)
	}

	if err := create(path, label); err != nil {
		return "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: read %s file: %v", ErrSecretFile, label, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s path must be a regular file", ErrSecretFile, label)
	}
	// Checked on every load, not only at creation. A file this process wrote
	// correctly can be widened afterwards, and the check is worth nothing if it
	// only ever runs on a file that was just created.
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		return "", fmt.Errorf("%w: %s file permissions are %#o; must be 0600 or stricter", ErrSecretFile, label, permissions)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: read %s file: %v", ErrSecretFile, label, err)
	}
	value := strings.TrimSpace(string(contents))
	// Length is checked because the failure this catches is a truncated or
	// half-written file, which would otherwise become a short token that still
	// authenticates.
	if len(value) < 32 || len(value) > 256 {
		return "", fmt.Errorf("%w: %s file does not contain a usable value", ErrSecretFile, label)
	}
	return value, nil
}

// create writes a fresh secret only if the file does not exist. O_EXCL makes
// that a decision the filesystem makes rather than one made between a stat and
// a write, so two collectors starting together cannot each generate a token and
// leave the second's producers unable to authenticate.
func create(path, label string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: create %s file: %v", ErrSecretFile, label, err)
	}
	defer file.Close()

	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Errorf("%w: generate %s: %v", ErrSecretFile, label, err)
	}
	if _, err := file.WriteString(base64.RawURLEncoding.EncodeToString(buffer) + "\n"); err != nil {
		return fmt.Errorf("%w: write %s file: %v", ErrSecretFile, label, err)
	}
	return nil
}
