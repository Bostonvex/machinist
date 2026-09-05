package controlplane

import (
	"context"
	"log"

	"github.com/owainlewis/machinist/internal/review"
)

// promoteOnApproval takes a pull request out of draft when every verdict
// recorded against it is an approval.
//
// The implement command opens its change as a draft. The window that holds shut
// is real: unreviewed machine-written work otherwise sits in a change a person
// is invited to read, that CI-gated automation can act on, and that reads
// exactly like work which has already passed. Draft is the one state GitHub
// offers that says "written, not yet vouched for", and this is what ends it.
//
// It folds every recorded verdict rather than trusting the one that just
// arrived. Two reviewers on one change is the normal case for high-risk work,
// and promoting on the second reviewer's approval while the first reviewer's
// objection still stands would present objected-to work as ready to read --
// the exact failure the draft prevents, arrived at from the other direction.
//
// Findings do not block promotion, and that is deliberate. Promotion is not
// merge authority: the gatekeeper decides what may land, and it already reports
// an approval carrying findings as attention-owed rather than merge-owed. If a
// nit on an approval kept the change a draft, nothing would ever promote it --
// no second review is scheduled for work that was approved -- and the draft
// would stop meaning "unreviewed" and start meaning "reviewed at some point".
//
// There is no matching demotion, in this function or anywhere. Converting a
// change back to a draft would undo a deliberate human act -- someone marking
// their own work ready -- on the strength of an automated verdict, and the
// objection is already recorded where it is acted on.
//
// Every failure here leaves the change a draft, which is the standing it
// already had, and needs no recovery: the review is recorded before this runs,
// so the standing still reaches `machinist merge-owed`. A change that stayed a
// draft when it could have been promoted costs a person one click. The opposite
// mistake costs a review.
func (s *Server) promoteOnApproval(ctx context.Context, repository, slug string, pullRequest int) bool {
	if s.pullRequests == nil {
		return false
	}
	judgements, err := s.store.RecordedJudgements(ctx, repository)
	if err != nil {
		// A verdict that cannot be read is not an approval. This is the place
		// where treating it as one would be invisible: the change would simply
		// be ready, with nothing on it saying why.
		log.Printf("promote %s pull request %d: read recorded verdicts: %v", slug, pullRequest, err)
		return false
	}
	judgement, judged := judgements[pullRequest]
	if !judged || judgement.Verdict != review.VerdictReady {
		return false
	}
	if err := s.pullRequests.PromotePullRequest(ctx, slug, pullRequest); err != nil {
		log.Printf("promote %s pull request %d: %v", slug, pullRequest, err)
		return false
	}
	return true
}
