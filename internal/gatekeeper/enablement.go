package gatekeeper

import "strings"

// Enablement is which tiers a repository owner has turned on.
//
// Its zero value enables nothing, which is the documented default: a tier that
// is described is not a tier that is enabled. Owner-named merge needs no
// enablement and is unaffected by this type.
type Enablement struct {
	// Green enables documentation-only merges.
	Green bool
	// ReviewedRepositories are the repositories, as "owner/name", where the
	// Reviewed tier applies. There is no boolean for Reviewed on purpose: the
	// tier is meaningless without a repository list, and a boolean would let it
	// be switched on everywhere by a single true.
	ReviewedRepositories []string
}

// reviewedAllows reports whether the Reviewed tier applies to a repository.
// An unnamed repository is not allowed, and neither is an empty repository
// name — matching nothing is not matching everything.
func (e Enablement) reviewedAllows(repository string) bool {
	wanted := normalize(repository)
	if wanted == "" {
		return false
	}
	for _, listed := range e.ReviewedRepositories {
		if normalize(listed) == wanted {
			return true
		}
	}
	return false
}

// Enables reports whether a tier is available at all in a repository, before
// any of its conditions are looked at. Owner-named is always available; it is
// the conditions, not the enablement, that decide it.
func (e Enablement) Enables(tier Tier, repository string) bool {
	switch tier {
	case TierOwnerNamed:
		return true
	case TierGreen:
		return e.Green
	case TierReviewed:
		return e.reviewedAllows(repository)
	default:
		return false
	}
}

// String renders the enablement for the record, so a merge note can say what
// was turned on when it happened.
func (e Enablement) String() string {
	parts := []string{}
	if e.Green {
		parts = append(parts, "green")
	}
	if len(e.ReviewedRepositories) > 0 {
		parts = append(parts, "reviewed="+strings.Join(e.ReviewedRepositories, ","))
	}
	if len(parts) == 0 {
		return "no tier enabled"
	}
	return strings.Join(parts, " ")
}
