// Package harness inspects typed execution profiles without exposing worker
// secrets or machine-local paths to the control plane.
package harness

import (
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/environment"
)

type Capability struct {
	Harness   string
	Provider  string
	AuthMode  string
	Models    []string
	Available bool
	Reason    string
}

type Report struct {
	Executors []string
	Models    map[string][]string
	Profiles  map[string]Capability
}

// Inspect returns schedulable executor/profile names and a redacted readiness
// report. Credential names, values, executable paths, and endpoint URLs are not
// included in the report.
func Inspect(worker config.Worker, manifest environment.Manifest) Report {
	return inspect(worker, manifest, exec.LookPath, os.LookupEnv)
}

func inspect(worker config.Worker, manifest environment.Manifest, lookPath func(string) (string, error), lookupEnv func(string) (string, bool)) Report {
	report := Report{
		Executors: make([]string, 0, len(worker.Executors)+len(worker.Profiles)),
		Models:    make(map[string][]string),
		Profiles:  make(map[string]Capability, len(worker.Profiles)),
	}
	for name, executor := range worker.Executors {
		report.Executors = append(report.Executors, name)
		if models := sortedKeys(executor.Models); len(models) > 0 {
			report.Models[name] = models
		}
	}
	for name, profile := range worker.Profiles {
		capability := Capability{
			Harness: profile.Harness, Provider: profile.Provider,
			AuthMode: profile.AuthMode, Models: sortedKeys(profile.Models), Available: true,
		}
		switch {
		case !matches(profile.RequiresOS, manifest.OS):
			capability.Available, capability.Reason = false, "operating system requirement not met"
		case !matches(profile.RequiresArch, manifest.Arch):
			capability.Available, capability.Reason = false, "architecture requirement not met"
		case !containsAll(manifest.Tags, profile.RequiresTags):
			capability.Available, capability.Reason = false, "operator tag requirement not met"
		case profile.AuthMode == "api_key" && !presentEnvironment(profile.SecretEnv, lookupEnv):
			capability.Available, capability.Reason = false, "credential unavailable"
		case needsPathLookup(profile.Command[0]):
			if _, err := lookPath(profile.Command[0]); err != nil {
				capability.Available, capability.Reason = false, "harness executable unavailable"
			}
		}
		report.Profiles[name] = capability
		if capability.Available {
			report.Executors = append(report.Executors, name)
			if len(capability.Models) > 0 {
				report.Models[name] = capability.Models
			}
		}
	}
	slices.Sort(report.Executors)
	return report
}

func matches(requirements []string, actual string) bool {
	return len(requirements) == 0 || slices.Contains(requirements, strings.ToLower(actual))
}

func containsAll(actual, required []string) bool {
	values := make(map[string]bool, len(actual))
	for _, value := range actual {
		values[value] = true
	}
	for _, value := range required {
		if !values[value] {
			return false
		}
	}
	return true
}

func presentEnvironment(name string, lookupEnv func(string) (string, bool)) bool {
	value, ok := lookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}

func needsPathLookup(executable string) bool {
	return !strings.ContainsAny(executable, `/\`)
}

func sortedKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}
