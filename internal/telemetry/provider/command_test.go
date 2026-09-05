package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestACommandRunsWithoutAShell(t *testing.T) {
	// If argv went through a shell, this would write a file. It runs /bin/echo
	// with three arguments instead, and the semicolon is one of them.
	output, err := RunBounded(context.Background(), []string{"/bin/echo", "a;", "b"}, time.Second, 4096)
	if err != nil {
		t.Fatalf("a plain command failed: %v", err)
	}
	if strings.TrimSpace(string(output)) != "a; b" {
		t.Fatalf("output was %q", output)
	}
}

func TestACommandThatHangsCostsOnePoll(t *testing.T) {
	// A provider polls every few seconds. A command with no timeout would hold
	// the poller open for as long as the command felt like running.
	started := time.Now()
	_, err := RunBounded(context.Background(), []string{"/bin/sleep", "30"}, 300*time.Millisecond, 4096)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("a hanging command was not refused: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the timeout did not bound the command; it took %s", elapsed)
	}
}

func TestACommandThatFloodsIsRefusedRatherThanTruncated(t *testing.T) {
	// Truncating would hand the parser half a line, and half a number parses.
	_, err := RunBounded(context.Background(),
		[]string{"/bin/sh", "-c", "yes 1234567890 | head -n 20000"}, 5*time.Second, 1024)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("a flood of output was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("the refusal did not say why: %v", err)
	}
}

func TestACommandThatFailsIsAFailureEvenWithOutput(t *testing.T) {
	_, err := RunBounded(context.Background(), []string{"/bin/sh", "-c", "echo partial; exit 3"}, time.Second, 4096)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("a non-zero exit was accepted: %v", err)
	}
}

func TestAnArgumentCannotSmuggleANewline(t *testing.T) {
	// Downstream of a provider, output is split on newlines. An argument that
	// carries one is either a configuration mistake or an attempt to end one
	// field and start another.
	_, err := RunBounded(context.Background(), []string{"/bin/echo", "one\ntwo"}, time.Second, 4096)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("a control character in an argument was accepted: %v", err)
	}
}

func TestArgvIsBounded(t *testing.T) {
	long := make([]string, maximumArguments+1)
	for index := range long {
		long[index] = "/bin/echo"
	}
	if _, err := RunBounded(context.Background(), long, time.Second, 4096); !errors.Is(err, ErrCommand) {
		t.Fatal("an unbounded argv was accepted")
	}
	if _, err := RunBounded(context.Background(), nil, time.Second, 4096); !errors.Is(err, ErrCommand) {
		t.Fatal("an empty argv was accepted")
	}
	huge := []string{"/bin/echo", strings.Repeat("a", maximumArgument+1)}
	if _, err := RunBounded(context.Background(), huge, time.Second, 4096); !errors.Is(err, ErrCommand) {
		t.Fatal("an unbounded argument was accepted")
	}
}

func TestATimeoutBeyondTheCeilingIsRefused(t *testing.T) {
	// A poller is not the place to wait a minute for a reading that is stale by
	// the time it arrives.
	_, err := RunBounded(context.Background(), []string{"/bin/echo", "hello"}, maximumTimeout+time.Second, 4096)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("an unbounded timeout was accepted: %v", err)
	}
}

func TestAFailureNeverRepeatsTheCommandOrItsOutput(t *testing.T) {
	// A failing provider writes to the log on every poll. Whatever it read is
	// output the collector already refused to trust, and it would end up in a
	// file that gets grepped and pasted into issues.
	secret := "ghp_00000000000000000000000000000000000000"
	_, err := RunBounded(context.Background(),
		[]string{"/bin/sh", "-c", "echo " + secret + "; exit 1"}, time.Second, 4096)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/bin/sh") {
		t.Fatalf("the error repeated the command or its output: %v", err)
	}
}
