package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
)

// gitHubOpenChanges is the forge read merge-owed detection needs. It is an
// interface for the same reason the review route's is: the judgement is worth
// testing against a forge that says awkward things, and a test that has to run
// gh cannot say them.
type gitHubOpenChanges interface {
	OpenChanges(ctx context.Context, repository string) ([]gatekeeper.Change, error)
}

// mergeOwedView is one change and what is owed on it.
//
// It carries the reason as text rather than leaving the caller to rebuild it
// from the disposition and the fields. Two readers reconstructing the same
// sentence is two places for it to be reconstructed differently, and the
// sentence is the part a person acts on.
type mergeOwedView struct {
	Repository   string   `json:"repository"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	Head         string   `json:"head"`
	Disposition  string   `json:"disposition"`
	Reason       string   `json:"reason"`
	OpenFindings []string `json:"open_findings,omitempty"`
	OwedSeconds  float64  `json:"owed_seconds"`
}

// readMergeOwed answers which changes in one repository are owed a merge, which
// are owed attention, and which are owed nothing yet.
//
// It is a read of the forge and the store together, and it decides nothing on
// its own: the judgement is gatekeeper.OwedAcross, which never sees a
// credential. Nothing here merges anything either. The command this serves
// tells a person what to go and do, which is the same shape the shell had and
// the same reason: merge is a human act.
func (s *Server) readMergeOwed(response http.ResponseWriter, request *http.Request) {
	repository := strings.TrimSpace(request.URL.Query().Get("repository"))
	if repository == "" {
		known, err := s.store.MergeOwedRepositories(request.Context())
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		writeError(response, http.StatusBadRequest, fmt.Errorf(
			"name a repository: the control plane has reviews for %s", describeRepositories(known)))
		return
	}
	slug, err := s.gitHubRepositoryFor(repository)
	if err != nil {
		// An unmapped repository is refused rather than passed to the forge
		// under its logical name, for the reason gitHubRepositoryFor gives: the
		// 404 that would come back reads like an empty repository, and "nothing
		// is owed here" is exactly the wrong thing to say about work nobody
		// looked for.
		writeError(response, http.StatusBadRequest, err)
		return
	}
	changes, err := s.changes.OpenChanges(request.Context(), slug)
	if err != nil {
		var forge *GitHubCLIError
		if errors.As(err, &forge) && forge.Kind == GitHubCLIErrorRateLimit {
			// The budget is spent. Retrying here would spend the next one on
			// the same answer, and reporting an empty list would say nothing is
			// owed. Refusing loudly is the only one of the three that is true.
			writeError(response, http.StatusTooManyRequests, err)
			return
		}
		writeError(response, http.StatusBadGateway, err)
		return
	}
	// The store is asked under the logical name and the forge under the slug.
	// They are different names for the same repository, and mixing them up
	// gives an answer that is confidently empty.
	judgements, err := s.store.RecordedJudgements(request.Context(), repository)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	standings := gatekeeper.OwedAcross(changes, judgements, s.store.now().UTC())
	views := make([]mergeOwedView, 0, len(standings))
	for _, standing := range standings {
		views = append(views, mergeOwedView{
			Repository:   standing.Change.Repository,
			Number:       standing.Change.Number,
			Title:        standing.Change.Title,
			URL:          standing.Change.URL,
			Head:         standing.Change.Head,
			Disposition:  string(standing.Disposition),
			Reason:       standing.Reason,
			OpenFindings: severityNames(standing),
			OwedSeconds:  standing.Owed.Seconds(),
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"repository": slug,
		"changes":    views,
		"read_at":    s.store.now().UTC().Format(time.RFC3339),
	})
}

func severityNames(standing gatekeeper.Standing) []string {
	if len(standing.OpenFindings) == 0 {
		return nil
	}
	names := make([]string, 0, len(standing.OpenFindings))
	for _, severity := range standing.OpenFindings {
		names = append(names, string(severity))
	}
	return names
}

// describeRepositories names what the caller could have asked about. An empty
// list says so in words rather than as an empty sentence, because "the control
// plane has reviews for " reads as a bug in the message rather than as the
// absence it is.
func describeRepositories(repositories []string) string {
	if len(repositories) == 0 {
		return "no repository yet"
	}
	return strings.Join(repositories, ", ")
}
