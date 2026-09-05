package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// A configuration example a reader can paste is the only kind worth printing.
// Issue #74 was filed because docs/observability.md named a collector that no
// longer existed; the fix was a rewrite, and a rewrite is exactly the thing that
// goes stale again the next time a key is renamed. So every documented example
// is read back by the loader that reads a real config, with unknown keys
// refused, and a key the struct does not have fails here rather than in
// someone's terminal.
//
// This checks the shape of an example, not its values: a documented ssh_host is
// a placeholder and nothing here tries to reach it.

var tomlBlock = regexp.MustCompile("(?s)```toml\n(.*?)```")

// tableOwner maps a top-level TOML key to the file it belongs in.
var tableOwner = map[string]string{
	"server": "config", "collector": "config", "observability": "config",
	"commands": "config", "routes": "config", "github": "config", "triggers": "config",
	"name": "worker", "fleet": "worker", "data_directory": "worker",
	"control_plane": "worker", "environment": "worker", "telemetry": "worker",
	"executors": "worker", "profiles": "worker", "repositories": "worker",
}

// fileMarker is the convention the documentation already uses when one fenced
// block shows two files: a comment naming the file the lines below belong to.
// Honouring it lets the check be exact instead of approximate.
var fileMarker = regexp.MustCompile(`(?m)^#\s*(config|worker)\.toml\s*$`)

// The parts an example is entitled to leave out. A fragment showing one key is
// documenting that key, not asserting that a config needs nothing else.
const configPreamble = `[server]
listen = "127.0.0.1:7331"
database = "~/.machinist/server/machinist.db"
worker_token_file = "~/.machinist/server/worker.token"
`

const workerPreamble = `name = "docs-example"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.machinist/server/worker.token"
`

func TestEveryDocumentedConfigurationExampleLoads(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil {
		t.Fatalf("list documentation: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no documentation found: a check that reads nothing passes")
	}

	examples := 0
	for _, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Line endings are the checkout's, not the document's. On Windows git
		// hands these files back with CRLF, and a fence pattern anchored to \n
		// then matches nothing -- which is the one way this check can pass by
		// reading no examples at all.
		text := strings.ReplaceAll(string(source), "\r\n", "\n")
		for index, match := range tomlBlock.FindAllStringSubmatch(text, -1) {
			for _, fragment := range splitByFile(match[1]) {
				examples++
				owner, err := ownerOf(fragment)
				if err != nil {
					t.Errorf("%s block %d: %v", filepath.Base(path), index+1, err)
					continue
				}
				if err := loadFragment(t, owner, fragment); err != nil {
					t.Errorf("%s block %d does not load as %s.toml: %v",
						filepath.Base(path), index+1, owner, err)
				}
			}
		}
	}
	if examples == 0 {
		t.Fatal("no toml examples matched: the pattern stopped matching, and a check that reads nothing passes")
	}
	t.Logf("%d documented examples loaded", examples)
}

// splitByFile divides one fenced block at its `# config.toml` / `# worker.toml`
// markers. A block without any marker is one fragment.
func splitByFile(body string) []string {
	positions := fileMarker.FindAllStringIndex(body, -1)
	if len(positions) < 2 {
		return []string{body}
	}
	var fragments []string
	for i, at := range positions {
		end := len(body)
		if i+1 < len(positions) {
			end = positions[i+1][0]
		}
		fragments = append(fragments, body[at[0]:end])
	}
	return fragments
}

// ownerOf decides which configuration a fragment belongs to from the top-level
// keys it declares. A fragment whose keys belong to both, or to neither, is an
// error rather than a guess: guessing would let a misspelled table pass as the
// other file's problem.
func ownerOf(body string) (string, error) {
	var document map[string]any
	if err := toml.Unmarshal([]byte(body), &document); err != nil {
		return "", errUnparsable{err}
	}
	owners := map[string]bool{}
	var keys []string
	for key := range document {
		keys = append(keys, key)
		owner, known := tableOwner[key]
		if !known {
			return "", errNoOwner(key)
		}
		owners[owner] = true
	}
	switch len(owners) {
	case 0:
		return "", errNoKeys{}
	case 1:
		for owner := range owners {
			return owner, nil
		}
	}
	return "", errTwoOwners{keys}
}

// loadFragment merges the fragment over the parts an example may omit and reads
// the result with the real loader. Merging rather than splicing text means a
// fragment that names one key inside a table the preamble also has keeps both.
func loadFragment(t *testing.T, owner, body string) error {
	t.Helper()
	name, preamble := "config.toml", configPreamble
	if owner == "worker" {
		name, preamble = "worker.toml", workerPreamble
	}

	var base, example map[string]any
	if err := toml.Unmarshal([]byte(preamble), &base); err != nil {
		return err
	}
	if err := toml.Unmarshal([]byte(body), &example); err != nil {
		return errUnparsable{err}
	}
	merge(base, example)

	document, err := toml.Marshal(base)
	if err != nil {
		return err
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, document, 0o600); err != nil {
		return err
	}
	if owner == "worker" {
		_, err = LoadWorker(path)
		return err
	}
	_, err = LoadConfig(path)
	return err
}

// merge writes the example over the base, table by table, so that supplying
// [server] listen does not delete the [server] keys the example left out.
func merge(base, example map[string]any) {
	for key, value := range example {
		nested, isTable := value.(map[string]any)
		existing, wasTable := base[key].(map[string]any)
		if isTable && wasTable {
			merge(existing, nested)
			continue
		}
		base[key] = value
	}
}

type errNoKeys struct{}

func (errNoKeys) Error() string {
	return "declares nothing, so there is nothing to say which configuration it is an example of"
}

type errTwoOwners struct{ keys []string }

func (e errTwoOwners) Error() string {
	return "mixes control-plane and worker keys (" + strings.Join(e.keys, ", ") +
		"); a reader cannot paste it into either file, and one block showing two " +
		"files should mark them with `# config.toml` and `# worker.toml`"
}

type errNoOwner string

func (e errNoOwner) Error() string {
	return "declares [" + string(e) + "], which is not a key either configuration has"
}

type errUnparsable struct{ err error }

func (e errUnparsable) Error() string { return "is not valid TOML: " + e.err.Error() }
