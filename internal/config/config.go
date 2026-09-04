package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/triggers"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultTimeout           = 30 * time.Minute
	maxConfigBytes           = 1 << 20
	maxPromptBytes           = 256 << 10
	maxInputPromptBytes      = 256 << 10
	maxRenderedPromptBytes   = maxPromptBytes + maxInputPromptBytes
	maxTokenBytes            = 8 << 10
	defaultWorkerDirName     = ".machinist/worker"
	defaultServerDirName     = ".machinist/server"
	promptParameter          = "{{machinist.prompt}}"
	modelParameter           = "{{machinist.model}}"
	machinistParameterPrefix = "{{machinist"
	legacyFactoryPrefix      = "{{factory."
)

type Worker struct {
	Name          string                `toml:"name"`
	DataDirectory string                `toml:"data_directory"`
	ControlPlane  ControlPlane          `toml:"control_plane"`
	Environment   WorkerEnvironment     `toml:"environment"`
	Telemetry     WorkerTelemetry       `toml:"telemetry"`
	Executors     map[string]Executor   `toml:"executors"`
	Profiles      map[string]Profile    `toml:"profiles"`
	Repositories  map[string]Repository `toml:"repositories"`
	configDir     string
}

type WorkerEnvironment struct {
	Detect *bool    `toml:"detect"`
	Tags   []string `toml:"tags"`
}

type WorkerTelemetry struct {
	Enabled          bool   `toml:"enabled"`
	URL              string `toml:"url"`
	TokenFile        string `toml:"token_file"`
	IdentitySaltFile string `toml:"identity_salt_file"`
	EndpointID       string `toml:"endpoint_id"`
}

func (e WorkerEnvironment) DetectionEnabled() bool {
	return e.Detect == nil || *e.Detect
}

type ControlPlane struct {
	URL       string `toml:"url"`
	TokenFile string `toml:"token_file"`
}

type Executor struct {
	Command      []string          `toml:"command"`
	Models       map[string]string `toml:"models"`
	HerdrAgent   string            `toml:"herdr_agent"`
	HerdrArgs    []string          `toml:"herdr_args"`
	HerdrCommand []string          `toml:"herdr_command"`
}

// Profile describes one typed, worker-local harness/provider combination.
// SecretEnv is an environment variable name only; its value is never loaded
// into configuration or sent to the control plane.
type Profile struct {
	Harness             string            `toml:"harness"`
	Provider            string            `toml:"provider"`
	AuthMode            string            `toml:"auth_mode"`
	SecretEnv           string            `toml:"secret_env"`
	BaseURL             string            `toml:"base_url"`
	BaseURLEnv          string            `toml:"base_url_env"`
	AllowInsecureHTTP   bool              `toml:"allow_insecure_http"`
	Command             []string          `toml:"command"`
	HerdrAgent          string            `toml:"herdr_agent"`
	HerdrArgs           []string          `toml:"herdr_args"`
	HerdrCommand        []string          `toml:"herdr_command"`
	Models              map[string]string `toml:"models"`
	RequiresExecutables []string          `toml:"requires_executables"`
	RequiresOS          []string          `toml:"requires_os"`
	RequiresArch        []string          `toml:"requires_arch"`
	RequiresTags        []string          `toml:"requires_tags"`
}

func (e Executor) supportsModel() bool {
	return slices.ContainsFunc(e.Command, func(argument string) bool { return strings.Contains(argument, modelParameter) })
}

type Repository struct {
	Path string `toml:"path"`
}

type Server struct {
	Listen            string `toml:"listen"`
	Database          string `toml:"database"`
	WorkerTokenFile   string `toml:"worker_token_file"`
	MaxConcurrentJobs *int   `toml:"max_concurrent_jobs"`
	configDir         string
}

type Config struct {
	Server        Server             `toml:"server"`
	Observability Observability      `toml:"observability"`
	Commands      map[string]Command `toml:"commands"`
	Routes        map[string]Route   `toml:"routes"`
	GitHub        GitHub             `toml:"github"`
	Triggers      TriggerDefinitions `toml:"triggers"`
	path          string
}

type Observability struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
}

type GitHub struct {
	Repositories map[string]string `toml:"repositories"`
}

type TriggerDefinitions struct {
	GitHub   map[string]GitHubTrigger   `toml:"github"`
	Interval map[string]IntervalTrigger `toml:"interval"`
	Cron     map[string]CronTrigger     `toml:"cron"`
}

type TriggerSelection struct {
	Command string `toml:"command"`
	Model   string `toml:"model"`
}

type GitHubTrigger struct {
	TriggerSelection
	Every string `toml:"every"`
	Label string `toml:"label"`
}

type IntervalTrigger struct {
	TriggerSelection
	Every      string `toml:"every"`
	Repository string `toml:"repository"`
	Prompt     string `toml:"prompt"`
}

type CronTrigger struct {
	TriggerSelection
	Schedule   string `toml:"schedule"`
	Timezone   string `toml:"timezone"`
	Repository string `toml:"repository"`
	Prompt     string `toml:"prompt"`
}

type ResolvedTrigger struct {
	Identity           string
	Family             string
	Name               string
	Repository         string
	GitHubRepository   string
	GitHubRepositories map[string]string
	Every              time.Duration
	Schedule           string
	Timezone           string
	Label              string
	SelectionName      string
	Model              string
	Prompt             string
	Command            ResolvedCommand
	Signature          string
	cron               *triggers.Cron
}

type Command struct {
	Executor   string `toml:"executor"`
	Profile    string `toml:"profile"`
	Route      string `toml:"route"`
	Role       string `toml:"role"`
	PromptFile string `toml:"prompt_file"`
	Timeout    string `toml:"timeout"`
}

type Route struct {
	Profiles       []string `toml:"profiles"`
	MaxAttempts    int      `toml:"max_attempts"`
	MaxTotalTokens int64    `toml:"max_total_tokens"`
	FallbackOn     []string `toml:"fallback_on"`
}

type ResolvedCommand struct {
	Name              string
	Executor          string
	Profile           string
	Route             string
	Candidates        []string
	MaxAttempts       int
	MaxTotalTokens    int64
	FallbackOn        []string
	Harness           string
	Provider          string
	AuthMode          string
	Role              string
	Environment       map[string]string
	DeniedEnvironment []string
	Command           []string
	HerdrAgent        string
	HerdrArgs         []string
	HerdrCommand      []string
	Model             string
	Prompt            string
	Timeout           time.Duration
	Definition        string
	Hash              string
}

func LoadWorker(path string) (Worker, error) {
	worker := Worker{}
	if path == "" {
		defaultPath, err := defaultConfigPath("worker.toml")
		if err != nil {
			return Worker{}, err
		}
		path = defaultPath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			worker.configDir = filepath.Dir(path)
			return applyWorkerDefaults(worker)
		} else if err != nil {
			return Worker{}, fmt.Errorf("inspect worker config: %w", err)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Worker{}, fmt.Errorf("read worker config %q: %w", absPath, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&worker); err != nil {
		return Worker{}, fmt.Errorf("parse worker config %q: %w", absPath, err)
	}
	worker.configDir = filepath.Dir(absPath)
	return applyWorkerDefaults(worker)
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		defaultPath, err := defaultConfigPath("config.toml")
		if err != nil {
			return Config{}, err
		}
		path = defaultPath
	}
	machinistConfig, err := loadConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	machinistConfig.Server.configDir = filepath.Dir(machinistConfig.path)
	machinistConfig.Server, err = applyServerDefaults(machinistConfig.Server)
	if err != nil {
		return Config{}, err
	}
	if err := applyObservabilityDefaults(&machinistConfig.Observability); err != nil {
		return Config{}, err
	}
	return machinistConfig, nil
}

func loadConfigFile(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve Machinist config: %w", err)
	}
	body, err := readBoundedFile(absPath, maxConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("read Machinist config %q: %w", absPath, err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(body, &raw); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	if _, ok := raw["pipelines"]; ok {
		return Config{}, fmt.Errorf("parse Machinist config %q: pipelines were removed; replace each pipeline with a repository-owned orchestration script configured under [commands]", absPath)
	}
	if _, ok := raw["shepherd"]; ok {
		return Config{}, fmt.Errorf("parse Machinist config %q: shepherd schedules were removed; schedule the shepherd command with a [triggers.cron.NAME] or [triggers.interval.NAME] trigger", absPath)
	}
	if _, ok := raw["agents"]; ok {
		return Config{}, fmt.Errorf("parse Machinist config %q: agents were renamed to commands; move [agents.NAME] definitions to [commands.NAME] and use --command", absPath)
	}
	if err := validateTriggerKeys(raw); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	machinistConfig := Config{path: absPath}
	decoder := toml.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&machinistConfig); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	if err := validateRoutes(machinistConfig.Routes); err != nil {
		return Config{}, fmt.Errorf("parse Machinist config %q: %w", absPath, err)
	}
	return machinistConfig, nil
}

func (c Config) Path() string { return c.path }

// CommandNames lists the defined command names in sorted order.
func (c Config) CommandNames() []string { return sortedMapKeys(c.Commands) }

func (w Worker) ResolveMachinistConfig(override string) (string, error) {
	if override != "" {
		return resolveConfigPath(override, "")
	}
	return resolveConfigPath("config.toml", w.configDir)
}

func (w Worker) ResolveCommandModel(command ResolvedCommand, requestedModel string) (ResolvedCommand, error) {
	if command.Route != "" && command.Executor == "" {
		return ResolvedCommand{}, fmt.Errorf("route %q has not been resolved to an available profile", command.Route)
	}
	executor, ok := w.Executors[command.Executor]
	if ok {
		command.HerdrAgent = strings.ToLower(strings.TrimSpace(executor.HerdrAgent))
		command.HerdrArgs = renderModelArguments(executor.HerdrArgs, requestedModel, executor.Models)
		command.HerdrCommand = renderModelArguments(executor.HerdrCommand, requestedModel, executor.Models)
	}
	if !ok {
		profile, profileOK := w.Profiles[command.Executor]
		if !profileOK {
			return ResolvedCommand{}, fmt.Errorf("executor or profile %q is not configured on this worker", command.Executor)
		}
		executor = Executor{Command: profile.Command, Models: profile.Models}
		command.Profile = command.Executor
		command.Harness = profile.Harness
		command.Provider = profile.Provider
		command.AuthMode = profile.AuthMode
		command.Environment = profileEnvironment(profile)
		command.DeniedEnvironment = w.otherProfileSecrets(command.Executor, profile.SecretEnv)
		command.HerdrAgent = profile.HerdrAgent
		command.HerdrArgs = renderModelArguments(profile.HerdrArgs, requestedModel, profile.Models)
		command.HerdrCommand = renderModelArguments(profile.HerdrCommand, requestedModel, profile.Models)
	}
	if err := validateCommand(command.Executor, executor.Command); err != nil {
		return ResolvedCommand{}, err
	}
	model, err := resolveModel(command.Executor, executor, requestedModel)
	if err != nil {
		return ResolvedCommand{}, err
	}
	command.Command = make([]string, 0, len(executor.Command))
	for _, argument := range executor.Command {
		if model == "" && strings.Contains(argument, modelParameter) {
			continue
		}
		command.Command = append(command.Command, strings.ReplaceAll(argument, modelParameter, model))
	}
	command.Model = model
	return command, nil
}

func renderModelArguments(arguments []string, requested string, models map[string]string) []string {
	model := strings.TrimSpace(requested)
	if resolved, ok := models[model]; ok {
		model = strings.TrimSpace(resolved)
	}
	rendered := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if model == "" && strings.Contains(argument, modelParameter) {
			continue
		}
		rendered = append(rendered, strings.ReplaceAll(argument, modelParameter, model))
	}
	return rendered
}

// otherProfileSecrets prevents a selected profile from inheriting API keys
// declared by other profiles. Normal worker environment, including harness
// subscription sessions and repository credentials, remains compatible.
func (w Worker) otherProfileSecrets(selected, selectedSecret string) []string {
	selectedSecret = strings.TrimSpace(selectedSecret)
	denied := make(map[string]bool)
	for name, profile := range w.Profiles {
		secret := strings.TrimSpace(profile.SecretEnv)
		if name != selected && secret != "" && secret != selectedSecret {
			denied[secret] = true
		}
	}
	return sortedMapKeys(denied)
}

// ResolveRoute chooses the first configured candidate present in available.
// The ordered candidate list is portable policy; profile details remain local.
func (w Worker) ResolveRoute(command ResolvedCommand, available []string) (ResolvedCommand, error) {
	if command.Route == "" {
		return command, nil
	}
	availableSet := make(map[string]bool, len(available))
	for _, name := range available {
		availableSet[name] = true
	}
	for _, name := range command.Candidates {
		if availableSet[name] {
			command.Executor = name
			command.Profile = name
			return command, nil
		}
	}
	return ResolvedCommand{}, fmt.Errorf("route %q has no available profile on this worker", command.Route)
}

func resolveModel(name string, executor Executor, requested string) (string, error) {
	model := strings.TrimSpace(requested)
	if model == "" {
		return "", nil
	}
	if !executor.supportsModel() {
		return "", fmt.Errorf("executor %q does not support model selection; add %s to its command", name, modelParameter)
	}
	if resolved, ok := executor.Models[model]; ok {
		model = strings.TrimSpace(resolved)
	} else if len(executor.Models) > 0 {
		return "", fmt.Errorf("model %q is not configured for executor %q", model, name)
	}
	if model == "" || len(model) > 128 || strings.ContainsAny(model, "\x00\r\n") {
		return "", fmt.Errorf("executor %q model is invalid", name)
	}
	return model, nil
}

func (w Worker) ResolveRepository(name string) (string, error) {
	repository, ok := w.Repositories[name]
	if !ok {
		return "", fmt.Errorf("repository %q is not configured on this worker", name)
	}
	return resolveConfigPath(repository.Path, w.configDir)
}

func (w Worker) WorkerToken() (string, error) {
	if strings.TrimSpace(w.ControlPlane.TokenFile) == "" {
		return "", errors.New("control_plane.token_file is required")
	}
	path, err := resolveConfigPath(w.ControlPlane.TokenFile, w.configDir)
	if err != nil {
		return "", err
	}
	return readToken(path)
}

// TelemetryEnvironment returns only collector configuration references. Token
// and identity-salt contents are read by the observer, never by Machinist.
func (w Worker) TelemetryEnvironment() map[string]string {
	if !w.Telemetry.Enabled {
		return nil
	}
	values := map[string]string{
		"MACHINIST_TELEMETRY_ENABLED":            "1",
		"MACHINIST_TELEMETRY_URL":                w.Telemetry.URL,
		"MACHINIST_TELEMETRY_TOKEN_FILE":         w.Telemetry.TokenFile,
		"MACHINIST_TELEMETRY_IDENTITY_SALT_FILE": w.Telemetry.IdentitySaltFile,
		"MACHINIST_TELEMETRY_ENDPOINT_ID":        w.Telemetry.EndpointID,
	}
	// Existing Buzz observers use these names. Keeping aliases here lets the
	// collector remain separately versioned while integrations migrate.
	values["BUZZ_TELEMETRY_ENABLED"] = values["MACHINIST_TELEMETRY_ENABLED"]
	values["BUZZ_TELEMETRY_URL"] = values["MACHINIST_TELEMETRY_URL"]
	values["BUZZ_TELEMETRY_TOKEN_FILE"] = values["MACHINIST_TELEMETRY_TOKEN_FILE"]
	values["BUZZ_TELEMETRY_IDENTITY_SALT_FILE"] = values["MACHINIST_TELEMETRY_IDENTITY_SALT_FILE"]
	values["BUZZ_TELEMETRY_ENDPOINT_ID"] = values["MACHINIST_TELEMETRY_ENDPOINT_ID"]
	return values
}

func (w Worker) ExecutorNames() []string {
	names := make(map[string]bool, len(w.Executors)+len(w.Profiles))
	for name := range w.Executors {
		names[name] = true
	}
	for name := range w.Profiles {
		names[name] = true
	}
	return sortedMapKeys(names)
}

func (w Worker) ModelCapabilities() map[string][]string {
	capabilities := make(map[string][]string)
	for name, executor := range w.Executors {
		if executor.supportsModel() {
			capabilities[name] = sortedMapKeys(executor.Models)
		}
	}
	for name, profile := range w.Profiles {
		if len(profile.Models) > 0 {
			capabilities[name] = sortedMapKeys(profile.Models)
		}
	}
	return capabilities
}

func (w Worker) RepositoryNames() []string { return sortedMapKeys(w.Repositories) }

func (s Server) WorkerToken() (string, error) {
	return readToken(s.WorkerTokenFile)
}

func (s Server) ConcurrentJobLimit() int {
	if s.MaxConcurrentJobs == nil {
		return 0
	}
	return *s.MaxConcurrentJobs
}

func LoadCommand(definitionPath, name string) (ResolvedCommand, error) {
	if strings.TrimSpace(name) == "" {
		return ResolvedCommand{}, errors.New("command name is required")
	}
	definition, err := loadConfigFile(definitionPath)
	if err != nil {
		return ResolvedCommand{}, err
	}
	return definition.ResolveCommand(name)
}

// ResolveCommand resolves one named command from an already loaded definition.
func (c Config) ResolveCommand(name string) (ResolvedCommand, error) {
	command, ok := c.Commands[name]
	if !ok {
		return ResolvedCommand{}, fmt.Errorf("command %q is not defined in %s", name, c.path)
	}
	return resolveCommand(c.path, name, command, c.Routes)
}

func LoadDefinitions(path string) (Config, error) { return loadConfigFile(path) }

func resolveCommand(definitionPath, name string, command Command, routes map[string]Route) (ResolvedCommand, error) {
	command.Executor = strings.TrimSpace(command.Executor)
	command.Profile = strings.TrimSpace(command.Profile)
	command.Route = strings.TrimSpace(command.Route)
	configuredSelections := 0
	for _, value := range []string{command.Executor, command.Profile, command.Route} {
		if value != "" {
			configuredSelections++
		}
	}
	if configuredSelections != 1 {
		return ResolvedCommand{}, fmt.Errorf("command %q must define executor, profile, or route (exactly one)", name)
	}
	executionName := command.Executor
	if command.Profile != "" {
		executionName = command.Profile
	}
	var route Route
	if command.Route != "" {
		var ok bool
		route, ok = routes[command.Route]
		if !ok {
			return ResolvedCommand{}, fmt.Errorf("command %q route %q is not defined", name, command.Route)
		}
	}
	command.Role = strings.ToLower(strings.TrimSpace(command.Role))
	if command.Role != "" && (len(command.Role) > 64 || !safeEnvironmentTag(command.Role)) {
		return ResolvedCommand{}, fmt.Errorf("command %q role is invalid", name)
	}
	prompt := promptParameter
	if strings.TrimSpace(command.PromptFile) != "" {
		promptPath, err := expandHome(command.PromptFile)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("resolve command %q prompt: %w", name, err)
		}
		if !filepath.IsAbs(promptPath) {
			promptPath = filepath.Join(filepath.Dir(definitionPath), promptPath)
		}
		body, err := readBoundedFile(filepath.Clean(promptPath), maxPromptBytes)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("read command %q prompt %q: %w", name, promptPath, err)
		}
		prompt = string(body)
		if strings.TrimSpace(prompt) == "" {
			return ResolvedCommand{}, fmt.Errorf("command %q prompt is empty", name)
		}
		if err := validatePromptParameters(name, prompt); err != nil {
			return ResolvedCommand{}, err
		}
	}
	timeout := defaultTimeout
	if command.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(command.Timeout)
		if err != nil {
			return ResolvedCommand{}, fmt.Errorf("command %q timeout: %w", name, err)
		}
		if timeout <= 0 {
			return ResolvedCommand{}, fmt.Errorf("command %q timeout must be positive", name)
		}
	}

	resolved := ResolvedCommand{
		Name:           name,
		Executor:       executionName,
		Profile:        command.Profile,
		Route:          command.Route,
		Candidates:     slices.Clone(route.Profiles),
		MaxAttempts:    route.MaxAttempts,
		MaxTotalTokens: route.MaxTotalTokens,
		FallbackOn:     slices.Clone(route.FallbackOn),
		Role:           command.Role,
		Prompt:         prompt,
		Timeout:        timeout,
		Definition:     definitionPath,
	}
	var err error
	resolved.Hash, err = commandHash(resolved)
	if err != nil {
		return ResolvedCommand{}, err
	}
	return resolved, nil
}

func validateRoutes(routes map[string]Route) error {
	allowedFallbacks := map[string]bool{
		"capacity": true, "rate_limit": true, "transient": true,
		"model_unavailable": true, "harness_crash": true, "timeout": true,
	}
	for name, route := range routes {
		if strings.TrimSpace(name) == "" || len(name) > 64 || !safeEnvironmentTag(strings.ToLower(strings.TrimSpace(name))) {
			return errors.New("route names must be non-empty portable identifiers")
		}
		if len(route.Profiles) == 0 || len(route.Profiles) > 8 {
			return fmt.Errorf("route %q must define between 1 and 8 profiles", name)
		}
		seen := make(map[string]bool, len(route.Profiles))
		for _, profile := range route.Profiles {
			if profile != strings.TrimSpace(profile) || len(profile) > 64 || !safeEnvironmentTag(strings.ToLower(profile)) || seen[profile] {
				return fmt.Errorf("route %q profile %q is invalid or duplicated", name, profile)
			}
			seen[profile] = true
		}
		if route.MaxAttempts < 0 || route.MaxAttempts > 8 {
			return fmt.Errorf("route %q max_attempts must be between 1 and 8 when configured", name)
		}
		if route.MaxAttempts == 0 {
			route.MaxAttempts = 1
			routes[name] = route
		}
		if route.MaxTotalTokens < 0 || route.MaxTotalTokens > 1_000_000_000_000 {
			return fmt.Errorf("route %q max_total_tokens must be between 0 and 1000000000000", name)
		}
		for _, reason := range route.FallbackOn {
			if !allowedFallbacks[reason] {
				return fmt.Errorf("route %q fallback reason %q is unsupported", name, reason)
			}
		}
	}
	return nil
}

func RenderPrompt(command ResolvedCommand, prompt string) (ResolvedCommand, error) {
	if strings.TrimSpace(prompt) == "" {
		return ResolvedCommand{}, errors.New("prompt is required")
	}
	if len(prompt) > maxInputPromptBytes {
		return ResolvedCommand{}, fmt.Errorf("prompt exceeds %d bytes", maxInputPromptBytes)
	}
	parameterCount := strings.Count(command.Prompt, promptParameter)
	if parameterCount == 0 {
		return ResolvedCommand{}, fmt.Errorf("command %q prompt must include %s", command.Name, promptParameter)
	}
	literalBytes := len(command.Prompt) - parameterCount*len(promptParameter)
	if literalBytes > maxRenderedPromptBytes || len(prompt) > (maxRenderedPromptBytes-literalBytes)/parameterCount {
		return ResolvedCommand{}, fmt.Errorf("rendered command prompt exceeds %d bytes", maxRenderedPromptBytes)
	}
	command.Prompt = strings.ReplaceAll(command.Prompt, promptParameter, prompt)
	return command, nil
}

func validatePromptParameters(commandName, prompt string) error {
	if strings.Contains(prompt, legacyFactoryPrefix) {
		return fmt.Errorf("command %q prompt uses the unsupported legacy Factory parameter namespace", commandName)
	}
	hasPrompt := false
	remaining := prompt
	for {
		start := strings.Index(remaining, machinistParameterPrefix)
		if start < 0 {
			break
		}
		remaining = remaining[start:]
		end := strings.Index(remaining, "}}")
		if end < 0 {
			return fmt.Errorf("command %q prompt contains a malformed Machinist parameter", commandName)
		}
		parameter := remaining[:end+2]
		if parameter != promptParameter {
			return fmt.Errorf("command %q prompt uses unsupported Machinist parameter %q", commandName, parameter)
		}
		hasPrompt = true
		remaining = remaining[end+2:]
	}
	if !hasPrompt {
		return fmt.Errorf("command %q prompt must include %s", commandName, promptParameter)
	}
	return nil
}

func applyWorkerDefaults(worker Worker) (Worker, error) {
	return applyWorkerDefaultsWithHostname(worker, os.Hostname)
}

func applyWorkerDefaultsWithHostname(worker Worker, getHostname func() (string, error)) (Worker, error) {
	worker.Name = strings.TrimSpace(worker.Name)
	if worker.Name == "" {
		hostname, err := getHostname()
		if err != nil {
			return Worker{}, fmt.Errorf("find machine hostname: %w", err)
		}
		worker.Name = strings.TrimSpace(hostname)
		if worker.Name == "" {
			return Worker{}, errors.New("find machine hostname: hostname is empty")
		}
	}
	if worker.DataDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Worker{}, fmt.Errorf("find user home directory: %w", err)
		}
		worker.DataDirectory = filepath.Join(home, filepath.FromSlash(defaultWorkerDirName))
	}
	dataDirectory, err := expandHome(worker.DataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	if !filepath.IsAbs(dataDirectory) && worker.configDir != "" {
		dataDirectory = filepath.Join(worker.configDir, dataDirectory)
	}
	worker.DataDirectory, err = filepath.Abs(dataDirectory)
	if err != nil {
		return Worker{}, fmt.Errorf("resolve worker data directory: %w", err)
	}
	worker.DataDirectory = filepath.Clean(worker.DataDirectory)
	if err := applyWorkerTelemetryDefaults(&worker); err != nil {
		return Worker{}, err
	}
	worker.Environment.Tags = normaliseEnvironmentTags(worker.Environment.Tags)
	if len(worker.Environment.Tags) > 32 {
		return Worker{}, errors.New("environment tags cannot exceed 32 items")
	}
	for _, tag := range worker.Environment.Tags {
		if len(tag) > 64 || !safeEnvironmentTag(tag) {
			return Worker{}, fmt.Errorf("environment tag %q is invalid", tag)
		}
	}
	for name, repository := range worker.Repositories {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(repository.Path) == "" {
			return Worker{}, errors.New("repository names and paths must be non-empty")
		}
		path, err := resolveConfigPath(repository.Path, worker.configDir)
		if err != nil {
			return Worker{}, fmt.Errorf("resolve repository %q: %w", name, err)
		}
		repository.Path = path
		worker.Repositories[name] = repository
	}
	for name, executor := range worker.Executors {
		if _, duplicate := worker.Profiles[name]; duplicate {
			return Worker{}, fmt.Errorf("execution name %q is configured as both an executor and a profile", name)
		}
		if err := validateCommand(name, executor.Command); err != nil {
			return Worker{}, err
		}
		for alias, model := range executor.Models {
			if strings.TrimSpace(alias) == "" || strings.TrimSpace(model) == "" {
				return Worker{}, fmt.Errorf("executor %q model aliases and values must be non-empty", name)
			}
		}
		if len(executor.Models) > 0 && !executor.supportsModel() {
			return Worker{}, fmt.Errorf("executor %q defines models but its command does not contain %s", name, modelParameter)
		}
		if err := validateHerdrAdapter(name, executor.HerdrAgent, executor.HerdrArgs, executor.HerdrCommand, executor.Models); err != nil {
			return Worker{}, err
		}
		executor.HerdrAgent = strings.ToLower(strings.TrimSpace(executor.HerdrAgent))
		worker.Executors[name] = executor
	}
	for name, profile := range worker.Profiles {
		if err := validateProfile(name, profile); err != nil {
			return Worker{}, err
		}
		profile.Harness = strings.ToLower(strings.TrimSpace(profile.Harness))
		profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
		profile.AuthMode = strings.ToLower(strings.TrimSpace(profile.AuthMode))
		profile.SecretEnv = strings.TrimSpace(profile.SecretEnv)
		profile.BaseURLEnv = strings.TrimSpace(profile.BaseURLEnv)
		profile.HerdrAgent = strings.ToLower(strings.TrimSpace(profile.HerdrAgent))
		profile.RequiresExecutables = normaliseExecutables(profile.RequiresExecutables)
		profile.RequiresOS = normaliseEnvironmentTags(profile.RequiresOS)
		profile.RequiresArch = normaliseEnvironmentTags(profile.RequiresArch)
		profile.RequiresTags = normaliseEnvironmentTags(profile.RequiresTags)
		worker.Profiles[name] = profile
	}
	return worker, nil
}

func applyWorkerTelemetryDefaults(worker *Worker) error {
	if !worker.Telemetry.Enabled {
		return nil
	}
	if strings.TrimSpace(worker.Telemetry.URL) == "" {
		worker.Telemetry.URL = "http://127.0.0.1:7900/api/v1/events"
	}
	parsed, err := url.Parse(worker.Telemetry.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "/api/v1/events" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("telemetry.url must be the collector's literal loopback http://127.0.0.1 URL ending in /api/v1/events")
	}
	for label, configured := range map[string]*string{
		"token_file": &worker.Telemetry.TokenFile, "identity_salt_file": &worker.Telemetry.IdentitySaltFile,
	} {
		if strings.TrimSpace(*configured) == "" {
			return fmt.Errorf("telemetry.%s is required when telemetry is enabled", label)
		}
		resolved, err := resolveConfigPath(*configured, worker.configDir)
		if err != nil {
			return fmt.Errorf("resolve telemetry.%s: %w", label, err)
		}
		*configured = resolved
	}
	worker.Telemetry.EndpointID = strings.ToLower(strings.TrimSpace(worker.Telemetry.EndpointID))
	if worker.Telemetry.EndpointID == "" || len(worker.Telemetry.EndpointID) > 64 || !safeEnvironmentTag(worker.Telemetry.EndpointID) {
		return errors.New("telemetry.endpoint_id must be a non-empty portable identifier")
	}
	return nil
}

func validateProfile(name string, profile Profile) error {
	if strings.TrimSpace(name) == "" || len(name) > 64 || !safeEnvironmentTag(strings.ToLower(strings.TrimSpace(name))) {
		return errors.New("profile names must be non-empty portable identifiers")
	}
	profile.Harness = strings.ToLower(strings.TrimSpace(profile.Harness))
	if profile.Harness == "" || len(profile.Harness) > 64 || !safeEnvironmentTag(profile.Harness) {
		return fmt.Errorf("profile %q harness must be a non-empty portable identifier", name)
	}
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	if profile.Provider != "" && (len(profile.Provider) > 64 || !safeEnvironmentTag(profile.Provider)) {
		return fmt.Errorf("profile %q provider is invalid", name)
	}
	profile.AuthMode = strings.ToLower(strings.TrimSpace(profile.AuthMode))
	if !slices.Contains([]string{"subscription", "api_key", "local"}, profile.AuthMode) {
		return fmt.Errorf("profile %q auth_mode must be subscription, api_key, or local", name)
	}
	if profile.AuthMode == "api_key" {
		if !safeEnvironmentVariable(strings.TrimSpace(profile.SecretEnv)) {
			return fmt.Errorf("profile %q api_key auth requires a valid secret_env name", name)
		}
	} else if strings.TrimSpace(profile.SecretEnv) != "" {
		return fmt.Errorf("profile %q secret_env is only valid with api_key auth", name)
	}
	if err := validateCommand(name, profile.Command); err != nil {
		return err
	}
	for alias, model := range profile.Models {
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(model) == "" {
			return fmt.Errorf("profile %q model aliases and values must be non-empty", name)
		}
	}
	if len(profile.Models) > 0 && !(Executor{Command: profile.Command}).supportsModel() {
		return fmt.Errorf("profile %q defines models but its command does not contain %s", name, modelParameter)
	}
	if err := validateHerdrAdapter(name, profile.HerdrAgent, profile.HerdrArgs, profile.HerdrCommand, profile.Models); err != nil {
		return err
	}
	if err := validateProfileEndpoint(name, profile); err != nil {
		return err
	}
	if len(profile.RequiresExecutables) > 32 {
		return fmt.Errorf("profile %q requires_executables cannot exceed 32 items", name)
	}
	for _, executable := range profile.RequiresExecutables {
		if strings.TrimSpace(executable) == "" || len(executable) > 512 || strings.ContainsAny(executable, "\x00\r\n") {
			return fmt.Errorf("profile %q requires_executables value %q is invalid", name, executable)
		}
	}
	for field, values := range map[string][]string{"requires_os": profile.RequiresOS, "requires_arch": profile.RequiresArch, "requires_tags": profile.RequiresTags} {
		normalised := normaliseEnvironmentTags(values)
		if len(normalised) > 32 {
			return fmt.Errorf("profile %q %s cannot exceed 32 items", name, field)
		}
		for _, value := range normalised {
			if len(value) > 64 || !safeEnvironmentTag(value) {
				return fmt.Errorf("profile %q %s value %q is invalid", name, field, value)
			}
		}
	}
	return nil
}

func validateHerdrAdapter(name, agent string, arguments, command []string, models map[string]string) error {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent != "" && len(command) > 0 {
		return fmt.Errorf("execution %q must configure only one of herdr_agent or herdr_command", name)
	}
	if agent != "" {
		if len(agent) > 64 || !safeEnvironmentTag(agent) {
			return fmt.Errorf("execution %q herdr_agent is invalid", name)
		}
		for _, argument := range arguments {
			if strings.ContainsAny(argument, "\x00\r\n") {
				return fmt.Errorf("execution %q herdr_args contain an invalid argument", name)
			}
			if strings.Contains(argument, machinistParameterPrefix) && !strings.Contains(argument, modelParameter) {
				return fmt.Errorf("execution %q herdr_args use an unsupported Machinist parameter", name)
			}
		}
		if len(models) > 0 && !slices.ContainsFunc(arguments, func(argument string) bool { return strings.Contains(argument, modelParameter) }) {
			return fmt.Errorf("execution %q defines models but herdr_args do not contain %s", name, modelParameter)
		}
	} else if len(arguments) > 0 {
		return fmt.Errorf("execution %q herdr_args require herdr_agent", name)
	}
	for _, argument := range command {
		if strings.ContainsAny(argument, "\x00\r\n") {
			return fmt.Errorf("execution %q herdr_command contains an invalid argument", name)
		}
		if strings.Contains(argument, machinistParameterPrefix) && !strings.Contains(argument, modelParameter) {
			return fmt.Errorf("execution %q herdr_command uses an unsupported Machinist parameter", name)
		}
	}
	if len(command) == 0 && command != nil {
		return fmt.Errorf("execution %q herdr_command must not be empty", name)
	}
	return nil
}

func validateProfileEndpoint(name string, profile Profile) error {
	baseURL := strings.TrimSpace(profile.BaseURL)
	baseURLEnv := strings.TrimSpace(profile.BaseURLEnv)
	if (baseURL == "") != (baseURLEnv == "") {
		return fmt.Errorf("profile %q base_url and base_url_env must be configured together", name)
	}
	if baseURL == "" {
		return nil
	}
	if !safeEnvironmentVariable(baseURLEnv) || strings.HasPrefix(baseURLEnv, "MACHINIST_") ||
		!(strings.HasSuffix(baseURLEnv, "_BASE_URL") || strings.HasSuffix(baseURLEnv, "_API_BASE") || strings.HasSuffix(baseURLEnv, "_HOST")) {
		return fmt.Errorf("profile %q base_url_env is invalid or reserved", name)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("profile %q base_url must be an http(s) origin without credentials, query, or fragment", name)
	}
	if parsed.Scheme == "http" && !loopbackHost(parsed.Hostname()) && !profile.AllowInsecureHTTP {
		return fmt.Errorf("profile %q remote http base_url requires allow_insecure_http = true", name)
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func safeEnvironmentVariable(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for index, character := range name {
		if (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func profileEnvironment(profile Profile) map[string]string {
	if profile.BaseURL == "" {
		return nil
	}
	return map[string]string{profile.BaseURLEnv: profile.BaseURL}
}

func normaliseEnvironmentTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	slices.Sort(result)
	return result
}

func normaliseExecutables(executables []string) []string {
	result := make([]string, 0, len(executables))
	seen := make(map[string]bool, len(executables))
	for _, executable := range executables {
		executable = strings.TrimSpace(executable)
		if executable != "" && !seen[executable] {
			seen[executable] = true
			result = append(result, executable)
		}
	}
	slices.Sort(result)
	return result
}

func safeEnvironmentTag(tag string) bool {
	for _, character := range tag {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func applyServerDefaults(server Server) (Server, error) {
	if server.Listen == "" {
		server.Listen = "127.0.0.1:7331"
	}
	if server.Database == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Server{}, fmt.Errorf("find user home directory: %w", err)
		}
		server.Database = filepath.Join(home, filepath.FromSlash(defaultServerDirName), "machinist.db")
	} else {
		path, err := resolveConfigPath(server.Database, server.configDir)
		if err != nil {
			return Server{}, fmt.Errorf("resolve database: %w", err)
		}
		server.Database = path
	}
	if strings.TrimSpace(server.WorkerTokenFile) == "" {
		return Server{}, errors.New("worker_token_file is required")
	}
	if server.MaxConcurrentJobs != nil && *server.MaxConcurrentJobs <= 0 {
		return Server{}, errors.New("max_concurrent_jobs must be positive")
	}
	tokenPath, err := resolveConfigPath(server.WorkerTokenFile, server.configDir)
	if err != nil {
		return Server{}, fmt.Errorf("resolve worker token file: %w", err)
	}
	server.WorkerTokenFile = tokenPath
	return server, nil
}

func applyObservabilityDefaults(observability *Observability) error {
	if !observability.Enabled {
		observability.URL = ""
		return nil
	}
	if strings.TrimSpace(observability.URL) == "" {
		observability.URL = "http://127.0.0.1:7900"
	}
	parsed, err := url.Parse(observability.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || (parsed.Path != "" && parsed.Path != "/") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("observability.url must be a literal loopback http://127.0.0.1 origin")
	}
	observability.URL = strings.TrimSuffix(parsed.String(), "/")
	return nil
}

func validateCommand(name string, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("executor %q must define a non-empty command", name)
	}
	for index, argument := range command {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("executor %q command argument %d contains a null byte", name, index)
		}
		if strings.Contains(argument, legacyFactoryPrefix) {
			return fmt.Errorf("executor %q command uses the unsupported legacy Factory parameter namespace", name)
		}
		if index == 0 && strings.Contains(argument, modelParameter) {
			return fmt.Errorf("executor %q command executable cannot contain %s", name, modelParameter)
		}
		if strings.Contains(argument, modelParameter) && (!strings.HasPrefix(argument, "--") || !strings.HasSuffix(argument, "="+modelParameter) || strings.Count(argument, modelParameter) != 1) {
			return fmt.Errorf("executor %q must use %s as a complete optional --flag=%s argument", name, modelParameter, modelParameter)
		}
	}
	return nil
}

func resolveConfigPath(path, base string) (string, error) {
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func readToken(path string) (string, error) {
	body, err := readBoundedFile(path, maxTokenBytes)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func defaultConfigPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".machinist", name), nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))), nil
	}
	return path, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func commandHash(command ResolvedCommand) (string, error) {
	payload, err := json.Marshal(struct {
		Name           string        `json:"name"`
		Executor       string        `json:"executor"`
		Profile        string        `json:"profile,omitempty"`
		Route          string        `json:"route,omitempty"`
		Candidates     []string      `json:"candidates,omitempty"`
		MaxAttempts    int           `json:"max_attempts,omitempty"`
		MaxTotalTokens int64         `json:"max_total_tokens,omitempty"`
		FallbackOn     []string      `json:"fallback_on,omitempty"`
		Role           string        `json:"role,omitempty"`
		Prompt         string        `json:"prompt"`
		Timeout        time.Duration `json:"timeout"`
	}{
		Name: command.Name, Executor: command.Executor, Profile: command.Profile,
		Route: command.Route, Candidates: command.Candidates, MaxAttempts: command.MaxAttempts,
		MaxTotalTokens: command.MaxTotalTokens, FallbackOn: command.FallbackOn,
		Role: command.Role, Prompt: command.Prompt, Timeout: command.Timeout,
	})
	if err != nil {
		return "", fmt.Errorf("encode command definition: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
