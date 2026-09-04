package factoryrun

import (
	"sync"
	"testing"
)

type memStore struct {
	mu  sync.Mutex
	got string
	set bool
}

func (m *memStore) GetComment(string, int) (string, error) { return m.got, nil }
func (m *memStore) SetComment(_ string, _ int, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.got = body
	m.set = true
	return nil
}

func TestUpdaterPublishesMarker(t *testing.T) {
	s := &memStore{}
	u := NewUpdater(s)
	body, err := u.Publish("o/r", 4, Evidence{JobID: "j", RunID: "r", Repo: "o/r", Verdict: "ready"})
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
