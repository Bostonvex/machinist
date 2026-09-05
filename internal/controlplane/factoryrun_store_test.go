package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/factoryrun"
)

type fakeCommentClient struct {
	comments []GitHubIssueComment
	listErr  error
	created  []string
	updated  map[int64]string
	nextID   int64
}

func newFakeCommentClient(comments ...GitHubIssueComment) *fakeCommentClient {
	return &fakeCommentClient{comments: comments, updated: map[int64]string{}, nextID: 100}
}

func (f *fakeCommentClient) ListIssueComments(context.Context, string, int) ([]GitHubIssueComment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.comments, nil
}

func (f *fakeCommentClient) CreateIssueComment(_ context.Context, _ string, _ int, body string) error {
	f.nextID++
	f.created = append(f.created, body)
	f.comments = append(f.comments, GitHubIssueComment{ID: f.nextID, Body: body})
	return nil
}

func (f *fakeCommentClient) UpdateIssueComment(_ context.Context, _ string, id int64, body string) error {
	f.updated[id] = body
	for i := range f.comments {
		if f.comments[i].ID == id {
			f.comments[i].Body = body
			return nil
		}
	}
	return errors.New("no such comment")
}

func markerBody(t *testing.T, e factoryrun.Evidence) string {
	t.Helper()
	body, err := factoryrun.Render(e)
	if err != nil {
		t.Fatalf("render marker: %v", err)
	}
	return body
}

func TestMarkerStoreAddsTheFirstMarker(t *testing.T) {
	client := newFakeCommentClient(GitHubIssueComment{ID: 1, Body: "unrelated discussion"})
	store := newGitHubMarkerStore(client)
	body, err := store.GetComment(context.Background(), "owner/name", 4)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		t.Fatalf("an issue with no marker reported one: %q", body)
	}
	marker := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name", Stage: factoryrun.StageRunning})
	if err := store.SetComment(context.Background(), "owner/name", 4, marker); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 1 || len(client.updated) != 0 {
		t.Fatalf("expected one created comment, got created=%d updated=%d", len(client.created), len(client.updated))
	}
}

// The marker is found by its content, so a control plane that never wrote it --
// one that has just restarted, say -- still updates it instead of adding a second.
func TestMarkerStoreUpdatesAMarkerItDidNotWrite(t *testing.T) {
	existing := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name", Stage: factoryrun.StageClaimed})
	client := newFakeCommentClient(
		GitHubIssueComment{ID: 1, Body: "unrelated"},
		GitHubIssueComment{ID: 2, Body: existing},
		GitHubIssueComment{ID: 3, Body: "later chatter"},
	)
	store := newGitHubMarkerStore(client)
	body, err := store.GetComment(context.Background(), "owner/name", 4)
	if err != nil {
		t.Fatal(err)
	}
	if body != existing {
		t.Fatalf("did not find the existing marker: %q", body)
	}
	updated := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name", Stage: factoryrun.StageComplete})
	if err := store.SetComment(context.Background(), "owner/name", 4, updated); err != nil {
		t.Fatal(err)
	}
	if len(client.created) != 0 {
		t.Fatalf("expected the existing marker to be edited, not duplicated: %#v", client.created)
	}
	if client.updated[2] != updated {
		t.Fatalf("comment 2 was not updated: %#v", client.updated)
	}
}

// Two markers on one issue is a repository someone has edited by hand. Evidence
// converges on the newest rather than alternating between them.
func TestMarkerStoreUpdatesTheNewestMarker(t *testing.T) {
	first := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name", Stage: factoryrun.StageClaimed})
	second := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name", Stage: factoryrun.StageRunning})
	client := newFakeCommentClient(
		GitHubIssueComment{ID: 1, Body: first},
		GitHubIssueComment{ID: 2, Body: second},
	)
	store := newGitHubMarkerStore(client)
	body, err := store.GetComment(context.Background(), "owner/name", 4)
	if err != nil {
		t.Fatal(err)
	}
	if body != second {
		t.Fatal("expected the newest marker to be read back")
	}
	if err := store.SetComment(context.Background(), "owner/name", 4, second); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.updated[2]; !ok || len(client.created) != 0 {
		t.Fatalf("the newest marker was not the one updated: updated=%#v created=%#v", client.updated, client.created)
	}
}

// The store maintains one comment: the marker. It is not a way to post anything
// else to an issue.
func TestMarkerStoreRefusesToWriteANonMarker(t *testing.T) {
	client := newFakeCommentClient()
	store := newGitHubMarkerStore(client)
	if err := store.SetComment(context.Background(), "owner/name", 4, "ship it"); err == nil {
		t.Fatal("expected the store to refuse a comment that is not a marker")
	}
	if len(client.created) != 0 || len(client.updated) != 0 {
		t.Fatal("a refused body must not reach GitHub")
	}
}

func TestMarkerStoreReportsListFailures(t *testing.T) {
	client := newFakeCommentClient()
	client.listErr = errors.New("github is down")
	store := newGitHubMarkerStore(client)
	if _, err := store.GetComment(context.Background(), "owner/name", 4); err == nil || !strings.Contains(err.Error(), "owner/name#4") {
		t.Fatalf("a failed listing should name the issue: %v", err)
	}
	marker := markerBody(t, factoryrun.Evidence{JobID: "j", RunID: "r", Repo: "owner/name"})
	if err := store.SetComment(context.Background(), "owner/name", 4, marker); err == nil {
		t.Fatal("a write must not proceed when the existing comments cannot be read")
	}
	if len(client.created) != 0 {
		t.Fatal("an unreadable issue must not be treated as an issue with no marker")
	}
}
