//go:build windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func replaceExecutable(path string, binary []byte) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".machinist-update-*.exe")
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(binary); err != nil {
		temporary.Close()
		return fmt.Errorf("write update: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync update: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close update: %w", err)
	}

	// Windows cannot rename a new file over an existing executable. Moving the
	// running image aside first is supported and gives us a rollback point.
	backupPath := path + ".old"
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove previous update backup %s: %w", backupPath, err)
	}
	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("stage current executable %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		rollbackErr := os.Rename(backupPath, path)
		return fmt.Errorf("replace %s: %w", path, errors.Join(err, rollbackErr))
	}
	// The running image may remain locked until this process exits. A leftover
	// .old file is harmless and is removed before the next successful update.
	_ = os.Remove(backupPath)
	return nil
}
