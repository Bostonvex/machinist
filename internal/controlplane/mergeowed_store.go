package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/gatekeeper"
	"github.com/owainlewis/machinist/internal/review"
)

// RecordedJudgements folds every review recorded against one GitHub repository
// into at most one judgement per pull request.
//
// Three reviewers on the same change produce three rows, and the fold is:
//
//   - The verdict is the strictest of them, via review.Strictest. One
//     reviewer's objection is not cleared by another reviewer's approval, which
//     is the same rule the run marker publishes under.
//   - The findings are the union. A finding raised by any reviewer is open,
//     whoever else did not raise it.
//   - The commit and the time come from the newest row. A reviewer that looked
//     at the current head supersedes one that looked at an older one, and a
//     newer review of an older commit leaves the judgement bound to that older
//     commit -- which reads as stale, which is the safe direction.
//
// The rows are the whole input. A pull request with no rows is absent from the
// result rather than present with an empty judgement, because "nobody has
// reviewed this" and "somebody reviewed it and recorded nothing" are different
// facts and only one of them is true here.
func (s *Store) RecordedJudgements(ctx context.Context, repository string) (map[int]gatekeeper.Judgement, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return nil, errors.New("merge-owed judgements need a repository")
	}
	// The scope is the run's repository, which is the same name the review
	// route scopes a submission on. Going through the GitHub trigger that
	// created the job instead would silently drop every review of work that was
	// not started from an issue, and a review that is dropped reads as a change
	// nobody has looked at.
	rows, err := s.db.QueryContext(ctx, `SELECT v.pull_request,v.verdict,v.reviewed_head,v.findings,v.recorded_at
FROM run_reviews v
JOIN runs r ON r.id=v.run_id
WHERE r.repository=? AND v.pull_request>0
ORDER BY v.pull_request, v.recorded_at, v.reviewer_run_id`, repository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	judgements := map[int]gatekeeper.Judgement{}
	verdicts := map[int][]review.Verdict{}
	for rows.Next() {
		var pullRequest int
		var verdict, head, encodedFindings, recordedAt string
		if err := rows.Scan(&pullRequest, &verdict, &head, &encodedFindings, &recordedAt); err != nil {
			return nil, err
		}
		var findings []review.Finding
		if err := json.Unmarshal([]byte(encodedFindings), &findings); err != nil {
			// A row whose findings will not decode is not a row with no
			// findings. Refusing the whole read is the only answer that cannot
			// be mistaken for an approval with nothing outstanding.
			return nil, fmt.Errorf("decode findings recorded against %s#%d: %w", repository, pullRequest, err)
		}
		when, err := time.Parse(time.RFC3339Nano, recordedAt)
		if err != nil {
			return nil, fmt.Errorf("decode the time a review of %s#%d was recorded: %w", repository, pullRequest, err)
		}
		// The rows arrive oldest first, so the last one to be folded is the
		// newest and its commit is the one the judgement is bound to.
		folded := judgements[pullRequest]
		folded.ReviewedHead = head
		folded.RecordedAt = when
		folded.Findings = append(folded.Findings, findings...)
		judgements[pullRequest] = folded
		verdicts[pullRequest] = append(verdicts[pullRequest], review.Verdict(verdict))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	for pullRequest, recorded := range verdicts {
		strictest, err := review.Strictest(recorded...)
		if err != nil {
			// A verdict this build cannot rank is refused rather than dropped
			// or ranked lowest. Dropping it would let an unreadable objection
			// become an approval by omission.
			return nil, fmt.Errorf("fold the verdicts recorded against %s#%d: %w", repository, pullRequest, err)
		}
		folded := judgements[pullRequest]
		folded.Verdict = strictest
		judgements[pullRequest] = folded
	}
	return judgements, nil
}

// MergeOwedRepositories lists the GitHub repositories the control plane has
// reviews for, so a caller that names none is told what it could have named
// rather than being handed an empty answer it cannot distinguish from silence.
func (s *Store) MergeOwedRepositories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT r.repository
FROM run_reviews v
JOIN runs r ON r.id=v.run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	sort.Strings(repositories)
	return repositories, nil
}
