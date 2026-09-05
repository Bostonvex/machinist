package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// maxReviewBytes bounds one reviewer output block. A review is prose about a
// change, not the change itself.
const maxReviewBytes = 1 << 20

// pullRequestFileLister reads the paths a pull request touches. It is the
// review route's only source of changed paths: neither the reviewer nor the
// author gets to say what the change touched.
type pullRequestFileLister interface {
	ListPullRequestFiles(ctx context.Context, repository string, number int) ([]string, error)
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
	changedPaths, err := s.pullRequests.ListPullRequestFiles(request.Context(), subject.Repository, input.PullRequest)
	if err != nil {
		log.Printf("review run %q: read pull request %d files: %v", reviewedRun, input.PullRequest, err)
		writeError(response, http.StatusBadGateway, errors.New("read the reviewed pull request"))
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
