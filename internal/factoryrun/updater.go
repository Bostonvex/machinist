package factoryrun

import "fmt"

// Store is the minimal surface the updater needs to persist or fetch a marker.
type Store interface {
	// GetComment returns the current marker body, or empty if none.
	GetComment(repo string, number int) (string, error)
	// SetComment writes (replaces) the marker body on the issue.
	SetComment(repo string, number int, body string) error
}

// Updater maintains the FACTORY:RUN marker for a GitHub issue without writing
// any other GitHub state. It never merges or deploys.
type Updater struct {
	store Store
}

// NewUpdater returns an Updater bound to a Store.
func NewUpdater(store Store) *Updater {
	return &Updater{store: store}
}

// Publish renders the evidence and writes (replaces) the marker comment on the
// issue, returning the rendered body. Because Render is deterministic, a
// republish of unchanged evidence reads the stored marker and writes nothing,
// so a retried or repeated handoff does not churn the issue.
func (u *Updater) Publish(repo string, number int, e Evidence) (string, error) {
	body, err := Render(e)
	if err != nil {
		return "", err
	}
	current, err := u.store.GetComment(repo, number)
	if err != nil {
		return "", fmt.Errorf("factoryrun: read marker: %w", err)
	}
	if current == body {
		return body, nil
	}
	if err := u.store.SetComment(repo, number, body); err != nil {
		return "", fmt.Errorf("factoryrun: write marker: %w", err)
	}
	return body, nil
}
