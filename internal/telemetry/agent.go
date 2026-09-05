package telemetry

import (
	"context"
	"fmt"
)

// maximumAgentSummaryTurns bounds what one agent's summary reads. It matches
// the listing's page rather than the fleet summary's window: this answers a
// reader who clicked one agent, and the turns they can see below the numbers
// are the turns the numbers were computed from.
const maximumAgentSummaryTurns = defaultListLimit

// AgentSummary is one agent, the aggregate over its turns in the window, and
// those turns.
type AgentSummary struct {
	Agent     AgentRow  `json:"agent"`
	Aggregate Aggregate `json:"aggregate"`
	Turns     []TurnRow `json:"turns"`
	Limited   bool      `json:"limited"`
}

// AgentSummary returns one agent's summary, or false if no such agent exists.
//
// The agent is looked up without the window, and its turns with it. An agent
// that ran nothing this week still exists, and answering 404 for it would tell
// a reader who narrowed the window that the agent was gone rather than idle.
func (s *Store) AgentSummary(ctx context.Context, agentID string, filter Filter) (AgentSummary, bool, error) {
	if agentID == "" || len(agentID) > maximumIdentifier {
		return AgentSummary{}, false, fmt.Errorf("agent id is %w", ErrInvalidIdentifier)
	}
	normalized, err := filter.Normalized()
	if err != nil {
		return AgentSummary{}, false, err
	}

	agents, err := s.ListAgents(ctx, Filter{AgentID: agentID}, 1)
	if err != nil {
		return AgentSummary{}, false, err
	}
	if len(agents) == 0 {
		return AgentSummary{}, false, nil
	}

	// The caller's agent filter is replaced rather than merged. A request for
	// agent A carrying ?agent=B is a contradiction, and answering it with B's
	// turns under A's name would be the one shape of wrong answer nothing on
	// the page could reveal.
	normalized.AgentID = agentID
	turns, err := s.ListTurns(ctx, normalized, maximumAgentSummaryTurns, 0)
	if err != nil {
		return AgentSummary{}, false, err
	}
	return AgentSummary{
		Agent: agents[0], Aggregate: aggregate(turns), Turns: turns,
		// Limited says the window held more turns than the aggregate was
		// computed from, so a reader can tell a quiet agent from a truncated
		// answer.
		Limited: len(turns) == maximumAgentSummaryTurns,
	}, true, nil
}
