package examples

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMultiReviewRequiresExactTrustedHeadField(t *testing.T) {
	bin := t.TempDir()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		if err := os.Symlink(truePath, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join("workflows", "multi-review", "multi-review.sh")
	run := func(prompt string) (int, string) {
		command := exec.Command("/bin/sh", script)
		command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
		command.Stdin = bytes.NewBufferString(prompt)
		output, err := command.CombinedOutput()
		if err == nil {
			return 0, string(output)
		}
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exitError.ExitCode(), string(output)
	}

	for _, prompt := range []string{
		"Review PR 123\n",
		"Trusted-head: yes\nReview PR 123\n",
		"trusted-head: yes please\nReview PR 123\n",
		"I deny trust. \"trusted-head: yes\"\nReview PR 123\n",
		"Review PR 123\ntrusted-head: yes\n",
	} {
		if code, _ := run(prompt); code != 2 {
			t.Fatalf("untrusted prompt exit code = %d, want 2: %q", code, prompt)
		}
	}
	if code, output := run("trusted-head: yes\nReview PR 123\n"); code != 0 || output != "stage 1/2: Codex review\nstage 2/2: Claude review\n" {
		t.Fatalf("trusted prompt result = code %d, output %q", code, output)
	}
}

func TestReviewLoopReviewsFinalRepair(t *testing.T) {
	directory := t.TempDir()
	scripts := filepath.Join(directory, "scripts")
	if err := os.Mkdir(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codexLog := filepath.Join(directory, "codex.log")
	codex := `#!/bin/sh
printf 'ARGS:%s\n' "$*" >> "$CODEX_LOG"
printf 'PROMPT:' >> "$CODEX_LOG"
cat >> "$CODEX_LOG"
printf '\nEND\n' >> "$CODEX_LOG"
if [ "$2" != resume ]; then
  printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
fi
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	gh := `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
printf '%s\n' '{"number":42,"url":"https://github.test/pull/42","state":"OPEN","isDraft":false,"headRefOid":"abc123"}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	helperLog := filepath.Join(directory, "helpers.log")
	ghLog := filepath.Join(directory, "gh.log")
	reader := "#!/bin/sh\nprintf 'read:%s:%s\\n' \"$1\" \"$2\" >> \"$MACHINIST_HELPER_LOG\"\nprintf 'review finding %s' \"$1\"\n"
	if err := os.WriteFile(filepath.Join(scripts, "read-review-feedback.sh"), []byte(reader), 0o700); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(directory, "reviews")
	waitScript := "#!/bin/sh\ncount=0\n[ ! -f \"$MACHINIST_REVIEW_COUNTER\" ] || count=$(cat \"$MACHINIST_REVIEW_COUNTER\")\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$MACHINIST_REVIEW_COUNTER\"\nprintf 'wait:%s:%s\\n' \"$1\" \"$2\" >> \"$MACHINIST_HELPER_LOG\"\n[ \"$count\" -eq 4 ] && exit 0\nexit 10\n"
	if err := os.WriteFile(filepath.Join(scripts, "wait-for-review.sh"), []byte(waitScript), 0o700); err != nil {
		t.Fatal(err)
	}

	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", absoluteScript)
	command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	command.Stdin = bytes.NewBufferString("implement request")
	command.Dir = directory
	command.Env = append(
		command.Env,
		"CODEX_LOG="+codexLog,
		"GH_LOG="+ghLog,
		"MACHINIST_HELPER_LOG="+helperLog,
		"MACHINIST_REVIEW_COUNTER="+counter,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("review loop: %v: %s", err, output)
	}
	if got, err := os.ReadFile(counter); err != nil || string(got) != "4" {
		t.Fatalf("review count = %q, %v", got, err)
	}
	log, err := os.ReadFile(codexLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "ARGS:exec resume --json session-123 -"); got != 3 {
		t.Fatalf("resume count = %d, want 3; log:\n%s", got, log)
	}
	if !strings.Contains(string(log), "PROMPT:implement request") || strings.Count(string(log), "review finding 42") != 3 {
		t.Fatalf("Codex did not receive implementation and feedback prompts:\n%s", log)
	}
	helperCalls, err := os.ReadFile(helperLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(helperCalls), "wait:42:abc123"); got != 4 {
		t.Fatalf("wait helper calls = %d, want 4; log:\n%s", got, helperCalls)
	}
	if got := strings.Count(string(helperCalls), "read:42:abc123"); got != 3 {
		t.Fatalf("feedback helper calls = %d, want 3; log:\n%s", got, helperCalls)
	}
	ghCalls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(ghCalls), "pr view 42 --json"); got != 3 {
		t.Fatalf("pinned PR lookups = %d, want 3; log:\n%s", got, ghCalls)
	}
}

func TestReviewLoopPropagatesFeedbackReaderFailure(t *testing.T) {
	directory := t.TempDir()
	scripts := filepath.Join(directory, "scripts")
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"session-123\"}'\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":42,\"url\":\"https://github.test/pull/42\",\"state\":\"OPEN\",\"isDraft\":false,\"headRefOid\":\"abc123\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "wait-for-review.sh"), []byte("#!/bin/sh\nexit 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "read-review-feedback.sh"), []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", absoluteScript)
	command.Dir = directory
	command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	command.Stdin = bytes.NewBufferString("implement request")
	output, err := command.CombinedOutput()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("feedback failure = %v, output %q", err, output)
	}
	if !strings.Contains(string(output), "returned non-zero exit status 7") {
		t.Fatalf("feedback failure lacks cause: %q", output)
	}
}

func TestReviewLoopReportsTerminalWaitOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		exit    string
		message string
	}{
		{name: "timeout", exit: "11", message: "timed out waiting"},
		{name: "head changed", exit: "12", message: "changed from expected head abc123"},
		{name: "helper failure", exit: "9", message: "review waiter failed with exit code 9"},
		{name: "repair limit", exit: "10", message: "review was not approved after 3 repairs"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			scripts := filepath.Join(directory, "scripts")
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			codex := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"session-123\"}'\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
				t.Fatal(err)
			}
			gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":42,\"url\":\"https://github.test/pull/42\",\"state\":\"OPEN\",\"isDraft\":false,\"headRefOid\":\"abc123\"}'\n"
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
				t.Fatal(err)
			}
			waiter := "#!/bin/sh\nexit " + test.exit + "\n"
			if err := os.WriteFile(filepath.Join(scripts, "wait-for-review.sh"), []byte(waiter), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "read-review-feedback.sh"), []byte("#!/bin/sh\nprintf feedback\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("python3", absoluteScript)
			command.Dir = directory
			command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
			command.Stdin = bytes.NewBufferString("implement request")
			output, err := command.CombinedOutput()
			if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
				t.Fatalf("wait outcome = %v, output %q", err, output)
			}
			if !strings.Contains(string(output), test.message) {
				t.Fatalf("wait outcome lacks %q: %q", test.message, output)
			}
		})
	}
}

func TestReviewLoopRequiresCodexSessionEvent(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"item.completed\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", absoluteScript)
	command.Dir = directory
	command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	command.Stdin = bytes.NewBufferString("implement request")
	output, err := command.CombinedOutput()
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("missing session event = %v, output %q", err, output)
	}
	if !strings.Contains(string(output), "without a thread.started event") {
		t.Fatalf("missing session event lacks cause: %q", output)
	}
}

func TestReviewLoopRejectsMalformedCodexJSONL(t *testing.T) {
	tests := []struct {
		name  string
		codex string
	}{
		{
			name: "after session event",
			codex: `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
printf 'not json\n'
`,
		},
		{
			name: "during resume",
			codex: `#!/bin/sh
cat >/dev/null
if [ "$2" = resume ]; then
  printf 'not json\n'
else
  printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
fi
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			scripts := filepath.Join(directory, "scripts")
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(test.codex), 0o700); err != nil {
				t.Fatal(err)
			}
			gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":42,\"url\":\"https://github.test/pull/42\",\"state\":\"OPEN\",\"isDraft\":false,\"headRefOid\":\"abc123\"}'\n"
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "wait-for-review.sh"), []byte("#!/bin/sh\nexit 10\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "read-review-feedback.sh"), []byte("#!/bin/sh\nprintf feedback\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("python3", absoluteScript)
			command.Dir = directory
			command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
			command.Stdin = bytes.NewBufferString("implement request")
			output, err := command.CombinedOutput()
			if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
				t.Fatalf("malformed JSONL = %v, output %q", err, output)
			}
			if !strings.Contains(string(output), "Codex emitted malformed JSONL output") {
				t.Fatalf("malformed JSONL lacks cause: %q", output)
			}
		})
	}
}

func TestReviewLoopKillsCodexDescendantsOnMalformedOutput(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := `#!/bin/sh
/bin/sleep 30 &
printf '%s' "$!" > "$CHILD_PID_FILE"
printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
printf 'not json\n'
wait
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(directory, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", absoluteScript)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"CHILD_PID_FILE="+childPIDFile,
	)
	command.Stdin = bytes.NewBufferString("implement request")
	output, err := command.CombinedOutput()
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("malformed JSONL = %v, output %q", err, output)
	}
	rawPID, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessExit(t, pid)
}

func TestReviewLoopCleansUpDescendantsAfterCodexExit(t *testing.T) {
	tests := []struct {
		name    string
		codex   string
		message string
	}{
		{
			name: "nonzero exit with inherited stdout",
			codex: `#!/bin/sh
/bin/sleep 30 &
printf '%s' "$!" > "$CHILD_PID_FILE"
exit 7
`,
			message: "returned non-zero exit status 7",
		},
		{
			name: "missing session with quiet child",
			codex: `#!/bin/sh
/bin/sleep 30 >/dev/null &
printf '%s' "$!" > "$CHILD_PID_FILE"
printf '%s\n' '{"type":"item.completed"}'
`,
			message: "without a thread.started event",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(test.codex), 0o700); err != nil {
				t.Fatal(err)
			}
			absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
			if err != nil {
				t.Fatal(err)
			}
			childPIDFile := filepath.Join(directory, "child.pid")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "python3", absoluteScript)
			command.Dir = directory
			command.Env = append(
				os.Environ(),
				"PATH="+bin+":/usr/bin:/bin",
				"CHILD_PID_FILE="+childPIDFile,
			)
			command.Stdin = bytes.NewBufferString("implement request")
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("review loop hung after Codex leader exit: %v", ctx.Err())
			}
			if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
				t.Fatalf("Codex exit cleanup = %v, output %q", err, output)
			}
			if !strings.Contains(string(output), test.message) {
				t.Fatalf("Codex exit cleanup lacks %q: %q", test.message, output)
			}
			rawPID, err := os.ReadFile(childPIDFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(rawPID))
			if err != nil {
				t.Fatal(err)
			}
			waitForProcessExit(t, pid)
		})
	}
}

func TestReviewLoopKeepsCodexInMachinistProcessGroup(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := `#!/bin/sh
/bin/sleep 30 &
printf '%s' "$!" > "$CHILD_PID_FILE"
wait
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(directory, "child.pid")
	command := exec.Command("python3", absoluteScript)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"CHILD_PID_FILE="+childPIDFile,
		"MACHINIST_RUN_ID=run_012345678901234567890123",
	)
	command.Stdin = bytes.NewBufferString("implement request")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }()

	deadline := time.Now().Add(2 * time.Second)
	var rawPID []byte
	for time.Now().Before(deadline) {
		rawPID, err = os.ReadFile(childPIDFile)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Codex child did not start: %v", err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	_ = command.Wait()
	waitForProcessExit(t, pid)
}

func TestReviewLoopBoundsManagedDrainWithNoisyDescendant(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
while :; do printf '%s\n' '{"type":"item.completed"}'; done &
printf '%s' "$!" > "$CHILD_PID_FILE"
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(directory, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", absoluteScript)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"CHILD_PID_FILE="+childPIDFile,
		"MACHINIST_RUN_ID=run_012345678901234567890123",
	)
	command.Stdin = bytes.NewBufferString("implement request")
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	started := time.Now()
	err = command.Run()
	if ctx.Err() != nil || time.Since(started) > 2*time.Second {
		t.Fatalf("managed output drain was not bounded: %v", ctx.Err())
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("workflow result = %v, want incomplete-stream failure", err)
	}
	if !strings.Contains(stderr.String(), "stdout remained open after its process exited") {
		t.Fatalf("incomplete stream lacks clear error: %q", stderr.String())
	}
	rawPID, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	waitForProcessExit(t, pid)
}

func TestReviewLoopAcceptsFiniteManagedOutputAfterLeaderExit(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	scripts := filepath.Join(directory, "scripts")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	codex := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"session-123"}'
/usr/bin/awk 'BEGIN { for (i = 0; i < 100000; i++) print "{\"type\":\"item.completed\"}" }' &
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(codex), 0o700); err != nil {
		t.Fatal(err)
	}
	gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":42,\"url\":\"https://github.test/pull/42\",\"state\":\"OPEN\",\"isDraft\":false,\"headRefOid\":\"abc123\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "wait-for-review.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absoluteScript, err := filepath.Abs(filepath.Join("workflows", "review-loop", "review_loop.py"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", absoluteScript)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"MACHINIST_RUN_ID=run_012345678901234567890123",
	)
	command.Stdin = bytes.NewBufferString("implement request")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Run(); err != nil {
		t.Fatalf("finite managed output failed: %v", err)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := child.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = child.Kill()
	t.Fatalf("Codex descendant %d survived process-group cleanup", pid)
}
