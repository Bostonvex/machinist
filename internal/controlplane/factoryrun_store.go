package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/owainlewis/machinist/internal/factoryrun"
)

// githubCommentClient is the GitHub surface marker maintenance needs. It is
// deliberately separate from githubTriggerClient: publishing evidence must not
// give a caller the ability to label, admit, or acknowledge anything.
type githubCommentClient interface {
	ListIssueComments(ctx context.Context, repository string, number int) ([]GitHubIssueComment, error)
	CreateIssueComment(ctx context.Context, repository string, number int, body string) error
	UpdateIssueComment(ctx context.Context, repository string, commentID int64, body string) error
}

// githubMarkerStore keeps one issue comment as a run's FACTORY:RUN marker.
//
// The marker is identified by its own content rather than by an id remembered
// locally, so a control plane that restarts, or one that never wrote the marker
// in the first place, still finds and updates the same comment instead of
// adding a second one. When an issue somehow carries more than one marker, the
// newest wins and is the one updated, so evidence converges on a single comment
// rather than alternating between two.
type githubMarkerStore struct {
	client githubCommentClient
}

func newGitHubMarkerStore(client githubCommentClient) *githubMarkerStore {
	return &githubMarkerStore{client: client}
}

// GetComment returns the body of the issue's marker, or empty when it has none.
func (s *githubMarkerStore) GetComment(ctx context.Context, repository string, number int) (string, error) {
	comment, err := s.findMarker(ctx, repository, number)
	if err != nil {
		return "", err
	}
	return comment.Body, nil
}

// SetComment writes the body to the issue's existing marker, or adds one.
func (s *githubMarkerStore) SetComment(ctx context.Context, repository string, number int, body string) error {
	if !factoryrun.IsMarker(body) {
		return errors.New("refusing to write a comment that is not a factory run marker")
	}
	comment, err := s.findMarker(ctx, repository, number)
	if err != nil {
		return err
	}
	if comment.ID > 0 {
		return s.client.UpdateIssueComment(ctx, repository, comment.ID, body)
	}
	return s.client.CreateIssueComment(ctx, repository, number, body)
}

func (s *githubMarkerStore) findMarker(ctx context.Context, repository string, number int) (GitHubIssueComment, error) {
	comments, err := s.client.ListIssueComments(ctx, repository, number)
	if err != nil {
		return GitHubIssueComment{}, fmt.Errorf("list comments on %s#%d: %w", repository, number, err)
	}
	var found GitHubIssueComment
	for _, comment := range comments {
		if factoryrun.IsMarker(comment.Body) {
			found = comment
		}
	}
	return found, nil
}
