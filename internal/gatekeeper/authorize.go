package gatekeeper

import (
	"fmt"

	"github.com/owainlewis/machinist/internal/review"
)

// Authorize decides whether one pull request may be merged, and on what
// authority.
//
// It tries the owner's word first, because that is the default and the only
// authority that needs no repository to opt in. Only if the owner has not named
// this pull request does it consider an enabled tier.
//
// Every path that does not end in an explicit authorization is a refusal with a
// reason. There is no path that returns an allowed Decision without having
// checked the conditions of the tier it names.
func Authorize(request Request, enabled Enablement) (Decision, error) {
	if err := request.Valid(); err != nil {
		return refuse("the merge request is incomplete: %s", err)
	}

	if request.OwnerNamed {
		return authorizeOwnerNamed(request)
	}

	// Green before Reviewed: it is the narrower tier, and a change that
	// qualifies for it should be recorded as documentation rather than as
	// low-risk code.
	if enabled.Green {
		if decision, err := evaluateGreen(request, enabled); err == nil {
			return decision, nil
		}
	}
	if enabled.reviewedAllows(request.Repository) {
		return evaluateReviewed(request, enabled)
	}
	if enabled.Green {
		// Re-run Green so its refusal, rather than a generic one, is what the
		// caller is told. Reviewed is not available here, so Green's reason is
		// the whole reason.
		return evaluateGreen(request, enabled)
	}
	return refuse("the owner has not named pull request %s#%d and no tier is enabled for it",
		request.Repository, request.PullRequest)
}

// authorizeOwnerNamed applies the conditions the gatekeeper role owes even when
// the owner has spoken. The owner names a pull request; they do not thereby
// certify that CI passed or that the change closes what it claims to.
func authorizeOwnerNamed(request Request) (Decision, error) {
	reasons := []string{"authorization: the repository owner named this pull request"}

	if missing := missingChecks(request.Checks); len(missing) > 0 {
		return refuse("required check %q is not passing on %s", missing[0], short(request.HeadSHA))
	}
	if len(request.Checks.Required) > 0 {
		reasons = append(reasons, fmt.Sprintf("checks: %d required, all passing on %s",
			len(request.Checks.Required), short(request.HeadSHA)))
	} else {
		reasons = append(reasons, "checks: the repository requires none")
	}

	// A review is not required for an owner-named merge — the owner may merge
	// unreviewed work — but a review that exists and withholds approval is not
	// silently discarded.
	if request.Review.Verdict != "" {
		if !request.Review.Verdict.Valid() {
			return refuse("the review verdict %q is not one this contract defines", request.Review.Verdict)
		}
		if request.Review.Verdict == review.VerdictEscalate {
			return refuse("the review returned %q, which no authority below a human may overrule", review.VerdictEscalate)
		}
		reasons = append(reasons, fmt.Sprintf("review: %s", request.Review.Verdict))
	}

	if !request.ClosingIssuesMatchIntent {
		return refuse("closing issues have not been confirmed to match intent")
	}
	reasons = append(reasons, closingIssuesReason(request))

	return Decision{Allowed: true, Tier: TierOwnerNamed, Reasons: reasons}, nil
}

// DeployRequest is a request to deploy a named target by a named procedure.
type DeployRequest struct {
	// Target is what is being deployed, named by the owner.
	Target string
	// Procedure is how, named by the owner.
	Procedure string
	// Tag is the release artifact tag the deploy publishes. Deploy happens by
	// tagging a release artifact, which is a recorded, reversible act, and never
	// by an agent reaching a host directly.
	Tag string
	// OwnerAuthorized records that the owner authorized this deploy — this
	// target, this procedure. It is separate from any merge authorization.
	OwnerAuthorized bool
}

// AuthorizeDeploy decides whether a deploy may proceed.
//
// No tier reaches this function, and none ever should. Merge tiers exist
// because a bad merge is visible in a diff and revertible with another merge; a
// deploy changes what is running. Every deploy needs the owner to name the
// target and the procedure, and that is the only authority this function
// accepts.
func AuthorizeDeploy(request DeployRequest) (Decision, error) {
	if !request.OwnerAuthorized {
		return refuse("deploy is never authorized by a tier; the owner must name the target and the procedure")
	}
	if normalize(request.Target) == "" {
		return refuse("deploy target is unnamed")
	}
	if normalize(request.Procedure) == "" {
		return refuse("deploy procedure is unnamed")
	}
	if normalize(request.Tag) == "" {
		return refuse("deploy publishes no release artifact tag")
	}
	return Decision{
		Allowed: true,
		Tier:    TierOwnerNamed,
		Reasons: []string{
			fmt.Sprintf("authorization: the owner named target %q and procedure %q", request.Target, request.Procedure),
			fmt.Sprintf("artifact: release tag %s", request.Tag),
		},
	}, nil
}
