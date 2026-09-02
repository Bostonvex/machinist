//go:build windows

package runner

import (
	"os/exec"
	"testing"
	"time"
)

func TestWindowsProcessTreeJobTerminatesCommand(t *testing.T) {
	command := exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 >NUL")
	configureProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := registerProcessTree(command.Process); err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		t.Fatal(err)
	}
	if err := terminateProcessTree(command.Process); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := command.Process.Wait()
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Job Object termination did not stop the command")
	}
}
