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

// TestFlowAddressesOneRoundThenStopsOnApproval runs flow.py against a local bare
// remote, a fake openai_codex package whose thread commits a file on every turn, and a
// fake gh that reports one review thread and then an approval.
func TestFlowAddressesOneRoundThenStopsOnApproval(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	directory := t.TempDir()
	remote := filepath.Join(directory, "remote.git")
	repository := filepath.Join(directory, "repository")
	bin := filepath.Join(directory, "bin")
	site := filepath.Join(directory, "site", "openai_codex")
	state := filepath.Join(directory, "gh-calls")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "--initial-branch=main", remote},
		{"init", "--quiet", "--initial-branch=main", repository},
		{"-C", repository, "config", "user.email", "test@example.com"},
		{"-C", repository, "config", "user.name", "Test"},
		{"-C", repository, "commit", "--quiet", "--allow-empty", "-m", "initial"},
		{"-C", repository, "remote", "add", "origin", remote},
		{"-C", repository, "push", "--quiet", "-u", "origin", "main"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	for _, path := range []string{bin, site} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The fake SDK records each prompt and commits a change, standing in for Codex.
	sdk := `import enum, os, subprocess

class Sandbox(str, enum.Enum):
    read_only = "read-only"
    workspace_write = "workspace-write"
    full_access = "full-access"

class TurnStatus(enum.Enum):
    completed = "completed"
    failed = "failed"

class Usage:
    def __init__(self):
        self.input_tokens, self.cached_input_tokens, self.output_tokens = 10, 4, 5

class TurnResult:
    def __init__(self, response):
        self.id, self.status, self.error, self.final_response = "turn-1", TurnStatus.completed, None, response
        self.usage = type("U", (), {"total": Usage()})()

class Thread:
    id = "thread-123"
    def run(self, prompt, output_schema=None, **kwargs):
        with open(os.environ["AGENT_LOG"], "a") as log:
            log.write(prompt + "\n---\n")
        if output_schema is not None:
            return TurnResult('{"title": "feat: add --json flag", "body": "Summary\\n\\nFixes #1"}')
        with open("main.go", "a") as source:
            source.write("change\n")
        subprocess.run(["git", "add", "main.go"], check=True)
        subprocess.run(["git", "commit", "--quiet", "-m", "agent change"], check=True)
        return TurnResult("done")

class Codex:
    def __enter__(self):
        return self
    def __exit__(self, *args):
        return False
    def thread_start(self, **kwargs):
        return Thread()
`
	if err := os.WriteFile(filepath.Join(site, "__init__.py"), []byte(sdk), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "types.py"), []byte("from openai_codex import TurnStatus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake gh answers by subcommand. GraphQL answers change with each call so the
	// first round carries a fresh review thread and the second an approval.
	gh := `#!/bin/sh
case "$1 $2" in
"repo view") printf '{"nameWithOwner":"owner/repo","defaultBranchRef":{"name":"main"}}\n' ;;
"api user") printf '{"login":"bot"}\n' ;;
"pr create") printf '%s\n' "$*" >> "$GH_LOG"; printf 'https://github.com/owner/repo/pull/7\n' ;;
"pr checks")
  if [ "$(cat "$GH_STATE")" = 1 ]; then printf 'no checks reported on the branch\n' >&2; exit 1; fi
  printf '[{"name":"tests","bucket":"pass","link":""}]\n' ;;
"api graphql")
  count=0; [ ! -f "$GH_STATE" ] || count=$(cat "$GH_STATE"); count=$((count + 1)); printf '%s' "$count" > "$GH_STATE"
  if [ "$count" -eq 1 ]; then
    printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"PRRT_1","isResolved":false,"path":"main.go","line":3,"comments":{"nodes":[{"author":{"login":"reviewer"},"body":"Handle the empty case","createdAt":"2999-01-01T00:00:00Z","url":"https://github.com/owner/repo/pull/7#r1"}]}}]},"reviews":{"nodes":[]}}}}}'
  else
    printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]},"reviews":{"nodes":[{"author":{"login":"reviewer"},"state":"APPROVED","submittedAt":"2999-01-01T00:00:00Z"}]}}}}}'
  fi ;;
*) printf 'unexpected gh %s\n' "$*" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("workflows", "flow", "flow.py"))
	if err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(directory, "agent.log")
	ghLog := filepath.Join(directory, "gh.log")
	usage := filepath.Join(directory, "usage")
	command := exec.Command(python, script)
	command.Dir = repository
	command.Stdin = bytes.NewBufferString("Add a --json flag")
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"PYTHONPATH="+filepath.Dir(site),
		"GH_STATE="+state,
		"AGENT_LOG="+agentLog,
		"GH_LOG="+ghLog,
		"MACHINIST_TOKEN_USAGE_PATH="+usage,
		"FLOW_BASE_BRANCH=main",
		"FLOW_MAX_ROUNDS=2",
		"FLOW_FEEDBACK_WAIT=0",
		"FLOW_POLL_INTERVAL=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("flow: %v: %s", err, output)
	}
	text := string(output)
	for _, want := range []string{"thread thread-123", "opened https://github.com/owner/repo/pull/7", "address feedback: round 1/2", "approved with green checks"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	prompts, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(prompts), "---") != 3 || !strings.Contains(string(prompts), "Add a --json flag") || !strings.Contains(string(prompts), "PRRT_1") || !strings.Contains(string(prompts), "Handle the empty case") {
		t.Fatalf("agent prompts = %s", prompts)
	}
	created, err := os.ReadFile(ghLog)
	if err != nil || !strings.Contains(string(created), "--title feat: add --json flag --body Summary") {
		t.Fatalf("gh pr create arguments = %s, %v", created, err)
	}
	if tokens, err := os.ReadFile(usage); err != nil || strings.TrimSpace(string(tokens)) != "15" {
		t.Fatalf("token usage = %q, %v", tokens, err)
	}
	pushed, err := exec.Command("git", "-C", remote, "log", "--oneline", "--all").Output()
	if err != nil || strings.Count(string(pushed), "agent change") != 2 {
		t.Fatalf("remote log = %s, %v", pushed, err)
	}
}
