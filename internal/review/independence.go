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

// IsReviewer normalises role the way every role-matching call site must, and
// reports whether it is the reviewer role. It is the single source of truth for
// the role-matching rule: independence checking, review assignment, and lease
// handover all consult it, so a role spelled with different case or padding is
// still the role it names and no caller can ever diverge from the rule.
func IsReviewer(role string) bool {
	return normalize(role) == RoleReviewer
}

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
	if !IsReviewer(reviewer.Role) {
		return fmt.Errorf("%w: reviewing party holds role %q, not %q", ErrNotIndependent, reviewer.Role, RoleReviewer)
	}
	if IsReviewer(author.Role) {
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
