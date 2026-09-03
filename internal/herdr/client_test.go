package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestNewFromEnvironmentRequiresHerdrPluginContext(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	if _, err := NewFromEnvironment(); err == nil || !strings.Contains(err.Error(), "HERDR_ENV=1") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteCreatesBoundInteractiveAgent(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "calls.log")
	scriptPath := filepath.Join(directory, "fake-herdr")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_HERDR_LOG"
case "$1 $2" in
  "workspace create") printf '%s\n' '{"result":{"workspace":{"workspace_id":"w9"},"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"}}}' ;;
  "agent start") printf '%s\n' '{"result":{"status":"idle"}}' ;;
  "agent prompt") printf '%s\n' '{"result":{"agent":{"status":"done"}}}' ;;
  *) printf '%s\n' '{"result":{}}' ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &Client{Binary: scriptPath, SocketPath: filepath.Join(directory, "sessions", "machinist", "herdr.sock"), Environment: []string{"PATH=/usr/bin:/bin", "FAKE_HERDR_LOG=" + logPath}}
	spec := protocol.RunSpec{ID: "run_0123456789abcdef01234567", JobID: "job_0123456789abcdef01234567", AttemptID: "attempt_0123456789abcdef01234567", Command: "implement", CommandHash: "hash"}
	command := config.ResolvedCommand{Name: "implement", Profile: "codex-subscription", Harness: "codex", HerdrAgent: "codex", HerdrArgs: []string{"--model=gpt-test"}, Prompt: "Implement the task", Timeout: time.Minute, Environment: map[string]string{"MACHINIST_JOB_ID": spec.JobID}}
	var bound protocol.TerminalBinding
	completion, err := client.Execute(context.Background(), spec, command, directory, func(binding protocol.TerminalBinding) error { bound = binding; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if completion.State != "succeeded" || completion.ExitCode != 0 || bound.Session != "machinist" || bound.WorkspaceID != "w9" || bound.PaneID != "w9:p1" {
		t.Fatalf("completion=%#v binding=%#v", completion, bound)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workspace create", "--env MACHINIST_JOB_ID=", "agent start", "--kind codex", "agent prompt", "Implement the task"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("calls %q do not contain %q", calls, want)
		}
	}
}

func TestExecuteReturnsTerminalCompletionWhenWorkspaceCreationFails(t *testing.T) {
	directory := t.TempDir()
	scriptPath := filepath.Join(directory, "fake-herdr")
	script := `#!/bin/sh
printf '%s\n' 'session unavailable' >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &Client{Binary: scriptPath, SocketPath: filepath.Join(directory, "sessions", "machinist", "herdr.sock"), Environment: []string{"PATH=/usr/bin:/bin"}}
	spec := protocol.RunSpec{ID: "run_0123456789abcdef01234567", JobID: "job_0123456789abcdef01234567", AttemptID: "attempt_0123456789abcdef01234567", Command: "implement", CommandHash: "hash"}
	command := config.ResolvedCommand{Name: "implement", Profile: "codex-subscription", Harness: "codex", HerdrAgent: "codex", Prompt: "Implement the task", Timeout: time.Minute}
	completion, err := client.Execute(context.Background(), spec, command, directory, func(protocol.TerminalBinding) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "session unavailable") {
		t.Fatalf("error=%v", err)
	}
	if completion.State != "failed" || completion.ExitCode == 0 || completion.ErrorClass != "transport" || !strings.Contains(completion.Error, "session unavailable") {
		t.Fatalf("completion=%#v", completion)
	}
}
