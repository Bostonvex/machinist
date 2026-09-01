package examples

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
printf '%s\n' '{"number":42,"url":"https://github.test/pull/42","state":"OPEN","isDraft":false,"headRefOid":"abc123"}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	helperLog := filepath.Join(directory, "helpers.log")
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
