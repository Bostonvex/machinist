package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runNotes(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), append([]string{"notes"}, args...), strings.NewReader(stdin), &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), code
}

func notesRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeNoteFile(t *testing.T, root, relative, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testPlan = `---
kind: plan
title: Move the ticker off the relay
date: 2026-09-01
subject: Bostonvex/machinist#8
status: active
---

The ticker reads the relay at every tick.
`

func TestNewWritesANoteThatReadsBack(t *testing.T) {
	root := notesRoot(t)
	stdout, stderr, code := runNotes(t, "", "new", "--root", root, "--kind", "work-log",
		"--title", "Deployed the collector", "--subject", "Bostonvex/machinist#6", "--date", "2026-09-05")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	relative := strings.TrimSpace(stdout)
	if want := "notes/work-logs/2026-09-05-deployed-the-collector.md"; relative != want {
		t.Fatalf("wrote %q, want %q", relative, want)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
		t.Fatalf("the note it named is not there: %v", err)
	}
	if _, _, code := runNotes(t, "", "check", "--root", root); code != 0 {
		t.Fatal("a note machinist just wrote does not pass its own check")
	}
}

// Two notes on one subject on one day is ordinary. Overwriting the first with
// the second is how a record quietly loses half of itself.
func TestNewRefusesToOverwriteANote(t *testing.T) {
	root := notesRoot(t)
	args := []string{"new", "--root", root, "--kind", "research", "--title", "Why the proxy aborts",
		"--subject", "Bostonvex/machinist#59", "--date", "2026-09-05"}
	if _, stderr, code := runNotes(t, "", args...); code != 0 {
		t.Fatalf("the first write failed: %s", stderr)
	}
	_, stderr, code := runNotes(t, "", args...)
	if code == 0 {
		t.Fatal("the second write overwrote the first")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("the refusal does not say the note is there: %s", stderr)
	}
}

func TestNewRefusesAKindItCannotFile(t *testing.T) {
	root := notesRoot(t)
	_, stderr, code := runNotes(t, "", "new", "--root", root, "--kind", "musing",
		"--title", "Something", "--subject", "x")
	if code == 0 {
		t.Fatal("a note of an unknown kind was written")
	}
	if !strings.Contains(stderr, "unknown note kind") {
		t.Fatalf("the refusal does not name the kind: %s", stderr)
	}
}

func TestNewRefusesAPlanWithoutAStatus(t *testing.T) {
	root := notesRoot(t)
	_, stderr, code := runNotes(t, "", "new", "--root", root, "--kind", "plan",
		"--title", "Something", "--subject", "x")
	if code == 0 {
		t.Fatal("a plan with no status was written")
	}
	if !strings.Contains(stderr, "a plan needs a status") {
		t.Fatalf("the refusal does not say the status is missing: %s", stderr)
	}
}

func TestNewTakesTheBodyFromStandardInput(t *testing.T) {
	root := notesRoot(t)
	body := "The tunnel was down for eleven minutes.\n"
	stdout, stderr, code := runNotes(t, body, "new", "--root", root, "--kind", "work-log",
		"--title", "Tunnel outage", "--subject", "dgx", "--date", "2026-09-05", "--body-file", "-")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	written, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(strings.TrimSpace(stdout))))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "eleven minutes") {
		t.Fatalf("the body did not reach the note:\n%s", written)
	}
}

func TestNewRefusesAnEmptyBody(t *testing.T) {
	root := notesRoot(t)
	_, stderr, code := runNotes(t, "   \n", "new", "--root", root, "--kind", "work-log",
		"--title", "Nothing", "--subject", "x", "--body-file", "-")
	if code == 0 {
		t.Fatal("a note with an empty body was written")
	}
	if !strings.Contains(stderr, "empty") {
		t.Fatalf("the refusal does not say the body is empty: %s", stderr)
	}
}

func TestListShowsWhatIsThere(t *testing.T) {
	root := notesRoot(t)
	writeNoteFile(t, root, "notes/plans/2026-09-01-a.md", testPlan)
	stdout, stderr, code := runNotes(t, "", "list", "--root", root)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"2026-09-01", "plan", "active", "Bostonvex/machinist#8", "Move the ticker"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the listing does not carry %q:\n%s", want, stdout)
		}
	}
}

func TestListFiltersByKind(t *testing.T) {
	root := notesRoot(t)
	writeNoteFile(t, root, "notes/plans/2026-09-01-a.md", testPlan)
	stdout, _, code := runNotes(t, "", "list", "--root", root, "--kind", "work-log")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(stdout, "Move the ticker") {
		t.Fatalf("a plan appeared under --kind work-log:\n%s", stdout)
	}
}

// The check is what a hook or a CI job runs, so it has to name every note that
// cannot be read. Reporting the first one turns a tidy-up into one CI run per
// broken file, which is how a check stops being run.
func TestCheckNamesEveryUnreadableNote(t *testing.T) {
	root := notesRoot(t)
	writeNoteFile(t, root, "notes/plans/2026-09-01-a.md", testPlan)
	writeNoteFile(t, root, "notes/plans/2026-08-01-b.md", "not a note\n")
	writeNoteFile(t, root, "notes/research/2026-07-01-c.md", "also not a note\n")

	_, stderr, code := runNotes(t, "", "check", "--root", root)
	if code == 0 {
		t.Fatal("a tree with two unreadable notes passed")
	}
	for _, want := range []string{"2026-08-01-b.md", "2026-07-01-c.md", "2 of 3"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("the report does not carry %q:\n%s", want, stderr)
		}
	}
}

func TestCheckRefusesAMisfiledNote(t *testing.T) {
	root := notesRoot(t)
	writeNoteFile(t, root, "notes/research/2026-09-01-a.md", testPlan)
	_, stderr, code := runNotes(t, "", "check", "--root", root)
	if code == 0 {
		t.Fatal("a plan filed under research passed the check")
	}
	if !strings.Contains(stderr, "notes/plans") {
		t.Fatalf("the report does not say where it belongs: %s", stderr)
	}
}

func TestCheckSaysWhenThereIsNoTree(t *testing.T) {
	_, stderr, code := runNotes(t, "", "check", "--root", notesRoot(t))
	if code == 0 {
		t.Fatal("a missing notes tree passed the check")
	}
	if !strings.Contains(stderr, "no notes directory") {
		t.Fatalf("the refusal does not say the tree is absent: %s", stderr)
	}
}

func TestAReadmeIsNotCheckedAsANote(t *testing.T) {
	root := notesRoot(t)
	writeNoteFile(t, root, "notes/README.md", "# Notes\n\nWhat lives here.\n")
	writeNoteFile(t, root, "notes/plans/2026-09-01-a.md", testPlan)
	stdout, stderr, code := runNotes(t, "", "check", "--root", root)
	if code != 0 {
		t.Fatalf("the README was checked as a note: %s", stderr)
	}
	if !strings.Contains(stdout, "1 notes read back") {
		t.Fatalf("stdout = %q", stdout)
	}
}
