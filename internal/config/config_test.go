package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestExecutorRendersSeparateHerdrAdapter(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{
		DataDirectory: t.TempDir(),
		Executors: map[string]Executor{"codex": {
			Command:    []string{"codex", "exec", "--model={{machinist.model}}", "-"},
			Models:     map[string]string{"fast": "gpt-test"},
			HerdrAgent: "codex", HerdrArgs: []string{"--model={{machinist.model}}", "--sandbox", "workspace-write"},
		}},
	}, func() (string, error) { return "test-host", nil })
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Name: "implement", Executor: "codex"}, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.HerdrAgent != "codex" || strings.Join(resolved.HerdrArgs, " ") != "--model=gpt-test --sandbox workspace-write" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestExecutorRendersReportedHerdrCommand(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{
		DataDirectory: t.TempDir(),
		Executors: map[string]Executor{"dsh": {
			Command:      []string{"dsh-headless", "--model={{machinist.model}}"},
			Models:       map[string]string{"local": "ds-0731"},
			HerdrCommand: []string{"dsh-tui", "--model={{machinist.model}}"},
		}},
	}, func() (string, error) { return "test-host", nil })
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Name: "implement", Executor: "dsh"}, "local")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.HerdrAgent != "" || strings.Join(resolved.HerdrCommand, " ") != "dsh-tui --model=ds-0731" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestLoadWorkerResolvesRelativePathsFromConfig(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if worker.DataDirectory != filepath.Join(directory, "state") {
		t.Fatalf("data directory = %q", worker.DataDirectory)
	}
	definition, err := worker.ResolveMachinistConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if definition != filepath.Join(directory, "config.toml") {
		t.Fatalf("definition = %q", definition)
	}
}

func TestLoadWorkerDefaultsNameToHostname(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "data_directory = \"state\"\n")

	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != hostname {
		t.Fatalf("worker name = %q, want hostname %q", worker.Name, hostname)
	}
}

func TestWorkerNameDefaultsReportHostnameFailure(t *testing.T) {
	want := errors.New("hostname unavailable")
	_, err := applyWorkerDefaultsWithHostname(Worker{DataDirectory: t.TempDir()}, func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "find machine hostname") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerExplicitNameDoesNotReadHostname(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{Name: " configured-worker ", DataDirectory: t.TempDir()}, func() (string, error) {
		t.Fatal("hostname lookup called for explicit worker name")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != "configured-worker" {
		t.Fatalf("worker name = %q", worker.Name)
	}
}

func TestLoadWorkerRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, "mystery = true\n")

	_, err := LoadWorker(path)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadManagedWorkerResolvesMachineConfiguration(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret\n")
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, `name = "local"
data_directory = "state"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "token"

[executors.test]
command = ["agent", "run"]

[repositories.machinist]
path = "repository"
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if repository, err := worker.ResolveRepository("machinist"); err != nil || repository != filepath.Join(directory, "repository") {
		t.Fatalf("repository = %q, %v", repository, err)
	}
	if token, err := worker.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "test"}, "")
	if err != nil || len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("agent = %#v, %v", resolved, err)
	}
}

func TestLoadWorkerProfilesAreTypedAndBackwardCompatible(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, `name = "local"
data_directory = "state"

[environment]
detect = true
tags = [" Trusted ", "dgx-spark", "trusted"]

[executors.legacy]
command = ["legacy"]

[profiles.deepseek]
harness = "pi"
provider = "deepseek"
auth_mode = "api_key"
secret_env = "DEEPSEEK_API_KEY"
command = ["pi", "--model={{machinist.model}}"]
models = { reasoner = "deepseek-reasoner" }

[profiles.local]
harness = "opencode"
provider = "openai_compatible"
auth_mode = "local"
base_url = "http://127.0.0.1:8000/v1"
base_url_env = "OPENAI_BASE_URL"
command = ["opencode", "run", "--model={{machinist.model}}"]
models = { coder = "local/coder" }
requires_os = ["linux", "darwin"]
requires_tags = ["dgx-spark"]
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Environment.Tags) != 2 || worker.Environment.Tags[0] != "dgx-spark" || worker.Environment.Tags[1] != "trusted" {
		t.Fatalf("tags = %#v", worker.Environment.Tags)
	}
	if got := worker.ExecutorNames(); strings.Join(got, ",") != "deepseek,legacy,local" {
		t.Fatalf("execution names = %#v", got)
	}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "local"}, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Profile != "local" || resolved.Harness != "opencode" || resolved.Provider != "openai_compatible" || resolved.AuthMode != "local" || resolved.Model != "local/coder" || resolved.Environment["OPENAI_BASE_URL"] != "http://127.0.0.1:8000/v1" {
		t.Fatalf("resolved profile = %#v", resolved)
	}
}

func TestLoadWorkerAcceptsCustomHarnessIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, `[profiles.deepseek-cli]
harness="deepseek"
provider="deepseek"
auth_mode="api_key"
secret_env="DEEPSEEK_API_KEY"
command=["deepseek-agent", "--model={{machinist.model}}"]
models={ coder="deepseek-coder" }
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := worker.Profiles["deepseek-cli"]
	if profile.Harness != "deepseek" || profile.Provider != "deepseek" || profile.AuthMode != "api_key" {
		t.Fatalf("custom profile = %#v", profile)
	}
}

func TestLoadWorkerBuildsProviderNeutralTelemetryAliases(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.toml")
	writeTestFile(t, path, `name="telemetry-worker"
data_directory="state"
[telemetry]
enabled=true
url="http://127.0.0.1:7900/api/v1/events"
token_file="collector.token"
identity_salt_file="identity.salt"
endpoint_id="DGX-Primary"
`)
	worker, err := LoadWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	values := worker.TelemetryEnvironment()
	if worker.Telemetry.EndpointID != "dgx-primary" || values["MACHINIST_TELEMETRY_URL"] != values["BUZZ_TELEMETRY_URL"] || values["MACHINIST_TELEMETRY_TOKEN_FILE"] != filepath.Join(directory, "collector.token") || values["BUZZ_TELEMETRY_IDENTITY_SALT_FILE"] != filepath.Join(directory, "identity.salt") {
		t.Fatalf("telemetry = %#v, environment = %#v", worker.Telemetry, values)
	}
	for name, value := range values {
		if strings.Contains(name, "TOKEN") && strings.Contains(value, "secret-value") {
			t.Fatalf("telemetry environment contains token value: %s", name)
		}
	}
}

func TestLoadWorkerRejectsNonLoopbackTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.toml")
	writeTestFile(t, path, `[telemetry]
enabled=true
url="https://collector.example/api/v1/events"
token_file="collector.token"
identity_salt_file="identity.salt"
endpoint_id="remote"
`)
	if _, err := LoadWorker(path); err == nil || !strings.Contains(err.Error(), "literal loopback") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadCommandAcceptsProfileAndRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[commands.implement]\nprofile=\"local\"\nrole=\"Implementer\"\n")
	command, err := LoadCommand(path, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if command.Executor != "local" || command.Profile != "local" || command.Role != "implementer" {
		t.Fatalf("command = %#v", command)
	}
}

func TestLoadCommandResolvesOrderedRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, `[routes.implementation]
profiles=["local", "codex-subscription", "deepseek"]
max_attempts=3
max_total_tokens=100000
fallback_on=["capacity", "rate_limit", "transient"]

[commands.implement]
route="implementation"
role="implementer"
`)
	command, err := LoadCommand(path, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if command.Route != "implementation" || command.Executor != "" || command.MaxAttempts != 3 || command.MaxTotalTokens != 100000 || strings.Join(command.Candidates, ",") != "local,codex-subscription,deepseek" {
		t.Fatalf("command = %#v", command)
	}
	worker := Worker{Profiles: map[string]Profile{"codex-subscription": {}}}
	command, err = worker.ResolveRoute(command, []string{"deepseek", "codex-subscription"})
	if err != nil || command.Executor != "codex-subscription" || command.Profile != "codex-subscription" {
		t.Fatalf("resolved route = %#v, %v", command, err)
	}
}

func TestLoadConfigRejectsInvalidRoutes(t *testing.T) {
	for name, test := range map[string]struct{ route, want string }{
		"no candidates":         {"[routes.test]\nprofiles=[]\n", "between 1 and 8"},
		"duplicates":            {"[routes.test]\nprofiles=[\"local\",\"local\"]\n", "duplicated"},
		"bad fallback":          {"[routes.test]\nprofiles=[\"local\"]\nfallback_on=[\"guess\"]\n", "unsupported"},
		"negative token budget": {"[routes.test]\nprofiles=[\"local\"]\nmax_total_tokens=-1\n", "max_total_tokens"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeTestFile(t, path, test.route+"[commands.test]\nroute=\"test\"\n")
			if _, err := LoadCommand(path, "test"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadWorkerRejectsUnsafeProfiles(t *testing.T) {
	for name, test := range map[string]struct{ body, want string }{
		"invalid harness identifier": {`[profiles.test]
harness="not a harness!"
auth_mode="local"
command=["agent"]
`, "harness"},
		"api key missing reference": {`[profiles.test]
harness="pi"
provider="deepseek"
auth_mode="api_key"
command=["pi"]
`, "secret_env"},
		"remote cleartext endpoint": {`[profiles.test]
harness="opencode"
auth_mode="local"
base_url="http://10.0.0.5:8000/v1"
base_url_env="OPENAI_BASE_URL"
command=["opencode"]
`, "allow_insecure_http"},
		"executor collision": {`[executors.test]
command=["legacy"]
[profiles.test]
harness="generic"
auth_mode="local"
command=["agent"]
`, "both an executor and a profile"},
		"invalid required executable": {`[profiles.test]
harness="deepcode"
auth_mode="local"
command=["agent"]
requires_executables=["deepcode\nbad"]
`, "requires_executables"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.toml")
			writeTestFile(t, path, test.body)
			if _, err := LoadWorker(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigCombinesServerAndCommands(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "token"), "secret")
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan {{machinist.prompt}}.\n")
	writeTestFile(t, path, `[server]
database = "state/machinist.db"
worker_token_file = "token"
max_concurrent_jobs = 2

[commands.plan]
executor = "test"
prompt_file = "plan.md"

`)
	machinistConfig, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := machinistConfig.Server
	if server.Listen != "127.0.0.1:7331" || server.Database != filepath.Join(directory, "state", "machinist.db") || server.ConcurrentJobLimit() != 2 {
		t.Fatalf("server = %#v", server)
	}
	if machinistConfig.Path() != path || len(machinistConfig.Commands) != 1 {
		t.Fatalf("Machinist config = %#v, path = %q", machinistConfig, machinistConfig.Path())
	}
	if token, err := server.WorkerToken(); err != nil || token != "secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestLoadConfigRejectsRemovedConfigurationWithMigrationGuidance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[pipelines.quality]\nagents=[\"review\"]\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "replace each pipeline with a repository-owned orchestration script") {
		t.Fatalf("pipeline migration error = %v", err)
	}
	writeTestFile(t, path, "[agents.review]\nexecutor=\"codex\"\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "agents were renamed to commands") {
		t.Fatalf("agent migration error = %v", err)
	}
}

func TestCommandWithoutPromptTemplatePassesInputThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "[commands.script]\nexecutor=\"script\"\n")
	command, err := LoadCommand(path, "script")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderPrompt(command, "raw task")
	if err != nil || rendered.Prompt != "raw task" {
		t.Fatalf("rendered script command = %#v, %v", rendered, err)
	}
}

func TestLoadConfigValidatesOptionalConcurrentJobLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  int
	}{
		{name: "omitted is unlimited", want: 0},
		{name: "positive limit", value: "max_concurrent_jobs = 1\n", want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "token"), "secret")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, "[server]\nworker_token_file = \"token\"\n"+test.value)
			machinistConfig, err := LoadConfig(path)
			if err != nil || machinistConfig.Server.ConcurrentJobLimit() != test.want {
				t.Fatalf("limit = %d, error = %v", machinistConfig.Server.ConcurrentJobLimit(), err)
			}
		})
	}

	for _, value := range []string{"0", "-1"} {
		t.Run("reject "+value, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "token"), "secret")
			path := filepath.Join(directory, "config.toml")
			writeTestFile(t, path, "[server]\nworker_token_file = \"token\"\nmax_concurrent_jobs = "+value+"\n")
			if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "max_concurrent_jobs must be positive") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadCommandResolvesPromptAndHashesDefinition(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Inspect the repository for {{machinist.prompt}}.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[commands.plan]
executor = "test"
prompt_file = "plan.md"
timeout = "45s"
`)

	agent, err := LoadCommand(definition, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "plan" || agent.Prompt != "Inspect the repository for {{machinist.prompt}}.\n" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
	if agent.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s", agent.Timeout)
	}
	resolved, err := (Worker{Executors: map[string]Executor{"test": {Command: []string{"agent", "run"}}}}).ResolveCommandModel(agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Command) != 2 || resolved.Command[1] != "run" {
		t.Fatalf("command = %#v", resolved.Command)
	}
	if len(agent.Hash) != 64 {
		t.Fatalf("hash = %q", agent.Hash)
	}
}

func TestResolveCommandModelUsesAlias(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"codex": {
		Command: []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:  map[string]string{"luna": "gpt-5.6-luna"},
	}}}

	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "codex"}, "luna")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "gpt-5.6-luna" || strings.Join(resolved.Command, " ") != "codex exec --model=gpt-5.6-luna -" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

// A command that takes a model and is given none is refused. Dropping the
// parameter would hand the choice to the harness, which then runs whatever its
// own configuration defaults to — a model machinist neither selected nor
// recorded. Refusing is louder and, unlike a silent substitution, cannot be
// mistaken for the run that was asked for.
func TestResolveCommandModelRefusesToLetTheHarnessChoose(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"codex": {
		Command: []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:  map[string]string{"luna": "gpt-5.6-luna"},
	}}}

	if _, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "codex"}, ""); err == nil {
		t.Fatal("a command that takes a model ran without one")
	}
}

// A command that takes no model is unaffected: there is nothing for a harness
// to choose behind machinist's back.
func TestResolveCommandModelLeavesModellessCommandsAlone(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"plain": {Command: []string{"agent", "run"}}}}

	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "plain"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "" || strings.Join(resolved.Command, " ") != "agent run" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

// default_model is how a command says which model it runs when a caller names
// none. It is read through the alias table like any other request, so a profile
// can name its default the same way it names every other model it offers.
func TestResolveCommandModelUsesDeclaredDefault(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{
		"aliased": {
			Command:      []string{"codex", "exec", "--model=" + modelParameter, "-"},
			Models:       map[string]string{"luna": "gpt-5.6-luna"},
			DefaultModel: "luna",
		},
		"literal": {
			Command:      []string{"codex", "exec", "--model=" + modelParameter, "-"},
			DefaultModel: "gpt-5.6-sol",
		},
	}}

	for executor, want := range map[string]string{"aliased": "gpt-5.6-luna", "literal": "gpt-5.6-sol"} {
		resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: executor}, "")
		if err != nil {
			t.Fatalf("%s: %v", executor, err)
		}
		if resolved.Model != want {
			t.Errorf("%s model = %q, want %q", executor, resolved.Model, want)
		}
	}
}

// A profile's default reaches the same code as an executor's, so a typed
// profile is not left with the silent behaviour that was just removed.
func TestResolveCommandModelUsesAProfilesDeclaredDefault(t *testing.T) {
	worker := Worker{Profiles: map[string]Profile{"dgx": {
		Harness: "codex", Provider: "openai_compatible", AuthMode: "local",
		Command:      []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:       map[string]string{"local": "ds-0731"},
		DefaultModel: "local",
	}}}

	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "dgx"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "ds-0731" {
		t.Fatalf("profile model = %q, want ds-0731", resolved.Model)
	}
}

// A default that names nothing the command offers is a misconfiguration, and is
// reported as one rather than falling back to no model at all.
func TestResolveCommandModelRefusesADefaultThatIsNotOffered(t *testing.T) {
	worker := Worker{Executors: map[string]Executor{"codex": {
		Command:      []string{"codex", "exec", "--model=" + modelParameter, "-"},
		Models:       map[string]string{"luna": "gpt-5.6-luna"},
		DefaultModel: "nonesuch",
	}}}

	if _, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "codex"}, ""); err == nil {
		t.Fatal("a default naming an unoffered model was accepted")
	}
}

func TestResolveCommandModelDeniesSecretsFromOtherProfiles(t *testing.T) {
	worker := Worker{Profiles: map[string]Profile{
		"deepseek":  {Harness: "opencode", AuthMode: "api_key", SecretEnv: "DEEPSEEK_API_KEY", Command: []string{"opencode", "run"}},
		"anthropic": {Harness: "claude", AuthMode: "api_key", SecretEnv: "ANTHROPIC_API_KEY", Command: []string{"claude", "-p"}},
		"shared":    {Harness: "generic", AuthMode: "api_key", SecretEnv: "DEEPSEEK_API_KEY", Command: []string{"agent"}},
	}}
	resolved, err := worker.ResolveCommandModel(ResolvedCommand{Executor: "deepseek"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resolved.DeniedEnvironment, ",") != "ANTHROPIC_API_KEY" {
		t.Fatalf("denied environment = %#v", resolved.DeniedEnvironment)
	}
}

func TestResolveCommandModelRejectsUnsupportedSelection(t *testing.T) {
	for name, executor := range map[string]Executor{
		"missing placeholder": {Command: []string{"agent", "run"}},
		"unknown alias":       {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"fast": "fast-v1"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (Worker{Executors: map[string]Executor{"test": executor}}).ResolveCommandModel(ResolvedCommand{Executor: "test"}, "other")
			if err == nil {
				t.Fatal("expected model selection error")
			}
		})
	}
}

func TestWorkerModelCapabilitiesAndConfiguration(t *testing.T) {
	worker, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors: map[string]Executor{
			"aliased": {Command: []string{"agent", "--model=" + modelParameter}, Models: map[string]string{"slow": "v2", "fast": "v1"}},
			"raw":     {Command: []string{"agent", "--model=" + modelParameter}},
			"fixed":   {Command: []string{"agent"}},
		},
	}, func() (string, error) { return "unused", nil })
	if err != nil {
		t.Fatal(err)
	}
	capabilities := worker.ModelCapabilities()
	if strings.Join(capabilities["aliased"], ",") != "fast,slow" || capabilities["raw"] == nil {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if _, ok := capabilities["fixed"]; ok {
		t.Fatalf("fixed executor advertised model support: %#v", capabilities)
	}

	_, err = applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent"}, Models: map[string]string{"fast": "v1"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), modelParameter) {
		t.Fatalf("invalid model config error = %v", err)
	}
}

func TestLoadWorkerRejectsCompoundModelPlaceholderArgument(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "prefix-" + modelParameter}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "complete optional") {
		t.Fatalf("compound placeholder error = %v", err)
	}
}

func TestLoadWorkerRejectsLegacyFactoryModelParameter(t *testing.T) {
	_, err := applyWorkerDefaultsWithHostname(Worker{
		Name:          "test",
		DataDirectory: t.TempDir(),
		Executors:     map[string]Executor{"invalid": {Command: []string{"agent", "--model={{factory.model}}"}}},
	}, func() (string, error) { return "unused", nil })
	if err == nil || !strings.Contains(err.Error(), "legacy Factory parameter namespace") {
		t.Fatalf("legacy model parameter error = %v", err)
	}
}

func TestRenderPromptReplacesEveryPromptParameterWithoutReevaluation(t *testing.T) {
	agent := ResolvedCommand{Prompt: "Before {{machinist.prompt}} between {{machinist.prompt}} after"}
	prompt := "fix {{machinist.prompt}} and $(touch never)"
	rendered, err := RenderPrompt(agent, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := "Before " + prompt + " between " + prompt + " after"
	if rendered.Prompt != want {
		t.Fatalf("prompt = %q, want %q", rendered.Prompt, want)
	}
}

func TestRenderPromptRejectsEmptyAndOversizedPrompts(t *testing.T) {
	agent := ResolvedCommand{Prompt: promptParameter}
	if _, err := RenderPrompt(agent, " \n\t"); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected empty-prompt error, got %v", err)
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected prompt-size error, got %v", err)
	}
}

func TestRenderPromptRejectsOversizedRenderedPromptBeforeReplacement(t *testing.T) {
	agent := ResolvedCommand{
		Name:   "plan",
		Prompt: strings.Repeat(promptParameter, maxPromptBytes/len(promptParameter)),
	}
	if _, err := RenderPrompt(agent, strings.Repeat("x", maxInputPromptBytes)); err == nil || !strings.Contains(err.Error(), "rendered command prompt exceeds") {
		t.Fatalf("expected rendered-size error, got %v", err)
	}
}

func TestLoadCommandRequiresPromptParameterAndRejectsUnsupportedMachinistParameter(t *testing.T) {
	for _, test := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "missing prompt", prompt: "Plan this ticket.\n", want: "must include {{machinist.prompt}}"},
		{name: "legacy Factory namespace", prompt: "Plan {{machinist.prompt}} with {{factory.prompt}}.\n", want: "legacy Factory parameter namespace"},
		{name: "legacy task parameter", prompt: "Plan {{machinist.task}}.\n", want: "unsupported Machinist parameter"},
		{name: "unsupported parameter", prompt: "Plan {{machinist.prompt}} in {{machinist.repository}}.\n", want: "unsupported Machinist parameter"},
		{name: "empty parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.}}.\n", want: "unsupported Machinist parameter"},
		{name: "unclosed parameter", prompt: "Plan {{machinist.prompt}} with {{machinist.repository.\n", want: "malformed Machinist parameter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "plan.md"), test.prompt)
			definition := filepath.Join(directory, "config.toml")
			writeTestFile(t, definition, "[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\n")
			if _, err := LoadCommand(definition, "plan"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestLoadCommandNormalizesPromptLineEndings(t *testing.T) {
	// A prompt file checked out with CRLF must produce the same prompt as one
	// checked out with LF: what an agent reads on stdin cannot depend on the
	// checkout's line-ending configuration.
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "# Heading\r\nPlan {{machinist.prompt}}.\r\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, "[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\n")

	command, err := LoadCommand(definition, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Heading\nPlan {{machinist.prompt}}.\n"; command.Prompt != want {
		t.Fatalf("prompt = %q, want %q", command.Prompt, want)
	}
}

func TestLoadCommandRejectsMissingAndInvalidDefinitions(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "plan.md"), "Plan.\n")
	definition := filepath.Join(directory, "config.toml")
	writeTestFile(t, definition, `[commands.plan]
prompt_file = "plan.md"
`)

	if _, err := LoadCommand(definition, "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("expected missing-agent error, got %v", err)
	}
	if _, err := LoadCommand(definition, "plan"); err == nil || !strings.Contains(err.Error(), "must define executor") {
		t.Fatalf("expected executor error, got %v", err)
	}
}

func TestExampleCommandDefinitionsLoad(t *testing.T) {
	definition := filepath.Join("..", "..", "examples", "config.toml")
	definitions, err := LoadDefinitions(definition)
	if err != nil {
		t.Fatal(err)
	}
	exampleCommands := []string{"foreman", "audit", "shepherd", "delegate-plan", "delegate-build", "delegate-review"}
	if len(definitions.Commands) != len(exampleCommands) {
		t.Fatalf("example commands = %#v, want %v", definitions.Commands, exampleCommands)
	}
	for _, name := range exampleCommands {
		if _, ok := definitions.Commands[name]; !ok {
			t.Fatalf("example %s command is missing", name)
		}
	}
	// The review route reads this role to decide whether a verdict is
	// independent of the run it judges. A delegate-review run under any other
	// role produces reviews the control plane refuses, which reads as nobody
	// having reviewed the change.
	if role := definitions.Commands["delegate-review"].Role; role != "reviewer" {
		t.Fatalf("example delegate-review role = %q, want %q", role, "reviewer")
	}

	for _, name := range exampleCommands {
		t.Run(name, func(t *testing.T) {
			agent, err := LoadCommand(definition, name)
			if err != nil {
				t.Fatal(err)
			}
			if agent.Name != name {
				t.Fatalf("agent name = %q, want %q", agent.Name, name)
			}
			if !strings.Contains(agent.Prompt, promptParameter) {
				t.Fatalf("agent prompt does not contain %s", promptParameter)
			}
		})
	}
	foreman, err := LoadCommand(definition, "foreman")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"Never plan the solution",
		"Perform this discovery at the start of every run",
		"**Existing implementation:**",
		"**CI failure:**",
		"**Review feedback:**",
		"**Open pull request:**",
		"**Completed planning:**",
		"**New issue:**",
		"a verified branch without an open pull request",
		"any dirty or incomplete work",
		"Positive numbers are repairs",
		"reset, reuse, or cap it on resume",
		"repair count without a maximum",
		"Existing work must reuse its branch, worktree, and pull request",
		"create a second pull request for the issue",
		"`machinist:ready-for-review` or a verified ready/completed state",
		"stale remote",
		"Repair or create its deterministic isolated worktree",
		"fast-forward a clean local head that is an ancestor",
		"Preserve dirty, ahead, or unpublished",
		"each recorded head is an ancestor of",
		"Never overwrite",
		"Create a missing local",
		"clean worktree and equality between the local branch head",
		"Every delegate prompt must require a concise Markdown handoff",
		"## Planning handoff",
		"## Build handoff",
		"## Review handoff",
		"## Repair handoff",
		"complete diff",
		"inspect the branch, HEAD, worktree",
		"return a valid handoff, whether it exits or remains active",
		"read-only reviewer",
		"Never inline the diff",
		// The Foreman delegates and never does the work itself. What "fresh"
		// asks for is a separate context, not one particular harness feature,
		// so the prompt names both mechanisms and blocks only when neither is
		// there. Before this, a harness with no native subagents blocked on
		// every issue while a working second mechanism sat unused.
		"Coordinate fresh coding delegates",
		"Use a fresh delegate for planning, building, each repair, and every review",
		"A fresh native subagent, if this harness has them",
		"Otherwise a fresh Machinist run",
		"Only if neither mechanism is available",
		"not having looked for the second\nmechanism is not",
		// The implementer opens a draft and never lifts it: its own review is
		// not independent, because it wrote the change. See
		// docs/draft-until-reviewed.md.
		"open one **draft** pull request linked",
		"Never run `gh pr ready`",
		"never convert one back",
		"For both paths, confirm the base, exact head, issue link",
		"recheck that it is open before pushing",
		"return to linked-pull-request resolution",
		"Use this one loop for local review, CI",
		"Resolve linked pull requests before",
		"Reuse exactly one open pull request and ignore historical closed or merged",
		"If multiple are open, or none is open and any is merged",
		"With none open and",
		"closed-unmerged candidates present",
		"multiple candidates or any",
		"selection, reopening, or verification failure",
		"For any existing or reopened",
		"open pull request without a usable worktree",
		"After every code change",
		"Approval applies only to the reviewed SHA",
		"push `<approved-sha>:refs/heads/<branch>`",
		"automated reviewers and review bots",
		"event, branch, path",
		"exactly match the",
		"missing expected results remain pending",
		"Exclude human",
		"Poll no more often than every 30 seconds",
		"at most 20 minutes",
		"set `machinist:blocked`",
		"resolve only threads whose feedback is fully addressed",
		"Compare each finding with",
		"`<!-- machinist:foreman-pr -->`",
		"persist its head, approval, checks",
		"If none remain, return to the originating stage",
		"Persist the count",
		"immediately after a code-changing commit and before Local review",
		"failure keeps the prior count",
		"If no",
		"pull request exists, continue to Create or reuse the pull request",
		"Never merge",
		"Keep the open-pull-request worktree",
		"Before any terminal stop or handoff",
	} {
		if !strings.Contains(foreman.Prompt, rule) {
			t.Fatalf("foreman prompt does not contain %q", rule)
		}
	}
	for _, forbidden := range []string{
		// The one-mechanism wording, which blocked a harness that had a
		// perfectly good second way to delegate.
		"Coordinate native coding subagents",
		"If native subagents are unavailable",
		"open one non-draft pull request",
		"open non-draft state",
		"mark the pull request ready for human review",
		"branch, complete diff",
		"SUBAGENT role=<role>",
		"Attempts `1` and `2` are the two allowed repairs",
		"at most two total",
		"block if it would exceed two",
		"Attempts `1`, `2`, and `3` are the allowed repairs",
		"at most three total",
		"block if it would exceed three",
	} {
		if strings.Contains(foreman.Prompt, forbidden) {
			t.Fatalf("foreman prompt still contains %q", forbidden)
		}
	}
	for _, heading := range []string{
		"# Ordered state entry\n",
		"## Local review\n",
		"## Automation gate\n",
		"# Shared repair loop\n",
	} {
		if count := strings.Count(foreman.Prompt, heading); count != 1 {
			t.Fatalf("foreman prompt contains %q %d times, want once", heading, count)
		}
	}
	if existing, open := strings.Index(foreman.Prompt, "**Existing implementation:**"), strings.Index(foreman.Prompt, "**Open pull request:**"); existing < 0 || open < 0 || existing > open {
		t.Fatalf("foreman prompt must classify unpublished implementation before open pull request: existing=%d open=%d", existing, open)
	}
	if reopen, recover := strings.Index(foreman.Prompt, "closed-unmerged candidates present"), strings.Index(foreman.Prompt, "For any existing or reopened"); reopen < 0 || recover < 0 || reopen > recover {
		t.Fatalf("foreman prompt must reopen a unique safe pull request before worktree recovery: reopen=%d recover=%d", reopen, recover)
	}
	// The cap is pinned just above the prompt's current size rather than set to
	// a round number with room in it, so that any growth has to be argued for
	// here. Raise it only for a rule that has to be in the prompt, and re-pin it
	// tight afterwards: a ceiling that is moved whenever it is reached is not a
	// ceiling. Last raised for the second delegation mechanism above
	// (docs/prompt-parity.md).
	if words := len(strings.Fields(foreman.Prompt)); words > 2380 {
		t.Fatalf("foreman prompt has %d words, want no more than 2380", words)
	}
	// Every command the Foreman tells itself to run has to be a command this
	// file defines. The set is read out of the prompt rather than restated
	// here, so a mechanism added to the prompt and to nothing else fails here
	// instead of at three in the morning inside a run that has already claimed
	// an issue.
	named := regexp.MustCompile(`machinist run --command ([a-z0-9-]+)`).FindAllStringSubmatch(foreman.Prompt, -1)
	if len(named) == 0 {
		t.Fatal("foreman prompt names no machinist run command: the second delegation mechanism is gone")
	}
	for _, match := range named {
		if _, ok := definitions.Commands[match[1]]; !ok {
			t.Fatalf("foreman prompt runs --command %q, which examples/config.toml does not define", match[1])
		}
	}

	audit, err := LoadCommand(definition, "audit")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"fresh general-purpose subagents",
		"For every candidate",
		"separate fresh general-purpose",
		"Do not combine candidates in one verification task",
		"verifier does not confirm as a correctness bug",
		"current open GitHub issues",
		"no more than three issues",
		"affected files and",
		"observed behavior, expected",
		"Never edit, create, delete, move, or format",
		"Never create or switch branches, commit, push, or open a pull request",
		"Never fix a bug, create a",
	} {
		if !strings.Contains(audit.Prompt, rule) {
			t.Fatalf("audit prompt does not contain %q", rule)
		}
	}
}

func TestWorkflowExampleDefinitionsLoad(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
	}{
		{name: "issue-to-pr", commands: []string{"issue-to-pr"}},
		{name: "multi-review", commands: []string{"multi-review"}},
		{name: "code-audit", commands: []string{"code-audit"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := filepath.Join("..", "..", "examples", "workflows", test.name, "config.toml")
			for _, name := range test.commands {
				agent, err := LoadCommand(definition, name)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(agent.Prompt, promptParameter) {
					t.Fatalf("agent %q prompt does not contain %s", name, promptParameter)
				}
				if test.name == "issue-to-pr" {
					for _, rule := range []string{"continue without a fixed cap", "Repair confirmed code defects with the next repair number"} {
						if !strings.Contains(agent.Prompt, rule) {
							t.Fatalf("agent %q prompt does not contain %q", name, rule)
						}
					}
					for _, obsolete := range []string{"at most two repair rounds", "same two-round limit", "after both repair rounds", "at most three repair rounds", "same three-round limit", "after all three repair rounds"} {
						if strings.Contains(agent.Prompt, obsolete) {
							t.Fatalf("agent %q prompt still contains %q", name, obsolete)
						}
					}
				}
				if test.name == "multi-review" {
					continue
				}
				for _, section := range []string{"# Role", "# Input", "# Required result", "# Procedure", "# Boundaries"} {
					if !strings.Contains(agent.Prompt, section) {
						t.Fatalf("agent %q prompt does not contain %q", name, section)
					}
				}
			}
		})
	}
}

func TestShippedGuidanceDoesNotDescribeARepairCap(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "skills", "machinist", "SKILL.md"),
		filepath.Join("..", "..", ".github", "site", "index.html"),
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, obsolete := range []string{"repair limit", "Limited repair attempts", "Repair loops have a fixed limit", "Bounded repair"} {
			if strings.Contains(string(body), obsolete) {
				t.Fatalf("%s still contains %q", path, obsolete)
			}
		}
	}
}

func TestShippedMachinistSkillDescribesGitHubIntake(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "machinist", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	guidance := string(body)
	for _, required := range []string{"[triggers.github.<name>]", "machinist:requested", "machinist:queued", "intake labels"} {
		if !strings.Contains(guidance, required) {
			t.Fatalf("%s does not describe %q", path, required)
		}
	}
	if strings.Contains(guidance, "Label-based delegation is not implemented") {
		t.Fatalf("%s still rejects label-based delegation", path)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRejectsRemovedShepherdSchedules(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, filepath.Join(directory, "shepherd.md"), "{{machinist.prompt}}\n")
	writeTestFile(t, path, "[commands.shepherd]\nexecutor=\"test\"\nprompt_file=\"shepherd.md\"\n[shepherd.api]\nrepository=\"api\"\nevery=\"15m\"\nmax_actions=1\n")
	_, err := LoadDefinitions(path)
	if err == nil || !strings.Contains(err.Error(), "shepherd schedules were removed") {
		t.Fatalf("error = %v, want removed shepherd schedule guidance", err)
	}
}
