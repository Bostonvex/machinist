package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A fleet lease decides whether a group of workers may take new work at all.
//
// The rule it holds is an operator's, not the scheduler's: agents must not run
// on a machine its owner is sitting at, and a host that has been stood down
// must stay down until someone says otherwise. buzz-workspace held that rule in
// a signed control message on a relay channel, which made standing a host down
// depend on the relay being reachable. Here it is a row in the control plane's
// own database, consulted on the dispatch side — so standing a fleet down does
// not depend on that fleet being healthy enough to obey.
//
// The lease is checked before new work is offered and never against work
// already running. Cancelling a generation mid-flight is a different decision
// with different consequences, and it is not the one an operator makes by
// standing a fleet down; the incident this mechanism exists for was a
// stood-down seat *claiming* work, not finishing it.

// LeaseState is what an operator has said about a fleet.
type LeaseState string

const (
	// LeaseAllowed lets the fleet take work until the lease expires.
	LeaseAllowed LeaseState = "allowed"
	// LeaseStoodDown refuses it. This is distinct from having no lease at all,
	// which also refuses: at three in the morning the difference between
	// "deliberately stood down, here is why" and "nobody ever granted this"
	// is the whole of what an operator needs to know.
	LeaseStoodDown LeaseState = "stood-down"
)

var leaseStates = []LeaseState{LeaseAllowed, LeaseStoodDown}

// ParseLeaseState refuses anything that is not a state. An unreadable state is
// never treated as permission.
func ParseLeaseState(value string) (LeaseState, error) {
	state := LeaseState(strings.TrimSpace(value))
	for _, known := range leaseStates {
		if state == known {
			return state, nil
		}
	}
	names := make([]string, len(leaseStates))
	for i, known := range leaseStates {
		names[i] = string(known)
	}
	return "", fmt.Errorf("unknown lease state %q (want one of %s)", value, strings.Join(names, ", "))
}

// Lease is one fleet's standing permission.
type Lease struct {
	Fleet     string     `json:"fleet"`
	State     LeaseState `json:"state"`
	ExpiresAt time.Time  `json:"expires_at"`
	Reason    string     `json:"reason"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Allows reports whether the lease permits new work at this instant. Expiry
// always lands on refusal, for either state: a lease is a statement with a
// deadline, and a deadline that quietly extends itself is not one.
func (l Lease) Allows(now time.Time) bool {
	return l.State == LeaseAllowed && now.Before(l.ExpiresAt)
}

// ErrFleetRefused reports that a worker may not take new work. It carries the
// sentence an operator needs, because the alternative — an empty poll — is
// indistinguishable from there being no work, which is the single most
// expensive thing this mechanism could be confused with.
type ErrFleetRefused struct {
	Fleet  string
	Reason string
}

func (e *ErrFleetRefused) Error() string { return e.Reason }

// ErrInvalidLease marks the errors SetLease returns because the lease it was
// handed is not one, as opposed to the errors it returns because the database
// would not take it. Callers at an API edge need to tell those apart: reporting
// a failed write as the operator's mistake sends them to re-read a lease that
// was already correct.
var ErrInvalidLease = errors.New("invalid lease")

// SetLease records what an operator has decided about a fleet. It is the only
// place that decides what a lease may say, so a lease that reached the store by
// any route has been through the same gate.
func (s *Store) SetLease(ctx context.Context, lease Lease) error {
	fleet := strings.TrimSpace(lease.Fleet)
	if fleet == "" {
		return fmt.Errorf("%w: a lease needs a fleet name", ErrInvalidLease)
	}
	if _, err := ParseLeaseState(string(lease.State)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidLease, err)
	}
	if lease.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: a lease needs an expiry, because a permission with no end is not one", ErrInvalidLease)
	}
	if strings.TrimSpace(lease.Reason) == "" {
		return fmt.Errorf("%w: a lease needs a reason, because it is what the next operator reads", ErrInvalidLease)
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO fleet_leases(fleet,state,expires_at,reason,updated_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(fleet) DO UPDATE SET state=excluded.state,expires_at=excluded.expires_at,
		 reason=excluded.reason,updated_at=excluded.updated_at`,
		fleet, string(lease.State), lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(lease.Reason), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store fleet lease: %w", err)
	}
	return nil
}

// Lease reads back one fleet's lease. Writers use it to report what was
// actually stored rather than what they sent: the store trims and normalizes,
// and an operator checking that they stood down the fleet they meant to needs
// to be shown the row, not their own typing echoed back.
func (s *Store) Lease(ctx context.Context, fleet string) (Lease, error) {
	return scanLease(s.db.QueryRowContext(ctx,
		`SELECT fleet,state,expires_at,reason,updated_at FROM fleet_leases WHERE fleet=?`,
		strings.TrimSpace(fleet)))
}

// Leases lists every lease, whether or not it is still in force. An expired
// lease is the record of a decision and is shown as one.
func (s *Store) Leases(ctx context.Context) ([]Lease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fleet,state,expires_at,reason,updated_at FROM fleet_leases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leases []Lease
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].Fleet < leases[j].Fleet })
	return leases, nil
}

type rowScanner interface {
	Scan(destination ...any) error
}

func scanLease(row rowScanner) (Lease, error) {
	var lease Lease
	var state, expires, updated string
	if err := row.Scan(&lease.Fleet, &state, &expires, &lease.Reason, &updated); err != nil {
		return Lease{}, err
	}
	// A state or a time that cannot be read is an error, never a lease that
	// happens to permit. The whole value of the mechanism is that it is the one
	// thing in the dispatch path that does not guess.
	parsed, err := ParseLeaseState(state)
	if err != nil {
		return Lease{}, fmt.Errorf("fleet %q: %w", lease.Fleet, err)
	}
	lease.State = parsed
	if lease.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return Lease{}, fmt.Errorf("fleet %q: unreadable lease expiry %q: %w", lease.Fleet, expires, err)
	}
	if lease.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Lease{}, fmt.Errorf("fleet %q: unreadable lease timestamp %q: %w", lease.Fleet, updated, err)
	}
	return lease, nil
}

// checkFleetLease answers whether this poll may be offered new work. It is
// called inside the poll transaction so a lease revoked between the read and
// the dispatch cannot let one more run through.
func checkFleetLease(ctx context.Context, tx *sql.Tx, fleet string, now time.Time) error {
	fleet = strings.TrimSpace(fleet)
	if fleet == "" {
		// Required leasing with an unnamed fleet is not an exemption. A worker
		// that has not said which fleet it belongs to cannot be stood down,
		// which is precisely the state the mechanism exists to make impossible.
		return &ErrFleetRefused{Reason: "this control plane requires a fleet lease, and the worker did not say which fleet it is in: set [worker] fleet"}
	}
	lease, err := scanLease(tx.QueryRowContext(ctx,
		`SELECT fleet,state,expires_at,reason,updated_at FROM fleet_leases WHERE fleet=?`, fleet))
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrFleetRefused{Fleet: fleet, Reason: fmt.Sprintf("fleet %q holds no lease", fleet)}
	}
	if err != nil {
		return err
	}
	if lease.Allows(now) {
		return nil
	}
	if lease.State == LeaseStoodDown {
		return &ErrFleetRefused{Fleet: fleet, Reason: fmt.Sprintf("fleet %q is stood down: %s", fleet, lease.Reason)}
	}
	return &ErrFleetRefused{Fleet: fleet, Reason: fmt.Sprintf(
		"fleet %q held a lease that expired at %s: %s", fleet, lease.ExpiresAt.Format(time.RFC3339), lease.Reason)}
}
