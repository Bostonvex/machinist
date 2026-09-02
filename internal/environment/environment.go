// Package environment discovers the bounded, non-secret platform facts a
// worker may advertise to the control plane.
package environment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	maxFieldLength = 64
	maxListItems   = 32
)

// Manifest contains only facts safe to persist in the control plane. It must
// never contain environment values, filesystem paths, credentials, or prompts.
type Manifest struct {
	OS        string   `json:"os,omitempty"`
	Arch      string   `json:"arch,omitempty"`
	Execution string   `json:"execution,omitempty"`
	Shell     string   `json:"shell,omitempty"`
	PathStyle string   `json:"path_style,omitempty"`
	Features  []string `json:"features,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Digest    string   `json:"digest,omitempty"`
}

// Detect returns the current platform facts and trusted operator-supplied
// tags. Tags are configuration, not inferred authority.
func Detect(tags []string) Manifest {
	manifest := detect(detector{
		goos: runtime.GOOS, goarch: runtime.GOARCH,
		lookupEnv: os.LookupEnv, readFile: os.ReadFile,
		stat: os.Stat,
	}, tags)
	manifest.Digest = manifest.digest()
	return manifest
}

// Validate rejects malformed or unexpectedly large manifests before they are
// persisted. An empty manifest is valid for older workers.
func (m Manifest) Validate() error {
	if m.OS == "" && m.Arch == "" && m.Execution == "" && m.Shell == "" && m.PathStyle == "" && len(m.Features) == 0 && len(m.Tags) == 0 && m.Digest == "" {
		return nil
	}
	for name, value := range map[string]string{
		"os": m.OS, "arch": m.Arch, "execution": m.Execution,
		"shell": m.Shell, "path_style": m.PathStyle,
	} {
		if value == "" || len(value) > maxFieldLength || !safeIdentifier(value) {
			return fmt.Errorf("worker environment %s is invalid", name)
		}
	}
	if err := validateList("features", m.Features); err != nil {
		return err
	}
	if err := validateList("tags", m.Tags); err != nil {
		return err
	}
	if len(m.Digest) != sha256.Size*2 {
		return errors.New("worker environment digest is invalid")
	}
	if _, err := hex.DecodeString(m.Digest); err != nil || m.Digest != m.digest() {
		return errors.New("worker environment digest does not match its facts")
	}
	return nil
}

type detector struct {
	goos      string
	goarch    string
	lookupEnv func(string) (string, bool)
	readFile  func(string) ([]byte, error)
	stat      func(string) (os.FileInfo, error)
}

func detect(d detector, tags []string) Manifest {
	execution := "native"
	if isWSL(d) {
		execution = "wsl"
	} else if isContainer(d) {
		execution = "container"
	}
	pathStyle := "posix"
	features := []string{"argument-array", "process-tree-cancel"}
	if d.goos == "windows" {
		pathStyle = "windows"
		features = append(features, "job-objects")
	} else {
		features = append(features, "process-groups", "unix-signals")
	}
	manifest := Manifest{
		OS: d.goos, Arch: d.goarch, Execution: execution,
		Shell: detectShell(d), PathStyle: pathStyle,
		Features: normaliseList(features), Tags: normaliseList(tags),
	}
	return manifest
}

func isWSL(d detector) bool {
	if d.goos != "linux" {
		return false
	}
	if _, ok := d.lookupEnv("WSL_INTEROP"); ok {
		return true
	}
	if _, ok := d.lookupEnv("WSL_DISTRO_NAME"); ok {
		return true
	}
	body, err := d.readFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(body)), "microsoft")
}

func isContainer(d detector) bool {
	if _, ok := d.lookupEnv("container"); ok {
		return true
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := d.stat(marker); err == nil {
			return true
		}
	}
	return false
}

func detectShell(d detector) string {
	if d.goos == "windows" {
		if value, ok := d.lookupEnv("COMSPEC"); ok {
			if shell := shellName(value); shell != "" {
				return shell
			}
		}
		return "powershell"
	}
	if value, ok := d.lookupEnv("SHELL"); ok {
		if shell := shellName(value); shell != "" {
			return shell
		}
	}
	return "sh"
}

func shellName(value string) string {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(value, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	if safeIdentifier(base) && len(base) <= maxFieldLength {
		return base
	}
	return ""
}

func validateList(name string, values []string) error {
	if len(values) > maxListItems {
		return fmt.Errorf("worker environment %s exceeds %d items", name, maxListItems)
	}
	if !slices.IsSorted(values) {
		return fmt.Errorf("worker environment %s must be sorted", name)
	}
	for index, value := range values {
		if value == "" || len(value) > maxFieldLength || !safeIdentifier(value) {
			return fmt.Errorf("worker environment %s item %d is invalid", name, index)
		}
		if index > 0 && values[index-1] == value {
			return fmt.Errorf("worker environment %s contains duplicates", name)
		}
	}
	return nil
}

func normaliseList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}

func safeIdentifier(value string) bool {
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (m Manifest) digest() string {
	m.Digest = ""
	body, _ := json.Marshal(m)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
