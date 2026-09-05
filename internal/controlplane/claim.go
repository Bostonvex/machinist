package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// An issue claim says who is working on a GitHub issue, so two agents do not
// start on the same one.
//
// It replaces factory-claim.sh, which encoded claims as `CLAIMED:` and
// `RELEASED:` lines in issue comments and rebuilt the current holder by
// replaying the whole thread. That design existed because nothing in the
// factory had a place to keep the fact. It brought with it every problem of a
// reconstructed state: a paginated read that stopped at page one picked the
// wrong claim, two comments could claim the same issue with nothing to stop
// them, and a release naming a holder that appeared twice released whichever
// one the replay happened to reach first.
//
// Here a claim is a row with a primary key. The collision the shell had to
// detect after the fact cannot be written in the first place, and reading the
// current holder is a lookup rather than a replay. What survives from the shell
// is its judgement, not its mechanism: the rules below are the ones it learned
// by losing real work.

// ClaimState is what a claim row currently asserts.
type ClaimState string

const (
	// ClaimHeld means someone is working on the issue now.
	ClaimHeld ClaimState = "held"
	// ClaimOnHold means nobody is working on it and it is still not free. A
	// redirect leaves this behind so the issue is not picked up by the next
	// agent to look at it while the redirect is still in flight. The shell
	// spelled this as a release plus prose, because its grammar had no room for
	// a third thing; the distinction was always real.
	ClaimOnHold ClaimState = "on-hold"
)

var claimStates = []ClaimState{ClaimHeld, ClaimOnHold}

// ParseClaimState refuses anything it does not recognise. A claim state that
// cannot be read is not a claim that is free.
func ParseClaimState(value string) (ClaimState, error) {
	for _, state := range claimStates {
		if string(state) == strings.TrimSpace(value) {
			return state, nil
		}
	}
	names := make([]string, 0, len(claimStates))
	for _, state := range claimStates {
		names = append(names, string(state))
	}
	return "", fmt.Errorf("unknown claim state %q: expected one of %s", value, strings.Join(names, ", "))
}

// Claim is one issue and what is currently asserted about it.
type Claim struct {
	Repository string     `json:"repository"`
	Issue      int        `json:"issue"`
	State      ClaimState `json:"state"`
	// Holder is the seat that took the claim. It is a name someone can be found
	// by, not an instance id: the point of a claim is that a person or an agent
	// can be asked what happened to it.
	Holder    string    `json:"holder"`
	Branch    string    `json:"branch,omitempty"`
	Reason    string    `json:"reason"`
	Transfer  string    `json:"transfer,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Live says whether this claim still stops anyone else taking the issue. Both
// states stop them; only time lifts either. An expired claim is not a claim,
// which is what makes it safe for the holder to disappear.
func (c Claim) Live(now time.Time) bool { return now.Before(c.ExpiresAt) }

// ErrInvalidClaim marks the errors a write returns because the claim it was
// handed is not one, as opposed to the errors it returns because the database
// would not take it. An API edge has to tell those apart: reporting a failed
// write as the caller's mistake sends them to correct a claim that was already
// right.
var ErrInvalidClaim = errors.New("invalid claim")

// ErrClaimTaken is returned when a live claim already stands on the issue and
// it belongs to someone else.
type ErrClaimTaken struct {
	Repository string
	Issue      int
	Holder     string
	ExpiresAt  time.Time
}

func (e *ErrClaimTaken) Error() string {
	return fmt.Sprintf("%s#%d is claimed by %s until %s",
		e.Repository, e.Issue, e.Holder, e.ExpiresAt.UTC().Format(time.RFC3339))
}

// ErrNoClaim is returned when a transition was asked for on an issue nothing
// holds. The shell refused this too, and for the reason it wrote down: a
// release with nothing to release is a sign the caller is working from a stale
// read, and answering it with success confirms the staleness.
type ErrNoClaim struct {
	Repository string
	Issue      int
}

func (e *ErrNoClaim) Error() string {
	return fmt.Sprintf("no live claim on %s#%d: there is nothing to release", e.Repository, e.Issue)
}

// claimKey normalises the two halves of the primary key and refuses a key that
// cannot identify an issue. An issue number of zero is not issue zero; it is a
// number nobody supplied.
func claimKey(repository string, issue int) (string, int, error) {
	trimmed := strings.TrimSpace(repository)
	if trimmed == "" {
		return "", 0, fmt.Errorf("%w: a claim needs a repository", ErrInvalidClaim)
	}
	if issue <= 0 {
		return "", 0, fmt.Errorf("%w: a claim needs an issue number", ErrInvalidClaim)
	}
	return trimmed, issue, nil
}

// TakeClaim records that a holder is working on an issue.
//
// It refuses when a live claim already stands and belongs to somebody else. The
// same holder re-taking their own claim extends it, because that is what an
// agent doing a long piece of work needs and there is nobody to collide with.
func (s *Store) TakeClaim(ctx context.Context, claim Claim) (Claim, error) {
	repository, issue, err := claimKey(claim.Repository, claim.Issue)
	if err != nil {
		return Claim{}, err
	}
	holder := strings.TrimSpace(claim.Holder)
	if holder == "" {
		return Claim{}, fmt.Errorf("%w: a claim needs a holder, because the point of one is that someone can be asked about it", ErrInvalidClaim)
	}
	if strings.TrimSpace(claim.Reason) == "" {
		return Claim{}, fmt.Errorf("%w: a claim needs a reason, because it is what the next agent reads", ErrInvalidClaim)
	}
	if claim.ExpiresAt.IsZero() {
		return Claim{}, fmt.Errorf("%w: a claim needs an expiry, because a claim nobody can outlast is a lock", ErrInvalidClaim)
	}
	now := s.now().UTC()
	if !now.Before(claim.ExpiresAt.UTC()) {
		return Claim{}, fmt.Errorf("%w: a claim that has already expired claims nothing", ErrInvalidClaim)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	existing, err := readClaim(ctx, tx, repository, issue)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, err
	}
	if err == nil && existing.Live(now) && existing.Holder != holder {
		return Claim{}, &ErrClaimTaken{
			Repository: repository, Issue: issue,
			Holder: existing.Holder, ExpiresAt: existing.ExpiresAt,
		}
	}
	// The original claimed_at survives a holder extending their own claim, so
	// "how long has this been going" is still answerable afterwards.
	claimedAt := now
	if err == nil && existing.Live(now) && existing.Holder == holder {
		claimedAt = existing.ClaimedAt
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issue_claims(repository,issue,state,holder,branch,reason,transfer,claimed_at,expires_at,updated_at)
		 VALUES(?,?,?,?,?,?,'',?,?,?)
		 ON CONFLICT(repository,issue) DO UPDATE SET state=excluded.state,holder=excluded.holder,
		 branch=excluded.branch,reason=excluded.reason,transfer='',claimed_at=excluded.claimed_at,
		 expires_at=excluded.expires_at,updated_at=excluded.updated_at`,
		repository, issue, string(ClaimHeld), holder, strings.TrimSpace(claim.Branch),
		strings.TrimSpace(claim.Reason), claimedAt.Format(time.RFC3339Nano),
		claim.ExpiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return Claim{}, fmt.Errorf("take issue claim: %w", err)
	}
	stored, err := readClaim(ctx, tx, repository, issue)
	if err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, err
	}
	return stored, nil
}

// ReleaseClaim gives an issue back. It refuses when nothing live is there to
// release, and when the claim that is there belongs to somebody else: releasing
// another seat's claim is how real work gets lost, which is the incident the
// shell version was written after.
func (s *Store) ReleaseClaim(ctx context.Context, repository string, issue int, holder, reason string) (Claim, error) {
	return s.transitionClaim(ctx, repository, issue, holder, reason, ClaimHeld, time.Time{}, "")
}

// HoldClaim releases a claim into a hold: nobody is working on the issue and it
// is still not free work. The expiry has to be in the future, because a hold
// that has already lapsed is a release and saying so is the caller's job, not
// something to be silently corrected for them.
func (s *Store) HoldClaim(ctx context.Context, repository string, issue int, holder, reason string, until time.Time, transfer string) (Claim, error) {
	return s.transitionClaim(ctx, repository, issue, holder, reason, ClaimOnHold, until, transfer)
}

// transitionClaim is the one path both release and hold take, so the rules
// about who may transition a claim are stated once. The difference between them
// is what is left behind, not who is allowed to do it.
func (s *Store) transitionClaim(ctx context.Context, repository string, issue int, holder, reason string, into ClaimState, until time.Time, transfer string) (Claim, error) {
	repository, issue, err := claimKey(repository, issue)
	if err != nil {
		return Claim{}, err
	}
	holder = strings.TrimSpace(holder)
	if holder == "" {
		return Claim{}, fmt.Errorf("%w: say which holder is transitioning the claim", ErrInvalidClaim)
	}
	if strings.TrimSpace(reason) == "" {
		return Claim{}, fmt.Errorf("%w: a transition needs a reason, because it is the record of why the work stopped", ErrInvalidClaim)
	}
	now := s.now().UTC()
	if into == ClaimOnHold {
		if until.IsZero() {
			return Claim{}, fmt.Errorf("%w: a hold needs an end", ErrInvalidClaim)
		}
		if !now.Before(until.UTC()) {
			return Claim{}, fmt.Errorf("%w: a hold that has already lapsed is a release, so release it", ErrInvalidClaim)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	existing, err := readClaim(ctx, tx, repository, issue)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, &ErrNoClaim{Repository: repository, Issue: issue}
	}
	if err != nil {
		return Claim{}, err
	}
	if !existing.Live(now) {
		return Claim{}, &ErrNoClaim{Repository: repository, Issue: issue}
	}
	if existing.Holder != holder {
		return Claim{}, &ErrClaimTaken{
			Repository: repository, Issue: issue,
			Holder: existing.Holder, ExpiresAt: existing.ExpiresAt,
		}
	}
	if into == ClaimHeld {
		// A release removes the row rather than expiring it in place. A row
		// that lingers describing a claim nobody holds is the state every
		// reader then has to remember to discount, and the ones that forget
		// read it as a claim.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM issue_claims WHERE repository=? AND issue=?`, repository, issue); err != nil {
			return Claim{}, fmt.Errorf("release issue claim: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Claim{}, err
		}
		return Claim{}, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE issue_claims SET state=?,reason=?,transfer=?,expires_at=?,updated_at=? WHERE repository=? AND issue=?`,
		string(ClaimOnHold), strings.TrimSpace(reason), strings.TrimSpace(transfer),
		until.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		repository, issue); err != nil {
		return Claim{}, fmt.Errorf("hold issue claim: %w", err)
	}
	stored, err := readClaim(ctx, tx, repository, issue)
	if err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, err
	}
	return stored, nil
}

// Claim reads back one issue's claim. A missing row is reported as a missing
// claim rather than as an error, because "nobody holds this" is an answer.
func (s *Store) Claim(ctx context.Context, repository string, issue int) (Claim, bool, error) {
	repository, issue, err := claimKey(repository, issue)
	if err != nil {
		return Claim{}, false, err
	}
	claim, err := readClaim(ctx, s.db, repository, issue)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		return Claim{}, false, err
	}
	return claim, true, nil
}

// Claims lists every claim, expired ones included. An expired claim is still
// worth showing: it is the trace of work that stopped without being handed
// back, and hiding it hides the thing an operator most needs to notice.
func (s *Store) Claims(ctx context.Context) ([]Claim, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT repository,issue,state,holder,branch,reason,transfer,claimed_at,expires_at,updated_at
		 FROM issue_claims ORDER BY repository,issue`)
	if err != nil {
		return nil, fmt.Errorf("read issue claims: %w", err)
	}
	defer rows.Close()
	claims := []Claim{}
	for rows.Next() {
		claim, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issue claims: %w", err)
	}
	return claims, rows.Close()
}

func readClaim(ctx context.Context, executor contextQuerier, repository string, issue int) (Claim, error) {
	return scanClaim(executor.QueryRowContext(ctx,
		`SELECT repository,issue,state,holder,branch,reason,transfer,claimed_at,expires_at,updated_at
		 FROM issue_claims WHERE repository=? AND issue=?`, repository, issue))
}

// contextQuerier is the part of a database handle or transaction a claim read
// needs, so the same read runs inside and outside a transaction.
type contextQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanClaim reads one row, and refuses one it cannot read.
//
// This is the fail-closed rule the shell wrote down as "comments unreadable →
// refuse; never guess a claim state". A row with a state this build does not
// know, or a timestamp it cannot parse, is an error. The tempting alternative —
// treat it as free — hands the issue to a second agent precisely when the first
// one's record has gone strange, which is the worst moment to do it.
func scanClaim(scanner rowScanner) (Claim, error) {
	var claim Claim
	var state, claimedAt, expiresAt, updatedAt string
	if err := scanner.Scan(&claim.Repository, &claim.Issue, &state, &claim.Holder,
		&claim.Branch, &claim.Reason, &claim.Transfer, &claimedAt, &expiresAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Claim{}, err
		}
		return Claim{}, fmt.Errorf("read issue claim: %w", err)
	}
	parsed, err := ParseClaimState(state)
	if err != nil {
		return Claim{}, fmt.Errorf("read issue claim for %s#%d: %w", claim.Repository, claim.Issue, err)
	}
	claim.State = parsed
	for _, field := range []struct {
		name string
		text string
		into *time.Time
	}{
		{name: "claimed_at", text: claimedAt, into: &claim.ClaimedAt},
		{name: "expires_at", text: expiresAt, into: &claim.ExpiresAt},
		{name: "updated_at", text: updatedAt, into: &claim.UpdatedAt},
	} {
		value, err := time.Parse(time.RFC3339Nano, field.text)
		if err != nil {
			return Claim{}, fmt.Errorf("read issue claim for %s#%d: %s is unreadable: %w",
				claim.Repository, claim.Issue, field.name, err)
		}
		*field.into = value.UTC()
	}
	return claim, nil
}
