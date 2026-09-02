//go:build darwin || linux

package runner

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
)

func TestIgnorableProcessTreeTerminationError(t *testing.T) {
	if !ignorableProcessTreeTerminationError(syscall.ESRCH) {
		t.Fatal("ESRCH should be ignored")
	}
	if got, want := ignorableProcessTreeTerminationError(syscall.EPERM), runtime.GOOS == "darwin"; got != want {
		t.Fatalf("EPERM ignored = %t, want %t on %s", got, want, runtime.GOOS)
	}
	if ignorableProcessTreeTerminationError(errors.New("unexpected")) {
		t.Fatal("unexpected error should be preserved")
	}
}
