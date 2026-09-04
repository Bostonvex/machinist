package harness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/environment"
)

func TestInspectFiltersProfilesWithoutExposingSecretNames(t *testing.T) {
	worker := config.Worker{
		Executors: map[string]config.Executor{"legacy": {Command: []string{"legacy"}}},
		Profiles: map[string]config.Profile{
			"local": {
				Harness: "opencode", Provider: "openai_compatible", AuthMode: "local",
				Command: []string{"opencode", "run", "--model={{machinist.model}}"},
				Models:  map[string]string{"coder": "local/coder"}, RequiresTags: []string{"dgx-spark"},
			},
			"deepseek": {
				Harness: "pi", Provider: "deepseek", AuthMode: "api_key", SecretEnv: "DEEPSEEK_API_KEY",
				Command: []string{"pi", "--model={{machinist.model}}"}, Models: map[string]string{"reasoner": "deepseek-reasoner"},
			},
		},
	}
	manifest := environment.Detect([]string{"dgx-spark"})
	report := inspect(worker, manifest,
		func(name string) (string, error) {
			if name == "opencode" {
				return "/usr/bin/opencode", nil
			}
			return "", errors.New("not found")
		},
		func(string) (string, bool) { return "", false },
		func(string) bool { return true },
	)
	if len(report.Executors) != 2 || report.Executors[0] != "legacy" || report.Executors[1] != "local" {
		t.Fatalf("executors = %#v", report.Executors)
	}
	if !report.Profiles["local"].Available || report.Profiles["deepseek"].Available || report.Profiles["deepseek"].Reason != "credential unavailable" {
		t.Fatalf("profiles = %#v", report.Profiles)
	}
	if strings.Contains(fmt.Sprintf("%#v", report), "DEEPSEEK_API_KEY") {
		t.Fatal("secret environment name leaked into models")
	}
}

func TestInspectChecksPlatformAndExecutable(t *testing.T) {
	worker := config.Worker{Profiles: map[string]config.Profile{
		"other-os": {Harness: "generic", AuthMode: "local", Command: []string{"agent"}, RequiresOS: []string{"not-a-real-os"}},
		"missing":  {Harness: "generic", AuthMode: "local", Command: []string{"missing"}},
	}}
	// The requirement must name an OS no host reports: against a detected
	// manifest, requiring the host's own OS passes the platform check and
	// inspection reports the executable reason instead.
	manifest := environment.Detect(nil)
	report := inspect(worker, manifest, func(string) (string, error) { return "", errors.New("not found") }, func(string) (string, bool) { return "", false }, func(string) bool { return true })
	if report.Profiles["other-os"].Reason != "operating system requirement not met" {
		t.Fatalf("other-os profile = %#v", report.Profiles["other-os"])
	}
	if report.Profiles["missing"].Reason != "harness executable unavailable" {
		t.Fatalf("missing profile = %#v", report.Profiles["missing"])
	}
}

func TestInspectDoesNotAdvertiseUnavailableLocalEndpoint(t *testing.T) {
	worker := config.Worker{Profiles: map[string]config.Profile{
		"local": {
			Harness: "codex", Provider: "openai_compatible", AuthMode: "local",
			BaseURL: "http://127.0.0.1:18000/v1", Command: []string{"codex"},
		},
	}}
	report := inspect(worker, environment.Detect(nil),
		func(string) (string, error) { return "/usr/bin/codex", nil },
		func(string) (string, bool) { return "", false },
		func(rawURL string) bool { return rawURL != "http://127.0.0.1:18000/v1" },
	)
	if report.Profiles["local"].Available || report.Profiles["local"].Reason != "local model endpoint unavailable" || len(report.Executors) != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestInspectChecksWrapperAndRequiredExecutables(t *testing.T) {
	worker := config.Worker{Profiles: map[string]config.Profile{
		"deepcode": {
			Harness: "deepcode", AuthMode: "local", Command: []string{"/opt/machinist/run-deepcode.sh"},
			RequiresExecutables: []string{"deepcode", "node"},
		},
	}}
	report := inspect(worker, environment.Detect(nil),
		func(name string) (string, error) {
			if name == "node" || name == "/opt/machinist/run-deepcode.sh" {
				return name, nil
			}
			return "", errors.New("not found")
		},
		func(string) (string, bool) { return "", false },
		func(string) bool { return true },
	)
	if report.Profiles["deepcode"].Available || report.Profiles["deepcode"].Reason != "harness executable unavailable" {
		t.Fatalf("report = %#v", report)
	}
}
