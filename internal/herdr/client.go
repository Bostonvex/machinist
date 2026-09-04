package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

// Client drives the Herdr session inherited by the interactive worker. Keeping
// this worker inside a Herdr plugin process avoids guessing which user session
// owns a pane and gives every call an explicit socket.
type Client struct {
	Binary      string
	SocketPath  string
	Environment []string
}

type workspaceResponse struct {
	Result struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	} `json:"result"`
}

type agentResponse struct {
	Result json.RawMessage `json:"result"`
}

func NewFromEnvironment() (*Client, error) {
	if os.Getenv("HERDR_ENV") != "1" {
		return nil, errors.New("Herdr transport requires a worker started inside a Herdr plugin session (HERDR_ENV=1)")
	}
	socket := strings.TrimSpace(os.Getenv("HERDR_SOCKET_PATH"))
	if socket == "" {
		return nil, errors.New("Herdr transport requires HERDR_SOCKET_PATH")
	}
	binary := strings.TrimSpace(os.Getenv("HERDR_BIN_PATH"))
	if binary == "" {
		binary = "herdr"
	}
	return &Client{Binary: binary, SocketPath: socket, Environment: os.Environ()}, nil
}

func (client *Client) Execute(ctx context.Context, spec protocol.RunSpec, command config.ResolvedCommand, repository string, onBinding func(protocol.TerminalBinding) error) (protocol.Completion, error) {
	started := time.Now().UTC()
	if strings.TrimSpace(command.HerdrAgent) == "" && len(command.HerdrCommand) == 0 {
		err := fmt.Errorf("profile %q does not configure herdr_agent or herdr_command", command.Profile)
		return failure(started, protocol.TerminalBinding{}, "configure Herdr adapter", err), err
	}
	usageFile, err := os.CreateTemp("", "machinist-herdr-usage-*")
	if err != nil {
		return failure(started, protocol.TerminalBinding{}, "create Herdr usage file", err), err
	}
	usagePath := usageFile.Name()
	_ = usageFile.Close()
	defer os.Remove(usagePath)
	if command.Environment == nil {
		command.Environment = make(map[string]string)
	}
	command.Environment["MACHINIST_TOKEN_USAGE_PATH"] = usagePath
	environment := sanitizedEnvironment(client.Environment, command.DeniedEnvironment)
	for key, value := range command.Environment {
		environment = append(environment, key+"="+value)
	}
	workspaceArgs := []string{"workspace", "create", "--cwd", repository, "--label", compactLabel(spec), "--no-focus"}
	profileEnvironmentNames := make([]string, 0, len(command.Environment))
	for key := range command.Environment {
		profileEnvironmentNames = append(profileEnvironmentNames, key)
	}
	sort.Strings(profileEnvironmentNames)
	for _, key := range profileEnvironmentNames {
		workspaceArgs = append(workspaceArgs, "--env", key+"="+command.Environment[key])
	}
	for _, key := range command.DeniedEnvironment {
		workspaceArgs = append(workspaceArgs, "--env", key+"=")
	}
	var created workspaceResponse
	if err := client.call(ctx, environment, &created, workspaceArgs...); err != nil {
		return failure(started, protocol.TerminalBinding{}, "create Herdr workspace", err), err
	}
	binding := protocol.TerminalBinding{
		Session: sessionName(client.SocketPath), WorkspaceID: created.Result.Workspace.WorkspaceID,
		TabID: created.Result.Tab.TabID, PaneID: created.Result.RootPane.PaneID,
		AgentName: agentName(spec.AttemptID),
	}
	if binding.WorkspaceID == "" || binding.PaneID == "" {
		err := errors.New("Herdr workspace response omitted workspace or pane ID")
		return failure(started, binding, "create Herdr workspace", err), err
	}
	if err := onBinding(binding); err != nil {
		return failure(started, binding, "record Herdr terminal binding", err), err
	}
	waitTarget := binding.AgentName
	if command.HerdrAgent != "" {
		startArgs := []string{"agent", "start", binding.AgentName, "--kind", command.HerdrAgent, "--pane", binding.PaneID, "--timeout", "300000"}
		if len(command.HerdrArgs) > 0 {
			startArgs = append(startArgs, "--")
			startArgs = append(startArgs, command.HerdrArgs...)
		}
		if err := client.call(ctx, environment, &agentResponse{}, startArgs...); err != nil {
			return failure(started, binding, "start Herdr agent", err), err
		}
	} else {
		if err := client.call(ctx, environment, &agentResponse{}, "pane", "run", binding.PaneID, shellCommand(command.HerdrCommand)); err != nil {
			return failure(started, binding, "start Herdr process", err), err
		}
		if err := client.waitForReportedAgent(ctx, environment, binding.PaneID, 5*time.Minute); err != nil {
			return failure(started, binding, "detect reported Herdr agent", err), err
		}
		if err := client.call(ctx, environment, &agentResponse{}, "agent", "rename", binding.PaneID, binding.AgentName); err != nil {
			return failure(started, binding, "name reported Herdr agent", err), err
		}
		waitTarget = binding.PaneID
	}
	var state string
	if command.HerdrAgent != "" {
		timeout := remainingMillis(ctx, command.Timeout, started)
		var prompted agentResponse
		if err := client.call(ctx, environment, &prompted, "agent", "prompt", binding.AgentName, command.Prompt, "--wait", "--timeout", timeout); err != nil {
			if ctx.Err() != nil {
				client.Interrupt(binding)
				err = ctx.Err()
			}
			return failure(started, binding, "run Herdr agent", err), err
		}
		state = responseStatus(prompted.Result)
	} else {
		if err := client.call(ctx, environment, &agentResponse{}, "pane", "run", binding.PaneID, command.Prompt); err != nil {
			return failure(started, binding, "prompt reported Herdr agent", err), err
		}
		// Ink-based TUIs can accept Herdr's atomic paste before their Enter
		// handler has settled. Give lifecycle reporting one poll interval, then
		// send a separate Enter only when the turn has not started.
		select {
		case <-ctx.Done():
			client.Interrupt(binding)
			return failure(started, binding, "prompt reported Herdr agent", ctx.Err()), ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		var afterPrompt agentResponse
		promptState := ""
		if err := client.call(ctx, environment, &afterPrompt, "agent", "get", binding.PaneID); err == nil {
			promptState = responseStatus(afterPrompt.Result)
		}
		if promptState != "working" && promptState != "blocked" {
			if err := client.call(ctx, environment, &agentResponse{}, "pane", "send-keys", binding.PaneID, "enter"); err != nil {
				return failure(started, binding, "submit reported Herdr agent prompt", err), err
			}
		}
		var active agentResponse
		if err := client.call(ctx, environment, &active, "agent", "wait", binding.PaneID, "--until", "working", "--until", "blocked", "--timeout", "30000"); err != nil {
			return failure(started, binding, "wait for reported Herdr agent to start", err), err
		}
		state = responseStatus(active.Result)
		if state == "working" {
			var settled agentResponse
			timeout := remainingMillis(ctx, command.Timeout, started)
			if err := client.call(ctx, environment, &settled, "agent", "wait", binding.PaneID, "--until", "idle", "--until", "done", "--until", "blocked", "--timeout", timeout); err != nil {
				return failure(started, binding, "wait for reported Herdr agent", err), err
			}
			state = responseStatus(settled.Result)
		}
	}
	if state == "blocked" {
		timeout := remainingMillis(ctx, command.Timeout, started)
		var waited agentResponse
		if err := client.call(ctx, environment, &waited, "agent", "wait", waitTarget, "--until", "idle", "--until", "done", "--timeout", timeout); err != nil {
			if ctx.Err() != nil {
				client.Interrupt(binding)
				err = ctx.Err()
			}
			return failure(started, binding, "wait for operator response", err), err
		}
		state = responseStatus(waited.Result)
	}
	if state != "idle" && state != "done" {
		if state == "" {
			state = "missing"
		}
		err := fmt.Errorf("Herdr agent settled in %q", state)
		return failure(started, binding, "run Herdr agent", err), err
	}
	completed := time.Now().UTC()
	resultFields := map[string]any{
		"id": spec.ID, "command": spec.Command, "command_hash": spec.CommandHash,
		"profile": command.Profile, "harness": command.Harness, "provider": command.Provider,
		"auth_mode": command.AuthMode, "role": command.Role, "repository": repository,
		"state": "succeeded", "exit_code": 0, "started_at": started, "completed_at": completed,
		"duration_millis": completed.Sub(started).Milliseconds(), "transport": "herdr", "terminal": binding,
	}
	if tokenUsage := readTokenUsage(usagePath); tokenUsage != nil {
		resultFields["token_usage"] = *tokenUsage
	}
	result, _ := json.Marshal(resultFields)
	events := eventJSONL(1, started, "herdr.workspace.created", binding, "") + eventJSONL(2, completed, "herdr.agent.settled", binding, state)
	return protocol.Completion{State: "succeeded", ExitCode: 0, Result: result, Events: events}, nil
}

func (client *Client) waitForReportedAgent(ctx context.Context, environment []string, paneID string, timeout time.Duration) error {
	startup, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var response agentResponse
		err := client.call(startup, environment, &response, "agent", "get", paneID)
		if err == nil {
			state := responseStatus(response.Result)
			if state == "idle" || state == "done" {
				return nil
			}
			if state == "blocked" {
				return errors.New("reported agent is blocked during startup")
			}
			waitMillis := remainingContextMillis(startup)
			var waited agentResponse
			if err := client.call(startup, environment, &waited, "agent", "wait", paneID, "--until", "idle", "--until", "done", "--until", "blocked", "--timeout", waitMillis); err != nil {
				return err
			}
			state = responseStatus(waited.Result)
			if state == "idle" || state == "done" {
				return nil
			}
			return fmt.Errorf("reported agent settled in %q during startup", state)
		}
		select {
		case <-startup.Done():
			return fmt.Errorf("wait for reported agent in pane %q: %w", paneID, startup.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func remainingContextMillis(ctx context.Context) string {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < time.Second {
			remaining = time.Second
		}
		return strconv.FormatInt(remaining.Milliseconds(), 10)
	}
	return "300000"
}

func shellCommand(arguments []string) string {
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if runtime.GOOS == "windows" {
			quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "''")+"'")
		} else {
			quoted = append(quoted, "'"+strings.ReplaceAll(argument, "'", "'\\''")+"'")
		}
	}
	return strings.Join(quoted, " ")
}

func (client *Client) Interrupt(binding protocol.TerminalBinding) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := binding.PaneID
	if target == "" {
		target = binding.AgentName
	}
	_ = client.call(ctx, client.Environment, nil, "agent", "send-keys", target, "ctrl+c")
}

func readTokenUsage(path string) *int64 {
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 64 {
		return nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

func (client *Client) call(ctx context.Context, environment []string, output any, arguments ...string) error {
	command := exec.CommandContext(ctx, client.Binary, arguments...)
	command.Env = append(environment, "HERDR_SOCKET_PATH="+client.SocketPath, "HERDR_ENV=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	if output != nil && stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), output); err != nil {
			return fmt.Errorf("decode Herdr response: %w", err)
		}
	}
	return nil
}

func failure(started time.Time, binding protocol.TerminalBinding, phase string, err error) protocol.Completion {
	completed := time.Now().UTC()
	state, code, class := "failed", 1, "transport"
	if errors.Is(err, context.Canceled) {
		state, code, class = "cancelled", 130, "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		state, code, class = "timed_out", 124, "timeout"
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		state, code, class = "timed_out", 124, "timeout"
	}
	result, _ := json.Marshal(map[string]any{"state": state, "exit_code": code, "started_at": started, "completed_at": completed, "duration_millis": completed.Sub(started).Milliseconds(), "transport": "herdr", "terminal": binding})
	return protocol.Completion{State: state, ExitCode: code, ErrorClass: class, Error: phase + ": " + err.Error(), Result: result, Events: eventJSONL(1, completed, "herdr.agent.failed", binding, err.Error())}
}

func responseStatus(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findStatus(value)
}

func findStatus(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"status", "agent_status", "state"} {
			if text, ok := typed[key].(string); ok && (text == "idle" || text == "working" || text == "blocked" || text == "done" || text == "unknown") {
				return text
			}
		}
		for _, child := range typed {
			if found := findStatus(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findStatus(child); found != "" {
				return found
			}
		}
	}
	return ""
}

func compactLabel(spec protocol.RunSpec) string {
	label := "Machinist " + strings.TrimPrefix(spec.JobID, "job_")
	if len(label) > 32 {
		label = label[:32]
	}
	return label
}

func agentName(attemptID string) string {
	value := strings.TrimPrefix(attemptID, "attempt_")
	if len(value) > 20 {
		value = value[:20]
	}
	return "machinist_" + value
}

func sessionName(socket string) string {
	parent := filepath.Base(filepath.Dir(socket))
	if parent == "herdr" || parent == ".config" || parent == "." {
		return "default"
	}
	return parent
}

func remainingMillis(ctx context.Context, configured time.Duration, started time.Time) string {
	remaining := configured - time.Since(started)
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < remaining {
		remaining = time.Until(deadline)
	}
	if remaining < time.Second {
		remaining = time.Second
	}
	return strconv.FormatInt(remaining.Milliseconds(), 10)
}

func sanitizedEnvironment(environment []string, denied []string) []string {
	blocked := make(map[string]bool, len(denied))
	for _, name := range denied {
		blocked[name] = true
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			result = append(result, entry)
		}
	}
	return result
}

func eventJSONL(sequence int, at time.Time, eventType string, binding protocol.TerminalBinding, message string) string {
	body, _ := json.Marshal(map[string]any{"sequence": sequence, "time": at, "type": eventType, "message": message, "terminal": binding})
	return string(body) + "\n"
}
