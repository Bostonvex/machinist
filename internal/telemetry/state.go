package telemetry

// This file holds the tables that turn a stream of events into the state a
// reader asks about: what an agent is doing now, and how a turn went. They are
// derived, never authoritative — events are the record, and both tables can be
// rebuilt from them.

// agentStates maps an event to the state the agent is in once it has happened.
//
// The map is exhaustive over the event types that say something about what an
// agent is doing. Types absent from it — the samples, the model accounting —
// leave the state alone rather than resetting it, because they describe
// something other than the agent's own progress.
var agentStates = map[EventType]string{
	EventProcessStarted:       "idle",
	EventSessionStarted:       "idle",
	EventTurnStarted:          "waiting_for_activity",
	EventTurnFirstActivity:    "active",
	EventTurnFirstVisibleText: "generating_text",
	EventTurnFirstTool:        "running_tools",
	EventTurnStall:            "stalled",
	EventToolStarted:          "running_tools",
	EventToolUpdated:          "running_tools",
	EventToolCompleted:        "active",
	EventToolFailed:           "active",
	EventTurnCompleted:        "completed",
	EventTurnFailed:           "failed",
	EventTurnCancelled:        "cancelled",
	EventSessionEnded:         "idle",
	EventProcessExited:        "offline",
}

// terminalOutcomes maps the events that end a turn to the outcome recorded for
// it. A turn has exactly one, and it is set by the event that ended the turn
// rather than inferred later from the absence of further events — an agent that
// is killed stops sending events too, and silence is not an outcome.
var terminalOutcomes = map[EventType]string{
	EventTurnCompleted: "completed",
	EventTurnFailed:    "failed",
	EventTurnCancelled: "cancelled",
}

// clearsCurrentTurn lists the events after which an agent is holding no turn.
// Without this an agent that exits mid-turn keeps pointing at the turn it was
// running, and a reader sees work in progress that no process is doing.
var clearsCurrentTurn = map[EventType]bool{
	EventProcessStarted: true,
	EventProcessExited:  true,
	EventSessionEnded:   true,
	EventTurnCompleted:  true,
	EventTurnFailed:     true,
	EventTurnCancelled:  true,
}

// isInfrastructure reports whether an event measures a machine rather than an
// agent. These carry an agent identity for routing but describe a GPU or a
// server, so letting them touch the agent and turn tables would report a host
// that is warm as an agent that is working.
func isInfrastructure(eventType EventType) bool {
	return eventType == EventServerSample || eventType == EventHardwareSample
}
