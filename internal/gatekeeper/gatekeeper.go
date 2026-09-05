// Package gatekeeper decides whether an agent may merge a pull request, and on
// whose authority.
//
// The default is that it may not. Merge is a human act, and every tier that
// relaxes that is opt-in, per repository, and verified against the exact commit
// that will land. Deploy is never relaxed at all.
//
// The package decides; it does not act. Nothing here calls a forge. A caller
// takes the Decision and performs the merge, or does not — which keeps the
// judgement testable and keeps the credential that can merge out of the code
// that reasons about whether merging is allowed.
package gatekeeper

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRefused reports that the merge is not authorized. Callers must treat it as
// a refusal to merge, never as a failed merge: nothing was attempted.
var ErrRefused = errors.New("merge refused")

// Tier is the authority a merge runs on.
type Tier string

const (
	// TierOwnerNamed is the default and needs no repository to opt in: the
	// repository owner named this pull request. One authorization, one pull
	// request.
	TierOwnerNamed Tier = "owner-named"
	// TierGreen permits documentation-only merges. See
	// docs/governance/protocol/merge-tiers.md.
	TierGreen Tier = "green"
	// TierReviewed permits low-risk code merges, and only in repositories the
	// owner has listed.
	TierReviewed Tier = "reviewed"
)

var tiers = map[Tier]struct{}{
	TierOwnerNamed: {}, TierGreen: {}, TierReviewed: {},
}

// Valid reports whether t is a tier this package defines. An unrecognized tier
// is rejected rather than treated as the weakest one.
func (t Tier) Valid() bool {
	_, ok := tiers[t]
	return ok
}

// Decision is the answer to "may this merge happen, and why".
//
// A Decision is only ever produced by Authorize. Its zero value authorizes
// nothing: Allowed is false and Tier is empty, so a Decision that was never
// filled in cannot be mistaken for one that was.
type Decision struct {
	// Allowed reports whether the merge may proceed.
	Allowed bool
	// Tier is the authority it proceeds on, when it may.
	Tier Tier
	// Reasons record every condition that was checked and how it was satisfied,
	// in the order checked. They exist to be written back to the pull request:
	// several conditions cannot be reconstructed from the forge afterwards.
	Reasons []string
	// Refusal explains why the merge may not proceed, when it may not.
	Refusal string
}

// String renders the decision in the gatekeeper role's output form.
func (d Decision) String() string {
	if d.Allowed {
		return fmt.Sprintf("RESULT: merged\nAUTHORIZATION: tier=%s\nCHECKS: %s",
			d.Tier, strings.Join(d.Reasons, "; "))
	}
	return fmt.Sprintf("RESULT: refused — %s", d.Refusal)
}

// refuse builds a refusal Decision. Every exit that is not an explicit
// authorization goes through here, so no path can return a zero Decision that
// reads as an unexplained allow.
func refuse(format string, arguments ...any) (Decision, error) {
	reason := fmt.Sprintf(format, arguments...)
	return Decision{Refusal: reason}, fmt.Errorf("%w: %s", ErrRefused, reason)
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
