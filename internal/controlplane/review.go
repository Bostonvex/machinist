package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// maxReviewBytes bounds one reviewer output block. A review is prose about a
// change, not the change itself.
const maxReviewBytes = 1 << 20

// gitHubPullRequests is what the control plane reads from the forge about
// changes. It is the review route's only source of changed paths, and the
// assigner's only source of which change a run produced: neither the reviewer
// nor the author gets to say either.
type gitHubPullRequests interface {
	ListPullRequestFiles(ctx context.Context, repository string, number int) ([]string, error)
	LinkedPullRequests(ctx context.Context, repository string, number int) ([]GitHubLinkedPullRequest, error)
	// PullRequestHead answers which commit the change currently points at. It
	// is on this interface, rather than read from the submission, for the same
	// reason the changed paths are: the reviewer does not get to say what it
	// reviewed.
	PullRequestHead(ctx context.Context, repository string, number int) (string, error)
}

// submitReview records one independent review of a run.
//
// The route, not a convention, is what makes the review independent. The
// submission carries a lease and an output block; the two identities being
// compared are read from the runs themselves, and the changed paths come from
// GitHub's diff. A submission that cannot be shown to be independent is refused
// and nothing is recorded — a refusal to review is not a failed review, and it
// must never read as one.
//
// The decision is a verdict, never an action: this route merges nothing,
// deploys nothing, and writes no label.
func (s *Server) submitReview(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxReviewBytes) {
		return
	}
	var input protocol.ReviewSubmission
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" || input.ReviewerRun == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id, lease_token, and reviewer_run are required"))
		return
	}
	if input.PullRequest <= 0 {
		writeError(response, http.StatusBadRequest, errors.New("pull_request is required: a review names the change it judged"))
		return
	}
	if input.Output == "" {
		writeError(response, http.StatusBadRequest, errors.New("output is required"))
		return
	}
	reviewedRun := request.PathValue("id")
	subject, err := s.store.ReviewSubject(request.Context(), reviewedRun, input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) || errors.Is(err, ErrReviewScope) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		log.Printf("review run %q: %v", reviewedRun, err)
		writeError(response, http.StatusInternalServerError, errors.New("read review parties"))
		return
	}
	if s.pullRequests == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("no github client: the changed paths of a review cannot be read"))
		return
	}
	// Answered like a missing github client above, and for the same reason:
	// in both cases the control plane cannot read the change, and in neither
	// is that the reviewer's doing or something it can retry its way out of.
	repository, err := s.gitHubRepositoryFor(subject.Repository)
	if err != nil {
		log.Printf("review run %q: %v", reviewedRun, err)
		writeError(response, http.StatusServiceUnavailable, err)
		return
	}
	changedPaths, err := s.pullRequests.ListPullRequestFiles(request.Context(), repository, input.PullRequest)
	if err != nil {
		log.Printf("review run %q: read %s pull request %d files: %v", reviewedRun, repository, input.PullRequest, err)
		writeError(response, http.StatusBadGateway, errors.New("read the reviewed pull request"))
		return
	}
	// Read the head before evaluating, so the commit recorded is the one that
	// was in place while the reviewer's output was being judged rather than
	// whatever the branch has become by the time the row is written.
	//
	// A head the forge will not give up is refused, and nothing is recorded. A
	// verdict that cannot be bound to a commit is not a weaker verdict; the
	// question it answers -- does this approval still apply -- has no answer
	// without one, and an unanswerable approval that is stored anyway will be
	// read by something as a yes.
	reviewedHead, err := s.pullRequests.PullRequestHead(request.Context(), repository, input.PullRequest)
	if err != nil {
		log.Printf("review run %q: read %s pull request %d head: %v", reviewedRun, repository, input.PullRequest, err)
		writeError(response, http.StatusBadGateway, errors.New("read the commit under review"))
		return
	}
	outcome, err := s.reviews.Evaluate(review.Submission{
		Author:       subject.Author,
		Reviewer:     subject.Reviewer,
		ChangedPaths: changedPaths,
		Output:       input.Output,
	})
	if errors.Is(err, review.ErrNotIndependent) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err := s.store.RecordReview(request.Context(), RecordedReview{
		RunID:          reviewedRun,
		ReviewerRunID:  input.ReviewerRun,
		PullRequest:    input.PullRequest,
		Verdict:        outcome.Verdict,
		HighRisk:       outcome.HighRisk,
		Note:           outcome.Note,
		Findings:       outcome.Findings,
		ProtectedPaths: outcome.ProtectedPaths,
		Reasons:        outcome.Reasons,
		ReviewedHead:   reviewedHead,
	}); err != nil {
		log.Printf("review run %q: %v", reviewedRun, err)
		writeError(response, http.StatusInternalServerError, errors.New("record review"))
		return
	}
	writeJSON(response, http.StatusOK, protocol.ReviewOutcome{
		Verdict:        string(outcome.Verdict),
		HighRisk:       outcome.HighRisk,
		ProtectedPaths: outcome.ProtectedPaths,
		Reasons:        outcome.Reasons,
	})
}

// ErrRepositoryUnmapped is returned when a run names a repository that no
// [github.repositories] entry resolves to a forge slug.
var ErrRepositoryUnmapped = errors.New("repository has no github.repositories entry")

// gitHubRepositoryFor turns the repository a run records into the OWNER/REPO
// slug the forge knows it by.
//
// A run stores the logical name its worker registered a checkout under —
// "machinist", not "Bostonvex/machinist". Handing that name to the forge asks
// GitHub for a repository owned by nobody, and the 404 that comes back reads
// like a missing pull request rather than a missing mapping, so a review that
// judged a real change is refused for a reason nobody can act on.
//
// The mapping is operator configuration, so an unmapped repository is refused
// and named rather than guessed at. Falling back to the logical name would put
// the unreadable 404 back; falling back to a slug assembled from some default
// owner would ask the forge about a repository nobody configured, and could
// read the diff of a stranger's change under the same name.
func (s *Server) gitHubRepositoryFor(repository string) (string, error) {
	slug, ok := s.githubRepositories[repository]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrRepositoryUnmapped, repository)
	}
	return slug, nil
}
