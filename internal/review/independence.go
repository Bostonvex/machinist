package review

import (
	"errors"
	"fmt"
)

// RoleImplementer and RoleReviewer are the two governance roles this package
// arbitrates. They match docs/governance/roles/.
const (
	RoleImplementer = "implementer"
	RoleReviewer    = "reviewer"
)

// Party is one side of a review: the agent identity that acted and the
// Machinist run it acted in.
type Party struct {
	// Role is the governance role the party held, such as "implementer".
	Role string
	// Agent identifies who acted — an agent id, or the profile that ran.
	Agent string
	// RunID is the Machinist run the party acted in, when one exists.
	RunID string
}

// ErrNotIndependent reports that the proposed review would not be independent.
// Callers must treat it as a refusal to review, never as a failed review.
var ErrNotIndependent = errors.New("review is not independent")

// CheckIndependence reports whether reviewer may judge author's work.
//
// Independence is separation of context, not of machine: the same worker may
// host both parties, but the same agent may not both write and bless a change,
// and a run may not review itself. Missing identity fails closed — an unnamed
// reviewer cannot be shown to be independent.
func CheckIndependence(author, reviewer Party) error {
	if normalize(author.Agent) == "" {
		return fmt.Errorf("%w: author agent is unknown", ErrNotIndependent)
	}
	if normalize(reviewer.Agent) == "" {
		return fmt.Errorf("%w: reviewer agent is unknown", ErrNotIndependent)
	}
	if normalize(reviewer.Role) != RoleReviewer {
		return fmt.Errorf("%w: reviewing party holds role %q, not %q", ErrNotIndependent, reviewer.Role, RoleReviewer)
	}
	if normalize(author.Role) == RoleReviewer {
		return fmt.Errorf("%w: authoring party holds role %q", ErrNotIndependent, RoleReviewer)
	}
	if normalize(author.Agent) == normalize(reviewer.Agent) {
		return fmt.Errorf("%w: agent %q wrote the change", ErrNotIndependent, reviewer.Agent)
	}
	if runID := normalize(author.RunID); runID != "" && runID == normalize(reviewer.RunID) {
		return fmt.Errorf("%w: run %q cannot review itself", ErrNotIndependent, author.RunID)
	}
	return nil
}
