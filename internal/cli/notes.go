package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/notes"
	"github.com/spf13/cobra"
)

// The notes verbs keep durable knowledge — what was intended, what was found
// out, what actually happened — in the repository rather than in a session
// that ends. They read and write plain files on purpose: the record has to
// outlive Machinist, and a directory of markdown does.
func newNotesCommand(options *commandOptions) *cobra.Command {
	notesCommand := &cobra.Command{
		Use:   "notes",
		Short: "Read and write durable knowledge kept in the repository",
	}
	notesCommand.AddCommand(newNotesNewCommand(options))
	notesCommand.AddCommand(newNotesListCommand(options))
	notesCommand.AddCommand(newNotesCheckCommand(options))
	return notesCommand
}

type notesOptions struct {
	root    string
	kind    string
	title   string
	subject string
	status  string
	body    string
	date    string
}

// notesRoot is the directory the notes tree hangs under. It defaults to the
// working directory rather than to the configured repository, because notes
// belong to the checkout being worked in, and an agent in a worktree must not
// write its record into a different one.
func (o *notesOptions) resolvedRoot() (string, error) {
	if o.root != "" {
		return filepath.Abs(o.root)
	}
	return os.Getwd()
}

func (o *notesOptions) addRootFlag(command *cobra.Command) {
	command.Flags().StringVar(&o.root, "root", "", "repository root that holds the notes directory (default: the working directory)")
}

func newNotesNewCommand(options *commandOptions) *cobra.Command {
	local := &notesOptions{}
	command := &cobra.Command{
		Use:   "new",
		Short: "Write a new plan, research note or work log",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return writeNote(command, options, local)
		},
	}
	command.Flags().StringVar(&local.kind, "kind", "", "one of "+strings.Join(notes.KindNames(), ", "))
	command.Flags().StringVar(&local.title, "title", "", "what the note is called")
	command.Flags().StringVar(&local.subject, "subject", "", "what it is about: an issue, a repository, a system")
	command.Flags().StringVar(&local.status, "status", "", "for a plan: draft, active, superseded or done")
	command.Flags().StringVar(&local.body, "body-file", "", "read the body from this file, or - for standard input")
	command.Flags().StringVar(&local.date, "date", "", "the note's date as YYYY-MM-DD (default: today)")
	local.addRootFlag(command)
	return command
}

func writeNote(command *cobra.Command, options *commandOptions, local *notesOptions) error {
	kind, err := notes.ParseKind(local.kind)
	if err != nil {
		return err
	}
	if strings.TrimSpace(local.title) == "" {
		return errors.New("a note needs a --title: it is what the next reader sees in the listing")
	}
	if strings.TrimSpace(local.subject) == "" {
		return errors.New("a note needs a --subject: what it is about, such as an issue or a system")
	}
	date := time.Now().UTC()
	if local.date != "" {
		date, err = time.Parse(time.DateOnly, local.date)
		if err != nil {
			return fmt.Errorf("--date %q is not a date of the form YYYY-MM-DD: %w", local.date, err)
		}
	}
	body, err := noteBody(command, options, local, kind)
	if err != nil {
		return err
	}
	note := notes.Note{
		Kind: kind, Title: strings.TrimSpace(local.title), Date: date,
		Subject: strings.TrimSpace(local.subject), Status: strings.TrimSpace(local.status), Body: body,
	}
	// Render before writing: the note goes through the same reader an hour
	// later, and a file that cannot be read back is not a record.
	rendered := note.Render()
	if _, err := notes.Parse("the note being written", strings.NewReader(rendered)); err != nil {
		return err
	}

	relative, err := note.FileName()
	if err != nil {
		return err
	}
	root, err := local.resolvedRoot()
	if err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	if _, err := os.Stat(full); err == nil {
		// Two notes on one subject on one day is ordinary. Overwriting the
		// first with the second is how a record quietly loses half of itself.
		return fmt.Errorf("%s already exists; give the note a different --title or --date", relative)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, []byte(rendered), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(options.stdout, relative)
	return nil
}

func noteBody(command *cobra.Command, options *commandOptions, local *notesOptions, kind notes.Kind) (string, error) {
	if local.body == "" {
		return scaffold(kind), nil
	}
	var reader io.Reader
	if local.body == "-" {
		reader = command.InOrStdin()
		if reader == nil {
			reader = options.stdin
		}
	} else {
		file, err := os.Open(local.body)
		if err != nil {
			return "", err
		}
		defer file.Close()
		reader = file
	}
	read, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(read)) == "" {
		return "", fmt.Errorf("the body read from %s is empty", local.body)
	}
	return string(read), nil
}

// scaffold is the shape each kind is expected to have. It is headings and
// nothing else: prose invented here would be indistinguishable, later, from
// prose someone meant.
func scaffold(kind notes.Kind) string {
	switch kind {
	case notes.KindPlan:
		return "## What this is for\n\n## What it changes\n\n## How it is checked\n"
	case notes.KindResearch:
		return "## The question\n\n## What was found\n\n## What is still unknown\n"
	default:
		return "## What happened\n\n## What changed\n\n## What is left\n"
	}
}

func newNotesListCommand(options *commandOptions) *cobra.Command {
	local := &notesOptions{}
	command := &cobra.Command{
		Use:   "list",
		Short: "List the notes in the repository, newest first",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			collected, err := readNotes(local)
			if err != nil {
				return err
			}
			if local.kind != "" {
				kind, err := notes.ParseKind(local.kind)
				if err != nil {
					return err
				}
				kept := collected[:0]
				for _, note := range collected {
					if note.Kind == kind {
						kept = append(kept, note)
					}
				}
				collected = kept
			}
			if len(collected) == 0 {
				fmt.Fprintln(options.stderr, "machinist: no notes")
				return nil
			}
			writer := tabwriter.NewWriter(options.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(writer, "DATE\tKIND\tSTATUS\tSUBJECT\tTITLE")
			for _, note := range collected {
				status := note.Status
				if status == "" {
					status = "-"
				}
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
					note.Date.Format(time.DateOnly), string(note.Kind), status, note.Subject, note.Title)
			}
			return writer.Flush()
		},
	}
	command.Flags().StringVar(&local.kind, "kind", "", "show only one of "+strings.Join(notes.KindNames(), ", "))
	local.addRootFlag(command)
	return command
}

func newNotesCheckCommand(options *commandOptions) *cobra.Command {
	local := &notesOptions{}
	command := &cobra.Command{
		Use:   "check",
		Short: "Refuse a notes tree that cannot be read back",
		Args:  cobra.NoArgs,
		// This is the verb a hook or a CI job runs. It answers about the whole
		// tree, so it reports every note it cannot read rather than the first:
		// fixing them one CI run at a time is how a check stops being run.
		RunE: func(_ *cobra.Command, _ []string) error {
			root, err := local.resolvedRoot()
			if err != nil {
				return err
			}
			problems, count, err := checkNotes(os.DirFS(root))
			if err != nil {
				return err
			}
			if len(problems) > 0 {
				sort.Strings(problems)
				for _, problem := range problems {
					fmt.Fprintln(options.stderr, "machinist: "+problem)
				}
				return fmt.Errorf("%d of %d notes cannot be read back", len(problems), count+len(problems))
			}
			fmt.Fprintf(options.stdout, "%d notes read back\n", count)
			return nil
		},
	}
	local.addRootFlag(command)
	return command
}

// checkNotes reads every note file itself rather than calling notes.ReadAll,
// which stops at the first failure. Both behaviours are wanted, by different
// callers: a reader must not act on a partial answer, and a check must show
// the operator the whole list.
func checkNotes(system fs.FS) ([]string, int, error) {
	if _, err := fs.Stat(system, notes.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, fmt.Errorf("there is no %s directory here", notes.Root)
		}
		return nil, 0, err
	}
	var problems []string
	read := 0
	err := fs.WalkDir(system, notes.Root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || filepath.Base(name) == "README.md" {
			return nil
		}
		file, err := system.Open(name)
		if err != nil {
			problems = append(problems, err.Error())
			return nil
		}
		defer file.Close()
		note, err := notes.Parse(name, file)
		if err != nil {
			problems = append(problems, err.Error())
			return nil
		}
		if want := notes.Root + "/" + mustDirectory(note.Kind); !strings.HasPrefix(name, want+"/") {
			problems = append(problems, fmt.Sprintf("%s: a %s belongs in %s", name, string(note.Kind), want))
			return nil
		}
		read++
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return problems, read, nil
}

// mustDirectory is safe only because Parse has already refused every kind that
// has no directory.
func mustDirectory(kind notes.Kind) string {
	directory, err := kind.Directory()
	if err != nil {
		panic(err)
	}
	return directory
}

func readNotes(local *notesOptions) ([]notes.Note, error) {
	root, err := local.resolvedRoot()
	if err != nil {
		return nil, err
	}
	collected, err := notes.ReadAll(os.DirFS(root))
	if errors.Is(err, notes.ErrNoNotes) {
		return nil, nil
	}
	return collected, err
}
