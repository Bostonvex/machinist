package notes

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const goodPlan = `---
kind: plan
title: Move the ticker off the relay
date: 2026-09-01
subject: Bostonvex/machinist#8
status: active
---

The ticker reads the relay at every tick.
`

func parseForTest(t *testing.T, body string) Note {
	t.Helper()
	note, err := Parse("notes/plans/x.md", strings.NewReader(body))
	if err != nil {
		t.Fatalf("a well-formed note was refused: %v", err)
	}
	return note
}

func refusalFor(t *testing.T, body string) string {
	t.Helper()
	_, err := Parse("notes/plans/x.md", strings.NewReader(body))
	if err == nil {
		t.Fatal("a malformed note was accepted")
	}
	return err.Error()
}

func TestAWellFormedPlanIsRead(t *testing.T) {
	note := parseForTest(t, goodPlan)
	if note.Kind != KindPlan || note.Title != "Move the ticker off the relay" ||
		note.Subject != "Bostonvex/machinist#8" || note.Status != StatusActive {
		t.Fatalf("note = %#v", note)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !note.Date.Equal(want) {
		t.Fatalf("date = %v, want %v", note.Date, want)
	}
	if !strings.HasPrefix(note.Body, "The ticker reads") {
		t.Fatalf("body = %q", note.Body)
	}
}

// A typo in a required field would otherwise read as the field being absent,
// and the note would be refused for the wrong reason.
func TestAnUnknownFrontMatterFieldIsRefused(t *testing.T) {
	message := refusalFor(t, strings.Replace(goodPlan, "subject:", "subjet:", 1))
	if !strings.Contains(message, `"subjet"`) {
		t.Fatalf("the refusal does not name the field: %s", message)
	}
}

func TestEveryRequiredFieldIsRequired(t *testing.T) {
	for _, field := range requiredFields {
		t.Run(field, func(t *testing.T) {
			var kept []string
			for _, line := range strings.Split(goodPlan, "\n") {
				if strings.HasPrefix(line, field+":") {
					continue
				}
				kept = append(kept, line)
			}
			message := refusalFor(t, strings.Join(kept, "\n"))
			if !strings.Contains(message, field) {
				t.Fatalf("the refusal does not name %q: %s", field, message)
			}
		})
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	// The status line goes too. With it left in, the note is refused for
	// carrying a status no non-plan may have, and the test would pass without
	// the kind ever having been looked up.
	body := strings.Replace(strings.Replace(goodPlan, "kind: plan", "kind: musing", 1), "status: active\n", "", 1)
	message := refusalFor(t, body)
	if !strings.Contains(message, "unknown note kind") || !strings.Contains(message, "musing") {
		t.Fatalf("the refusal does not name the kind: %s", message)
	}
}

// A date that cannot be read is not today. Guessing would put the note in the
// wrong place in the one ordering a reader relies on.
func TestADateThatCannotBeReadIsRefused(t *testing.T) {
	message := refusalFor(t, strings.Replace(goodPlan, "date: 2026-09-01", "date: last Tuesday", 1))
	if !strings.Contains(message, "YYYY-MM-DD") {
		t.Fatalf("the refusal does not say what a date looks like: %s", message)
	}
}

func TestAPlanNeedsAStatus(t *testing.T) {
	// An absent status and a misspelled one are different mistakes, and the
	// message has to say which. Asking only for the word "status" would be
	// answered by the refusal of any status at all.
	message := refusalFor(t, strings.Replace(goodPlan, "status: active\n", "", 1))
	if !strings.Contains(message, "a plan needs a status") {
		t.Fatalf("the refusal does not say the status is missing: %s", message)
	}
}

func TestAPlanStatusOutsideTheSetIsRefused(t *testing.T) {
	message := refusalFor(t, strings.Replace(goodPlan, "status: active", "status: mostly-done", 1))
	if !strings.Contains(message, "mostly-done") {
		t.Fatalf("the refusal does not name the status: %s", message)
	}
}

// Research and work logs record what already happened. A status on one invites
// the reader to think it might have changed since.
func TestOnlyAPlanCarriesAStatus(t *testing.T) {
	body := strings.Replace(goodPlan, "kind: plan", "kind: research", 1)
	message := refusalFor(t, body)
	if !strings.Contains(message, "only a plan has") {
		t.Fatalf("the refusal does not say why the status is wrong there: %s", message)
	}
}

func TestAResearchNoteNeedsNoStatus(t *testing.T) {
	body := strings.Replace(strings.Replace(goodPlan, "kind: plan", "kind: research", 1), "status: active\n", "", 1)
	if note := parseForTest(t, body); note.Kind != KindResearch || note.Status != "" {
		t.Fatalf("note = %#v", note)
	}
}

func TestMissingFrontMatterIsRefused(t *testing.T) {
	message := refusalFor(t, "# A plan\n\nno front matter here\n")
	if !strings.Contains(message, "front-matter fence") {
		t.Fatalf("the refusal does not say what is missing: %s", message)
	}
}

func TestUnclosedFrontMatterIsRefused(t *testing.T) {
	body := strings.Replace(goodPlan, "status: active\n---\n", "status: active\n", 1)
	message := refusalFor(t, body)
	if !strings.Contains(message, "never closed") {
		t.Fatalf("the refusal does not say the fence is missing: %s", message)
	}
}

// A note with nothing under the front matter records nothing, and would sit in
// the listing looking like knowledge.
func TestANoteWithNoBodyIsRefused(t *testing.T) {
	parts := strings.SplitAfter(goodPlan, "---\n")
	message := refusalFor(t, parts[0]+parts[1]+"\n")
	if !strings.Contains(message, "nothing else") {
		t.Fatalf("the refusal does not say the body is empty: %s", message)
	}
}

func TestARepeatedFieldIsRefused(t *testing.T) {
	message := refusalFor(t, strings.Replace(goodPlan, "status: active", "title: Something else\nstatus: active", 1))
	if !strings.Contains(message, "twice") {
		t.Fatalf("the refusal does not say the field repeats: %s", message)
	}
}

func TestRenderRoundTrips(t *testing.T) {
	note := parseForTest(t, goodPlan)
	again, err := Parse("notes/plans/x.md", strings.NewReader(note.Render()))
	if err != nil {
		t.Fatalf("a rendered note was refused: %v", err)
	}
	if again.Kind != note.Kind || again.Title != note.Title || !again.Date.Equal(note.Date) ||
		again.Subject != note.Subject || again.Status != note.Status || again.Body != note.Body {
		t.Fatalf("round trip changed the note:\n%#v\n%#v", note, again)
	}
}

func TestTheFileNameIsDatedFirst(t *testing.T) {
	note := parseForTest(t, goodPlan)
	name, err := note.FileName()
	if err != nil {
		t.Fatal(err)
	}
	if want := "notes/plans/2026-09-01-move-the-ticker-off-the-relay.md"; name != want {
		t.Fatalf("name = %q, want %q", name, want)
	}
}

func TestATitleWithNothingToNameAFileWithIsRefused(t *testing.T) {
	note := parseForTest(t, goodPlan)
	note.Title = "?!--"
	if _, err := note.FileName(); err == nil {
		t.Fatal("a title with no letters or digits produced a file name")
	}
}

func notesFS(files map[string]string) fstest.MapFS {
	system := fstest.MapFS{}
	for name, body := range files {
		system[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return system
}

func TestReadAllOrdersNewestFirst(t *testing.T) {
	older := strings.Replace(goodPlan, "2026-09-01", "2026-08-01", 1)
	system := notesFS(map[string]string{
		"notes/plans/2026-09-01-a.md": goodPlan,
		"notes/plans/2026-08-01-b.md": older,
	})
	collected, err := ReadAll(system)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != 2 {
		t.Fatalf("read %d notes, want 2", len(collected))
	}
	if collected[0].Path != "notes/plans/2026-09-01-a.md" {
		t.Fatalf("the newest note is not first: %v", collected[0].Path)
	}
}

// A partial answer to "what do we know" is the one shape of answer that is
// worse than none, so one unreadable note fails the whole read.
func TestOneUnreadableNoteFailsTheWholeRead(t *testing.T) {
	system := notesFS(map[string]string{
		"notes/plans/2026-09-01-a.md": goodPlan,
		"notes/plans/2026-08-01-b.md": "not a note at all\n",
	})
	if _, err := ReadAll(system); err == nil {
		t.Fatal("an unreadable note was skipped")
	}
}

func TestAMisfiledNoteIsRefused(t *testing.T) {
	system := notesFS(map[string]string{"notes/research/2026-09-01-a.md": goodPlan})
	_, err := ReadAll(system)
	if err == nil {
		t.Fatal("a plan filed under research was accepted")
	}
	if !strings.Contains(err.Error(), "notes/plans") {
		t.Fatalf("the refusal does not say where it belongs: %v", err)
	}
}

func TestAReadmeIsNotANote(t *testing.T) {
	system := notesFS(map[string]string{
		"notes/README.md":             "# Notes\n\nWhat lives here.\n",
		"notes/plans/2026-09-01-a.md": goodPlan,
	})
	collected, err := ReadAll(system)
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != 1 {
		t.Fatalf("read %d notes, want 1", len(collected))
	}
}

// An unwritten record and an unreadable one call for different answers.
func TestAnAbsentNotesDirectoryIsItsOwnAnswer(t *testing.T) {
	if _, err := ReadAll(notesFS(map[string]string{"docs/x.md": "hello"})); err != ErrNoNotes {
		t.Fatalf("err = %v, want ErrNoNotes", err)
	}
}
