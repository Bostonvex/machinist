package managedworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/environment"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/herdr"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
	"github.com/owainlewis/machinist/internal/runner"
)

type Worker struct {
	config         config.Worker
	instanceID     string
	client         *Client
	stdout         io.Writer
	stderr         io.Writer
	heartbeatTicks <-chan time.Time
	executeRun     func(context.Context, protocol.RunSpec) protocol.Completion
	transport      string
	herdrClient    *herdr.Client
}

const heartbeatInterval = 10 * time.Second

func New(workerConfig config.Worker, stdout, stderr io.Writer) (*Worker, error) {
	return NewForTransport(workerConfig, stdout, stderr, "process")
}

func NewForTransport(workerConfig config.Worker, stdout, stderr io.Writer, transport string) (*Worker, error) {
	if strings.TrimSpace(workerConfig.Name) == "" {
		return nil, errors.New("worker name is required")
	}
	if len(workerConfig.Executors)+len(workerConfig.Profiles) == 0 || len(workerConfig.Repositories) == 0 {
		return nil, errors.New("managed worker requires at least one executor or profile and a repository")
	}
	client, err := NewClient(workerConfig)
	if err != nil {
		return nil, err
	}
	instanceID, err := randomID("worker", 16)
	if err != nil {
		return nil, err
	}
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport != "process" && transport != "herdr" {
		return nil, fmt.Errorf("worker transport must be process or herdr")
	}
	var herdrClient *herdr.Client
	if transport == "herdr" {
		herdrClient, err = herdr.NewFromEnvironment()
		if err != nil {
			return nil, err
		}
	}
	return &Worker{
		config:      workerConfig,
		instanceID:  instanceID,
		client:      client,
		stdout:      stdout,
		stderr:      stderr,
		transport:   transport,
		herdrClient: herdrClient,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		run, err := w.poll(ctx)
		if err != nil {
			if !wait(ctx, time.Second) {
				return nil
			}
			fmt.Fprintf(w.stderr, "machinist: worker poll: %v\n", err)
			continue
		}
		if run == nil {
			if !wait(ctx, time.Second) {
				return nil
			}
			continue
		}
		completion := w.executeWithHeartbeats(ctx, *run)
		if err := w.deliverWithHeartbeats(ctx, *run, completion); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var responseErr *ResponseError
			if errors.As(err, &responseErr) && responseErr.Status == 409 {
				fmt.Fprintf(w.stderr, "machinist: report run %s: %v\n", run.ID, err)
				continue
			}
			return err
		}
	}
}

func (w *Worker) executeWithHeartbeats(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	execute := w.executeRun
	if execute == nil {
		execute = w.execute
	}
	return withHeartbeats(ctx, w, spec, "", cancel, func() protocol.Completion { return execute(runContext, spec) })
}

func (w *Worker) deliverWithHeartbeats(ctx context.Context, spec protocol.RunSpec, completion protocol.Completion) error {
	if _, err := w.heartbeat(ctx, spec); err != nil {
		fmt.Fprintf(w.stderr, "machinist: heartbeat run %s before completion: %v\n", spec.ID, err)
	}
	return withHeartbeats(ctx, w, spec, " during completion", nil, func() error { return w.deliver(ctx, spec.ID, completion) })
}

// withHeartbeats runs work in the background and keeps the run lease alive
// until it returns. Cancellation does not abandon the work; the work observes
// ctx itself and its result is always returned.
func withHeartbeats[T any](ctx context.Context, w *Worker, spec protocol.RunSpec, phase string, cancelWork context.CancelFunc, work func() T) T {
	ticks := w.heartbeatTicks
	if ticks == nil {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	done := make(chan T, 1)
	go func() { done <- work() }()
	for {
		select {
		case result := <-done:
			return result
		case <-ticks:
			result, err := w.heartbeat(ctx, spec)
			if err != nil {
				fmt.Fprintf(w.stderr, "machinist: heartbeat run %s%s: %v\n", spec.ID, phase, err)
			} else if result.CancelRequested && cancelWork != nil {
				cancelWork()
				cancelWork = nil
			}
		case <-ctx.Done():
			return <-done
		}
	}
}

func (w *Worker) poll(ctx context.Context) (*protocol.RunSpec, error) {
	transport := w.transport
	if transport == "" {
		transport = "process"
	}
	var workerEnvironment environment.Manifest
	if w.config.Environment.DetectionEnabled() {
		workerEnvironment = environment.Detect(w.config.Environment.Tags)
	}
	capabilities := harness.Inspect(w.config, workerEnvironment)
	if transport == "herdr" {
		interactiveExecutors := make([]string, 0, len(capabilities.Executors))
		for _, name := range capabilities.Executors {
			profile, profileOK := w.config.Profiles[name]
			executor, executorOK := w.config.Executors[name]
			if (profileOK && hasHerdrAdapter(profile.HerdrAgent, profile.HerdrCommand)) || (executorOK && hasHerdrAdapter(executor.HerdrAgent, executor.HerdrCommand)) {
				interactiveExecutors = append(interactiveExecutors, name)
			}
		}
		capabilities.Executors = interactiveExecutors
		for name := range capabilities.Models {
			profile := w.config.Profiles[name]
			executor := w.config.Executors[name]
			if !hasHerdrAdapter(profile.HerdrAgent, profile.HerdrCommand) && !hasHerdrAdapter(executor.HerdrAgent, executor.HerdrCommand) {
				delete(capabilities.Models, name)
			}
		}
		for name := range capabilities.Profiles {
			profile := w.config.Profiles[name]
			if !hasHerdrAdapter(profile.HerdrAgent, profile.HerdrCommand) {
				delete(capabilities.Profiles, name)
			}
		}
	}
	request := protocol.PollRequest{
		InstanceID:   w.instanceID,
		Name:         w.config.Name,
		Executors:    capabilities.Executors,
		Repositories: w.config.RepositoryNames(),
		Models:       capabilities.Models,
		Profiles:     profileCapabilities(capabilities.Profiles),
		Environment:  workerEnvironment,
		Transports:   []string{transport},
	}
	var response protocol.PollResponse
	if err := w.client.Post(ctx, "/api/v1/workers/poll", request, &response); err != nil {
		return nil, err
	}
	return response.Run, nil
}

func hasHerdrAdapter(agent string, command []string) bool {
	return strings.TrimSpace(agent) != "" || len(command) > 0
}

func profileCapabilities(configured map[string]harness.Capability) map[string]protocol.ProfileCapability {
	capabilities := make(map[string]protocol.ProfileCapability, len(configured))
	for name, profile := range configured {
		capabilities[name] = protocol.ProfileCapability{
			Harness: profile.Harness, Provider: profile.Provider,
			AuthMode: profile.AuthMode, Models: profile.Models,
			Available: profile.Available, Reason: profile.Reason,
		}
	}
	return capabilities
}

func (w *Worker) execute(ctx context.Context, spec protocol.RunSpec) protocol.Completion {
	completion := protocol.Completion{InstanceID: w.instanceID, LeaseToken: spec.LeaseToken, AttemptID: spec.AttemptID, State: "failed", ExitCode: 1}
	repository, err := w.config.ResolveRepository(spec.Repository)
	if err != nil {
		completion.Error = err.Error()
		completion.ErrorClass = "configuration"
		return completion
	}
	command, err := w.config.ResolveCommandModel(config.ResolvedCommand{
		Name:       spec.Command,
		Executor:   spec.Executor,
		Profile:    spec.Profile,
		Harness:    spec.Harness,
		Provider:   spec.Provider,
		AuthMode:   spec.AuthMode,
		Role:       spec.Role,
		Prompt:     spec.RenderedPrompt,
		Timeout:    time.Duration(spec.TimeoutMillis) * time.Millisecond,
		Definition: "control-plane",
		Hash:       spec.CommandHash,
	}, spec.Model)
	if err != nil {
		completion.Error = err.Error()
		completion.ErrorClass = "configuration"
		return completion
	}
	if command.Environment == nil {
		command.Environment = make(map[string]string)
	}
	command.Prompt = retryPrompt(command.Prompt, spec)
	for name, value := range w.config.TelemetryEnvironment() {
		command.Environment[name] = value
	}
	for name, value := range agentEnvironment(spec, command, w.instanceID, w.controlPlaneURL()) {
		command.Environment[name] = value
	}
	if w.config.Environment.DetectionEnabled() {
		command.Environment["MACHINIST_ENVIRONMENT_DIGEST"] = environment.Detect(w.config.Environment.Tags).Digest
	}
	if spec.ExecutionMode == "herdr" {
		if w.transport != "herdr" || w.herdrClient == nil {
			completion.Error = "Herdr execution was assigned to a non-Herdr worker"
			completion.ErrorClass = "configuration"
			return completion
		}
		interactive, runErr := w.herdrClient.Execute(ctx, spec, command, repository, func(binding protocol.TerminalBinding) error {
			return w.bindTerminal(ctx, spec, binding)
		})
		interactive.InstanceID = w.instanceID
		interactive.LeaseToken = spec.LeaseToken
		interactive.AttemptID = spec.AttemptID
		if interactive.State == "" {
			interactive.State = "failed"
			interactive.ExitCode = 1
			interactive.ErrorClass = "transport"
		}
		if runErr != nil && interactive.Error == "" {
			interactive.Error = runErr.Error()
		}
		return interactive
	}
	result, runErr := runner.Execute(ctx, runner.Options{
		RunID:         spec.ID,
		ArtifactKey:   spec.LeaseToken,
		Command:       command,
		Repository:    repository,
		DataDirectory: w.config.DataDirectory,
		Stdout:        w.stdout,
		Stderr:        w.stderr,
	})
	if result.ID != "" {
		completion.State = string(result.State)
		completion.ExitCode = result.ExitCode
		completion.Result, _ = os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
		if events, readErr := os.ReadFile(result.EventsPath); readErr == nil {
			completion.Events = string(events)
		}
	}
	if runErr != nil {
		completion.Error = runErr.Error()
		completion.ErrorClass = classifyExecutionError(result.State, runErr)
	}
	return completion
}

func (w *Worker) bindTerminal(ctx context.Context, spec protocol.RunSpec, binding protocol.TerminalBinding) error {
	return w.client.Post(ctx, "/api/v1/runs/"+url.PathEscape(spec.ID)+"/terminal", protocol.BindTerminalRequest{
		InstanceID: w.instanceID, LeaseToken: spec.LeaseToken, AttemptID: spec.AttemptID, Terminal: binding,
	}, nil)
}

func retryPrompt(prompt string, spec protocol.RunSpec) string {
	if spec.AttemptNumber <= 1 || spec.PreviousErrorClass == "" {
		return prompt
	}
	budget := ""
	if spec.MaxTotalTokens > 0 {
		budget = fmt.Sprintf(" Total reported-token budget: %d.", spec.MaxTotalTokens)
	}
	return fmt.Sprintf("%s\n\n[Machinist retry handoff]\nAttempt %d of %d. Previous failure class: %s.%s Check existing repository state before repeating work or external side effects.", prompt, spec.AttemptNumber, spec.MaxAttempts, spec.PreviousErrorClass, budget)
}

func optionalPositiveInt(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func classifyExecutionError(state runner.State, err error) string {
	switch state {
	case runner.StateTimedOut:
		return "timeout"
	case runner.StateCancelled:
		return "cancelled"
	}
	var runtimeError *runner.RuntimeError
	if errors.As(err, &runtimeError) {
		return "harness_crash"
	}
	return "test_failure"
}

func (w *Worker) deliver(ctx context.Context, runID string, completion protocol.Completion) error {
	backoff := 250 * time.Millisecond
	for {
		err := w.client.Post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/complete", completion, nil)
		if err == nil {
			return nil
		}
		var responseErr *ResponseError
		if errors.As(err, &responseErr) && !responseErr.Retryable() {
			return err
		}
		fmt.Fprintf(w.stderr, "machinist: report run %s: %v\n", runID, err)
		if !wait(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

func (w *Worker) heartbeat(ctx context.Context, spec protocol.RunSpec) (protocol.HeartbeatResponse, error) {
	var response protocol.HeartbeatResponse
	err := w.client.Post(ctx, "/api/v1/runs/"+url.PathEscape(spec.ID)+"/heartbeat", protocol.Heartbeat{
		InstanceID: w.instanceID,
		LeaseToken: spec.LeaseToken,
	}, &response)
	return response, err
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func randomID(prefix string, byteCount int) (string, error) {
	body := make([]byte, byteCount)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(body), nil
}

// controlPlaneURL is the address the control plane answers on, or empty when
// this worker has no client to answer through. A reviewer with no address is
// told nothing rather than told a wrong one.
func (w *Worker) controlPlaneURL() string {
	if w.client == nil {
		return ""
	}
	return w.client.base
}

// agentEnvironment is what the agent process is told about the run it is
// serving. Empty values are left out rather than exported blank, so an agent
// can tell "not set" from "set to nothing".
//
// A reviewer is the one role that also gets the run's lease. It is the only
// role that has to answer the control plane rather than merely report to it:
// its verdict goes to the review route, which authenticates the submitting run
// by that lease. Every other role is given no credential, because no other role
// has anything to say back — and a credential handed out for no reason is one
// more thing that can be misused.
func agentEnvironment(spec protocol.RunSpec, command config.ResolvedCommand, instanceID, controlPlaneURL string) map[string]string {
	values := map[string]string{
		"MACHINIST_JOB_ID": spec.JobID, "MACHINIST_ATTEMPT_ID": spec.AttemptID,
		"MACHINIST_COMMAND": spec.Command, "MACHINIST_ROLE": spec.Role,
		"MACHINIST_ROUTE": spec.Route, "MACHINIST_PROFILE": command.Profile,
		"MACHINIST_HARNESS": command.Harness, "MACHINIST_PROVIDER": command.Provider,
		"MACHINIST_MODEL": command.Model, "MACHINIST_WORKER_INSTANCE": instanceID,
		"MACHINIST_ATTEMPT_NUMBER":       strconv.Itoa(spec.AttemptNumber),
		"MACHINIST_MAX_ATTEMPTS":         strconv.Itoa(spec.MaxAttempts),
		"MACHINIST_MAX_TOTAL_TOKENS":     optionalPositiveInt(spec.MaxTotalTokens),
		"MACHINIST_PREVIOUS_ERROR_CLASS": spec.PreviousErrorClass,
	}
	if normalizeRole(spec.Role) == review.RoleReviewer {
		values["MACHINIST_RUN_ID"] = spec.ID
		values["MACHINIST_LEASE_TOKEN"] = spec.LeaseToken
		values["MACHINIST_CONTROL_PLANE_URL"] = controlPlaneURL
	}
	for name, value := range values {
		if value == "" {
			delete(values, name)
		}
	}
	return values
}

// normalizeRole matches roles the way the control plane does, so a role spelled
// with different case or padding is still the role it names.
func normalizeRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}
