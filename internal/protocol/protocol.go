package protocol

import (
	"encoding/json"

	"github.com/owainlewis/machinist/internal/environment"
)

type ProfileCapability struct {
	Harness   string   `json:"harness"`
	Provider  string   `json:"provider,omitempty"`
	AuthMode  string   `json:"auth_mode"`
	Models    []string `json:"models,omitempty"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
}

type PollRequest struct {
	InstanceID   string                       `json:"instance_id"`
	Name         string                       `json:"name"`
	Executors    []string                     `json:"executors"`
	Repositories []string                     `json:"repositories"`
	Models       map[string][]string          `json:"models,omitempty"`
	Profiles     map[string]ProfileCapability `json:"profiles,omitempty"`
	Environment  environment.Manifest         `json:"environment,omitempty"`
}

type PollResponse struct {
	Run *RunSpec `json:"run,omitempty"`
}

type RunSpec struct {
	ID             string   `json:"id"`
	AttemptID      string   `json:"attempt_id,omitempty"`
	AttemptNumber  int      `json:"attempt_number,omitempty"`
	JobID          string   `json:"job_id"`
	Command        string   `json:"command"`
	CommandHash    string   `json:"command_hash"`
	Executor       string   `json:"executor"`
	Profile        string   `json:"profile,omitempty"`
	Route          string   `json:"route,omitempty"`
	Candidates     []string `json:"candidates,omitempty"`
	MaxAttempts    int      `json:"max_attempts,omitempty"`
	FallbackOn     []string `json:"fallback_on,omitempty"`
	Harness        string   `json:"harness,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	AuthMode       string   `json:"auth_mode,omitempty"`
	Role           string   `json:"role,omitempty"`
	Model          string   `json:"model,omitempty"`
	Repository     string   `json:"repository"`
	RenderedPrompt string   `json:"rendered_prompt"`
	TimeoutMillis  int64    `json:"timeout_millis"`
	LeaseToken     string   `json:"lease_token"`
}

type Heartbeat struct {
	InstanceID string `json:"instance_id"`
	LeaseToken string `json:"lease_token"`
}

type Completion struct {
	InstanceID string          `json:"instance_id"`
	LeaseToken string          `json:"lease_token"`
	AttemptID  string          `json:"attempt_id,omitempty"`
	State      string          `json:"state"`
	ExitCode   int             `json:"exit_code"`
	ErrorClass string          `json:"error_class,omitempty"`
	Error      string          `json:"error,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Events     string          `json:"events,omitempty"`
}
