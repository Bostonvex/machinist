package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/spf13/cobra"
)

var initialFiles = []string{
	"config.toml",
	"worker.toml",
	"prompts/foreman.md",
	"prompts/audit.md",
	"prompts/shepherd.md",
	// The Foreman delegates planning, building, and review to fresh runs of
	// these three. Shipping the prompt that names them without the prompts
	// themselves leaves an installation that blocks the first time it tries to
	// delegate, on a file it was never given.
	"prompts/delegate-plan.md",
	"prompts/delegate-build.md",
	"prompts/reviewer.md",
}

// installOutcome is what happened to one file, so the caller can say it. What
// is worth saying differs by file: a prompt that was kept and differs from the
// shipped one is drift worth naming, and the worker token has no shipped copy
// to differ from.
type installOutcome int

const (
	installCreated installOutcome = iota
	installKept
	installKeptDifferent
)

func newInitCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the default Machinist configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return initializeMachinist(options.stdout)
		},
	}
}

func initializeMachinist(output io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find user home directory: %w", err)
	}
	directory := filepath.Join(home, ".machinist")
	for _, path := range []string{directory, filepath.Join(directory, "prompts"), filepath.Join(directory, "server")} {
		if err := ensureDirectory(path); err != nil {
			return err
		}
	}

	for _, name := range initialFiles {
		body, err := machinistexamples.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read default %s: %w", name, err)
		}
		outcome, err := installFile(directory, name, body)
		if err != nil {
			return err
		}
		switch outcome {
		case installCreated:
			fmt.Fprintf(output, "created %s\n", name)
		case installKeptDifferent:
			// Keeping it is right: an operator's edits to their own prompt are
			// theirs, and init is not the place to take them away. Keeping it
			// silently is what is wrong. A prompt edited in place stays edited
			// forever with nothing anywhere recording that the copy Machinist
			// runs is no longer the copy Machinist tests, and a rule that only
			// exists in the running copy is a rule no test can hold.
			fmt.Fprintf(output, "kept %s (differs from the shipped copy)\n", name)
		default:
			fmt.Fprintf(output, "kept %s\n", name)
		}
	}

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generate worker token: %w", err)
	}
	// A token that is already there is kept, and no difference is reported: the
	// body offered here was generated a line ago, so every existing token
	// differs from it and none of them has drifted from anything.
	tokenOutcome, err := installFile(directory, "server/worker.token", []byte(hex.EncodeToString(token)+"\n"))
	if err != nil {
		return err
	}
	if tokenOutcome == installCreated {
		fmt.Fprintln(output, "created server/worker.token")
	} else {
		fmt.Fprintln(output, "kept server/worker.token")
	}

	fmt.Fprintf(output, "Machinist configuration is ready in %s\n", directory)
	fmt.Fprintln(output, "Add repositories to worker.toml before starting a managed worker.")
	return nil
}

// installFile writes one file if it is absent and never overwrites one that is
// present. It reports what it did rather than saying it, because what is worth
// saying about a kept file depends on whether there is a shipped copy for it to
// have drifted from.
func installFile(root, name string, body []byte) (installOutcome, error) {
	path := filepath.Join(root, filepath.FromSlash(name))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return 0, fmt.Errorf("inspect existing %s: %w", name, statErr)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("%s already exists and is not a regular file", name)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			// Unreadable is not identical. Reporting it as kept-and-same would
			// claim a comparison that never happened.
			return 0, fmt.Errorf("read existing %s: %w", name, readErr)
		}
		if !bytes.Equal(existing, body) {
			return installKeptDifferent, nil
		}
		return installKept, nil
	}
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", name, err)
	}
	written, writeErr := file.Write(body)
	if writeErr == nil && written != len(body) {
		writeErr = io.ErrShortWrite
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return 0, fmt.Errorf("write %s: %w", name, writeErr)
	}
	return installCreated, nil
}

func ensureDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return fmt.Errorf("inspect existing directory %s: %w", path, statErr)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s already exists and is not a directory", path)
	}
	return nil
}
