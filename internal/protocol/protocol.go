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
	Transports   []string                     `json:"transports,omitempty"`
	// Fleet is the group of workers an operator stands up or down as one. It
	// is configuration, not detection: the operator decides which machines
	// share a fate, and no fact the worker could measure about itself says so.
	Fleet string `json:"fleet,omitempty"`
}

type PollResponse struct {
	Run *RunSpec `json:"run,omitempty"`
	// Refused says why no work was offered, when the reason was a decision
	// rather than an empty queue. A silent empty poll is indistinguishable
	// from there being nothing to do, which is the single most expensive
	// thing a standing refusal could be confused with.
	Refused string `json:"refused,omitempty"`
}

type RunSpec struct {
	ID                 string   `json:"id"`
	AttemptID          string   `json:"attempt_id,omitempty"`
	AttemptNumber      int      `json:"attempt_number,omitempty"`
	PreviousErrorClass string   `json:"previous_error_class,omitempty"`
	JobID              string   `json:"job_id"`
	Command            string   `json:"command"`
	CommandHash        string   `json:"command_hash"`
	Executor           string   `json:"executor"`
	Profile            string   `json:"profile,omitempty"`
	Route              string   `json:"route,omitempty"`
	Candidates         []string `json:"candidates,omitempty"`
	MaxAttempts        int      `json:"max_attempts,omitempty"`
	MaxTotalTokens     int64    `json:"max_total_tokens,omitempty"`
	FallbackOn         []string `json:"fallback_on,omitempty"`
	Harness            string   `json:"harness,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	AuthMode           string   `json:"auth_mode,omitempty"`
	Role               string   `json:"role,omitempty"`
	Model              string   `json:"model,omitempty"`
	Repository         string   `json:"repository"`
	RenderedPrompt     string   `json:"rendered_prompt"`
	TimeoutMillis      int64    `json:"timeout_millis"`
	LeaseToken         string   `json:"lease_token"`
	ExecutionMode      string   `json:"execution_mode,omitempty"`
	Origin             string   `json:"origin,omitempty"`
}

type TerminalBinding struct {
	Session     string `json:"session,omitempty"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id,omitempty"`
	PaneID      string `json:"pane_id"`
	AgentName   string `json:"agent_name"`
}

type BindTerminalRequest struct {
	InstanceID string          `json:"instance_id"`
	LeaseToken string          `json:"lease_token"`
	AttemptID  string          `json:"attempt_id"`
	Terminal   TerminalBinding `json:"terminal"`
}

type Heartbeat struct {
	InstanceID string `json:"instance_id"`
	LeaseToken string `json:"lease_token"`
}

type HeartbeatResponse struct {
	CancelRequested bool `json:"cancel_requested"`
}

// ReviewSubmission offers one reviewer's output as the review of a run. The
// run being reviewed is addressed by the request path; ReviewerRun is the run
// that did the reviewing, and its lease authenticates the submission.
//
// The submission makes no claim about who either party is. The control plane
// reads both identities from the runs themselves, so independence is decided
// from recorded state rather than from what the submitter says about itself.
type ReviewSubmission struct {
	InstanceID string `json:"instance_id"`
	LeaseToken string `json:"lease_token"`
	// ReviewerRun is the run submitting the review.
	ReviewerRun string `json:"reviewer_run"`
	// PullRequest is the pull request the reviewer judged. The control plane
	// reads that pull request's changed paths itself: a reviewer says which
	// change it reviewed, never what that change touched.
	PullRequest int `json:"pull_request"`
	// Output is the reviewer's raw output block.
	Output string `json:"output"`
}

// ReviewOutcome is the decision the control plane recorded, returned so the
// submitting run learns the verdict its own review produced after policy.
type ReviewOutcome struct {
	Verdict        string   `json:"verdict"`
	HighRisk       bool     `json:"high_risk"`
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
	// Promoted says whether this review took the change out of draft. It is
	// reported rather than inferred from the verdict: an approval that could
	// not be promoted is still an approval, and the difference is the one thing
	// a reader cannot work out from the verdict alone.
	Promoted bool `json:"promoted,omitempty"`
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
