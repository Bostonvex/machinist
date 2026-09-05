package factoryrun

import (
	"context"
	"fmt"
	"time"
)

// Store is the minimal surface the updater needs to persist or fetch a marker.
type Store interface {
	// GetComment returns the current marker body, or empty if none.
	GetComment(ctx context.Context, repo string, number int) (string, error)
	// SetComment writes (replaces) the marker body on the issue.
	SetComment(ctx context.Context, repo string, number int, body string) error
}

// Updater maintains the FACTORY:RUN marker for a GitHub issue without writing
// any other GitHub state. It never merges or deploys.
type Updater struct {
	store Store
	now   func() time.Time
}

// NewUpdater returns an Updater bound to a Store.
func NewUpdater(store Store) *Updater {
	return &Updater{store: store, now: time.Now}
}

// Publish writes the evidence as the issue's marker and returns the rendered
// body.
//
// It compares the stored marker as evidence rather than as bytes, so a
// republication that says nothing new writes nothing: the caller may hand the
// same evidence to Publish on every scheduler tick without churning the issue.
// A stamped UpdatedAt therefore always marks a material change. A stored marker
// that cannot be parsed is not evidence, so it is replaced rather than trusted.
func (u *Updater) Publish(ctx context.Context, repo string, number int, e Evidence) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	current, err := u.store.GetComment(ctx, repo, number)
	if err != nil {
		return "", fmt.Errorf("factoryrun: read marker: %w", err)
	}
	if current != "" {
		if stored, parseErr := Parse(current); parseErr == nil && stored.SameEvidence(e) {
			return current, nil
		}
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = u.now().UTC()
	}
	body, err := Render(e)
	if err != nil {
		return "", err
	}
	if err := u.store.SetComment(ctx, repo, number, body); err != nil {
		return "", fmt.Errorf("factoryrun: write marker: %w", err)
	}
	return body, nil
}
