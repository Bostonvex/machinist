package factoryrun

import (
	"sync"
	"testing"

	"github.com/owainlewis/machinist/internal/review"
)

type memStore struct {
	mu   sync.Mutex
	got  string
	set  bool
	sets int
}

func (m *memStore) GetComment(string, int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.got, nil
}

func (m *memStore) SetComment(_ string, _ int, body string) error {
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
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: review.VerdictReady}
	body, err := u.Publish("o/r", 4, e)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !s.set {
		t.Fatal("store was not written")
	}
	if _, err := Parse(body); err != nil {
		t.Fatalf("published marker did not re-parse: %v", err)
	}
}

// Republishing unchanged evidence reads the stored marker and writes nothing.
func TestUpdaterSkipsUnchangedRepublish(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: review.VerdictReady}
	if _, err := u.Publish("o/r", 4, e); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := u.Publish("o/r", 4, e); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if s.sets != 1 {
		t.Fatalf("expected 1 write for unchanged evidence, got %d", s.sets)
	}
	e.Verdict = review.VerdictChangesRequested
	if _, err := u.Publish("o/r", 4, e); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	if s.sets != 2 {
		t.Fatalf("changed evidence should write, got %d writes", s.sets)
	}
}

func TestUpdaterRejectsInvalidEvidenceBeforeWriting(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	if _, err := u.Publish("o/r", 4, Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: "merge-it"}); err == nil {
		t.Fatal("expected publish to reject an out-of-contract verdict")
	}
	if s.sets != 0 {
		t.Fatal("invalid evidence must not reach the store")
	}
}
