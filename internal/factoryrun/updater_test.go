package factoryrun

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/review"
)

type memStore struct {
	mu   sync.Mutex
	got  string
	set  bool
	sets int
	err  error
}

func (m *memStore) GetComment(context.Context, string, int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.got, m.err
}

func (m *memStore) SetComment(_ context.Context, _ string, _ int, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.got = body
	m.set = true
	m.sets++
	return nil
}

func TestUpdaterPublishesMarker(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: StageRunning, Verdict: review.VerdictReady}
	body, err := u.Publish(context.Background(), "o/r", 4, e)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !s.set {
		t.Fatal("store was not written")
	}
	got, err := Parse(body)
	if err != nil {
		t.Fatalf("published marker did not re-parse: %v", err)
	}
	if got.Stage != StageRunning {
		t.Fatalf("stage lost in round trip: %q", got.Stage)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("a written marker should record when it was written")
	}
}

// Republishing the same evidence writes nothing, so a caller may hand the same
// evidence to Publish on every tick without churning the issue.
func TestUpdaterSkipsUnchangedRepublish(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: StageRunning}
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if s.sets != 1 {
		t.Fatalf("expected 1 write for unchanged evidence, got %d", s.sets)
	}
	e.Stage = StageComplete
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	if s.sets != 2 {
		t.Fatalf("changed evidence should write, got %d writes", s.sets)
	}
}

// The stored marker is compared as evidence, not as bytes: a clock that has
// moved on is not a material change.
func TestUpdaterDoesNotRewriteForTimeAlone(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	u.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: StageRunning}
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	u.now = func() time.Time { return time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC) }
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if s.sets != 1 {
		t.Fatalf("a later clock is not new evidence, got %d writes", s.sets)
	}
	if !strings.Contains(s.got, "updated_at: 2026-09-04T12:00:00Z") {
		t.Fatalf("stored marker lost its original timestamp: %s", s.got)
	}
}

// A marker nobody can read is not evidence, so it is replaced rather than kept.
func TestUpdaterReplacesUnparsableMarker(t *testing.T) {
	s := &memStore{got: markerAnchor + "\nrepo: o/r\njob: j\nrun: r\ncheck: linux\n"}
	u := NewUpdater(s)
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Stage: StageRunning}
	if _, err := u.Publish(context.Background(), "o/r", 4, e); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if s.sets != 1 {
		t.Fatalf("expected the unreadable marker to be replaced, got %d writes", s.sets)
	}
}

func TestUpdaterRejectsInvalidEvidenceBeforeWriting(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	if _, err := u.Publish(context.Background(), "o/r", 4, Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: "merge-it"}); err == nil {
		t.Fatal("expected publish to reject an out-of-contract verdict")
	}
	if s.sets != 0 {
		t.Fatal("invalid evidence must not reach the store")
	}
}

// A read that fails is not an empty issue: publishing anyway could duplicate a
// marker that is already there.
func TestUpdaterFailsWhenTheStoredMarkerCannotBeRead(t *testing.T) {
	s := &memStore{err: errors.New("github is down")}
	u := NewUpdater(s)
	if _, err := u.Publish(context.Background(), "o/r", 4, Evidence{JobID: "j", RunID: "r", Repo: "o/r"}); err == nil {
		t.Fatal("expected publish to fail when the marker cannot be read")
	}
	if s.sets != 0 {
		t.Fatal("a failed read must not be treated as an empty issue")
	}
}
