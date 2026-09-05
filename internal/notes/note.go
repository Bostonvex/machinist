// Package notes implements durable knowledge as repository artifacts: the
// plans, research and work logs an agent leaves behind so the next session
// reads a record instead of reconstructing one.
//
// The record only helps if it can be trusted, so every field fails closed. An
// unknown kind is not a note of some other sort, an unrecognized front-matter
// field is an error rather than something to ignore, and a date that cannot be
// parsed is not today. A note that cannot be read is reported as unreadable,
// never skipped: a silently dropped work log reads exactly like work that was
// never done.
package notes

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Kind is what a note is for. The three are separate because they answer
// different questions and are written at different moments: a plan before the
// work, research while a question is open, a work log after something changed.
type Kind string

const (
	// KindPlan is what is intended, written before the work starts.
	KindPlan Kind = "plan"
	// KindResearch is what was found out, written while a question is open.
	KindResearch Kind = "research"
	// KindWorkLog is what actually happened, written after something changed.
	KindWorkLog Kind = "work-log"
)

// Directory is the subdirectory of the notes root that holds a kind. The
// mapping is exhaustive: a kind with no directory cannot be filed, which is
// caught here rather than by writing it somewhere arbitrary.
var directories = map[Kind]string{
	KindPlan:     "plans",
	KindResearch: "research",
	KindWorkLog:  "work-logs",
}

// Root is the directory, relative to the repository, that holds every note.
const Root = "notes"

// Statuses a plan may be in. Research and work logs have none: they record
// something that already happened and do not later become untrue, they become
// superseded by a later note that says so.
const (
	StatusDraft      = "draft"
	StatusActive     = "active"
	StatusSuperseded = "superseded"
	StatusDone       = "done"
)

var planStatuses = []string{StatusDraft, StatusActive, StatusSuperseded, StatusDone}

// requiredFields is every front-matter key a note must carry, and the full set
// it may carry. Both directions matter: a missing field leaves the record
// incomplete, and an unrecognized one is usually a typo in a required one,
// which would otherwise be read as "absent" and refused for the wrong reason.
var requiredFields = []string{"kind", "title", "date", "subject"}

// Note is one durable-knowledge artifact.
type Note struct {
	Kind    Kind
	Title   string
	Date    time.Time
	Subject string
	Status  string
	Body    string
	Path    string
}

// Directory reports where a note of this kind is filed.
func (k Kind) Directory() (string, error) {
	directory, ok := directories[k]
	if !ok {
		return "", fmt.Errorf("unknown note kind %q (want one of %s)", string(k), strings.Join(KindNames(), ", "))
	}
	return directory, nil
}

// KindNames lists every kind, in a stable order, for use in messages.
func KindNames() []string {
	names := make([]string, 0, len(directories))
	for kind := range directories {
		names = append(names, string(kind))
	}
	sort.Strings(names)
	return names
}

// ParseKind reads a kind, refusing anything that is not one.
func ParseKind(value string) (Kind, error) {
	kind := Kind(strings.TrimSpace(value))
	if _, err := kind.Directory(); err != nil {
		return "", err
	}
	return kind, nil
}

const fence = "---"

// Parse reads a note. The name is carried through only so the error says which
// file is wrong; nothing about the content is inferred from it, because a note
// filed in the wrong directory is a thing to report rather than to believe.
func Parse(name string, reader io.Reader) (Note, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Note{}, fmt.Errorf("%s: %w", name, err)
		}
		return Note{}, fmt.Errorf("%s: the file is empty", name)
	}
	if strings.TrimRight(scanner.Text(), " \t") != fence {
		return Note{}, fmt.Errorf("%s: line 1 is %q, want the front-matter fence %q", name, scanner.Text(), fence)
	}

	fields := map[string]string{}
	closed := false
	line := 1
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.TrimRight(text, " \t") == fence {
			closed = true
			break
		}
		// Front matter has no blank lines, so one means the closing fence was
		// left out and the body has already started. Reporting that as "not a
		// field" would send the writer looking at the wrong line.
		if strings.TrimSpace(text) == "" {
			return Note{}, fmt.Errorf("%s:%d: the front matter is never closed by a %q line", name, line, fence)
		}
		key, value, found := strings.Cut(text, ":")
		if !found {
			return Note{}, fmt.Errorf("%s:%d: %q is not a front-matter field (want key: value); "+
				"if the front matter ended above, it is missing its closing %q", name, line, text, fence)
		}
		key = strings.TrimSpace(key)
		if _, repeated := fields[key]; repeated {
			return Note{}, fmt.Errorf("%s:%d: %q is given twice", name, line, key)
		}
		fields[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return Note{}, fmt.Errorf("%s: %w", name, err)
	}
	if !closed {
		return Note{}, fmt.Errorf("%s: the front matter is never closed by a %q line", name, fence)
	}

	var body strings.Builder
	for scanner.Scan() {
		body.WriteString(scanner.Text())
		body.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return Note{}, fmt.Errorf("%s: %w", name, err)
	}

	note, err := fromFields(name, fields)
	if err != nil {
		return Note{}, err
	}
	note.Body = strings.TrimLeft(body.String(), "\n")
	note.Path = name
	if strings.TrimSpace(note.Body) == "" {
		return Note{}, fmt.Errorf("%s: the note has front matter and nothing else", name)
	}
	return note, nil
}

func fromFields(name string, fields map[string]string) (Note, error) {
	allowed := map[string]bool{"status": true}
	for _, field := range requiredFields {
		allowed[field] = true
	}
	unknown := make([]string, 0)
	for field := range fields {
		if !allowed[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Note{}, fmt.Errorf("%s: unknown front-matter field(s) %s", name, strings.Join(quoted(unknown), ", "))
	}
	for _, field := range requiredFields {
		if fields[field] == "" {
			return Note{}, fmt.Errorf("%s: %q is required and is empty or absent", name, field)
		}
	}

	kind, err := ParseKind(fields["kind"])
	if err != nil {
		return Note{}, fmt.Errorf("%s: %w", name, err)
	}
	// A note carries a date rather than a timestamp. The hour it was written
	// is not what a later reader needs, and a timestamp invites a timezone
	// that would make two notes written on one day sort into two.
	date, err := time.Parse(time.DateOnly, fields["date"])
	if err != nil {
		return Note{}, fmt.Errorf("%s: %q is not a date of the form YYYY-MM-DD: %w", name, fields["date"], err)
	}

	note := Note{Kind: kind, Title: fields["title"], Date: date, Subject: fields["subject"], Status: fields["status"]}
	if err := note.checkStatus(name); err != nil {
		return Note{}, err
	}
	return note, nil
}

func (n Note) checkStatus(name string) error {
	if n.Kind != KindPlan {
		if n.Status != "" {
			return fmt.Errorf("%s: %q carries a status, which only a plan has", name, string(n.Kind))
		}
		return nil
	}
	if n.Status == "" {
		return fmt.Errorf("%s: a plan needs a status (one of %s)", name, strings.Join(planStatuses, ", "))
	}
	for _, status := range planStatuses {
		if n.Status == status {
			return nil
		}
	}
	return fmt.Errorf("%s: %q is not a plan status (want one of %s)", name, n.Status, strings.Join(planStatuses, ", "))
}

func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = fmt.Sprintf("%q", value)
	}
	return out
}

// Render writes a note back out in the form Parse reads.
func (n Note) Render() string {
	var out strings.Builder
	out.WriteString(fence + "\n")
	out.WriteString("kind: " + string(n.Kind) + "\n")
	out.WriteString("title: " + n.Title + "\n")
	out.WriteString("date: " + n.Date.Format(time.DateOnly) + "\n")
	out.WriteString("subject: " + n.Subject + "\n")
	if n.Kind == KindPlan {
		out.WriteString("status: " + n.Status + "\n")
	}
	out.WriteString(fence + "\n\n")
	body := strings.TrimRight(n.Body, "\n")
	if body != "" {
		out.WriteString(body + "\n")
	}
	return out.String()
}

var separators = regexp.MustCompile(`-+`)

// Slug turns a title into the file-name half of a note's path. It is not
// reversible and is not meant to be: the title lives in the front matter, and
// the name only has to be stable, ordered and unsurprising in a directory
// listing.
func Slug(title string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
		default:
			out.WriteRune('-')
		}
	}
	return strings.Trim(separators.ReplaceAllString(out.String(), "-"), "-")
}

// FileName is where a note belongs under Root, dated first so a directory
// listing is a timeline.
func (n Note) FileName() (string, error) {
	directory, err := n.Kind.Directory()
	if err != nil {
		return "", err
	}
	slug := Slug(n.Title)
	if slug == "" {
		return "", fmt.Errorf("%q has no letters or digits to name a file with", n.Title)
	}
	return path.Join(Root, directory, n.Date.Format(time.DateOnly)+"-"+slug+".md"), nil
}

// ErrNoNotes reports that the notes root is absent. It is separated from a
// read failure because an unwritten record and an unreadable one call for
// different answers.
var ErrNoNotes = errors.New("no notes directory")

// ReadAll parses every note under Root. It reports the first file it cannot
// read rather than returning what it managed to parse: a partial answer to
// "what do we know" is the one shape of answer that is worse than none.
func ReadAll(system fs.FS) ([]Note, error) {
	if _, err := fs.Stat(system, Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoNotes
		}
		return nil, err
	}
	var collected []Note
	err := fs.WalkDir(system, Root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		if path.Base(name) == "README.md" {
			return nil
		}
		file, err := system.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		note, err := Parse(name, file)
		if err != nil {
			return err
		}
		if err := note.checkFiling(name); err != nil {
			return err
		}
		collected = append(collected, note)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(collected, func(i, j int) bool {
		if !collected[i].Date.Equal(collected[j].Date) {
			return collected[i].Date.After(collected[j].Date)
		}
		return collected[i].Path < collected[j].Path
	})
	return collected, nil
}

// checkFiling refuses a note whose kind and directory disagree. Listing by
// kind reads the front matter, so a misfiled note would still be found; the
// reason to refuse it is that a person browsing the tree would not find it,
// and the tree is the interface this whole package exists to keep honest.
func (n Note) checkFiling(name string) error {
	directory, err := n.Kind.Directory()
	if err != nil {
		return err
	}
	want := path.Join(Root, directory)
	if got := path.Dir(name); got != want {
		return fmt.Errorf("%s: a %s belongs in %s, not %s", name, string(n.Kind), want, got)
	}
	return nil
}
