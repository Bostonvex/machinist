package proxy

import (
	"errors"
	"sync"
)

// The three things a measurement can honestly say about which turn it belongs
// to. They are stored on the event so a reader can weigh a per-agent latency
// figure by how confidently the calls under it were attributed.
const (
	// CorrelationExact means the turn was named, or was the only one running.
	CorrelationExact = "exact"
	// CorrelationAmbiguous means several turns were running and the call named
	// none of them. It could belong to any of them.
	CorrelationAmbiguous = "ambiguous"
	// CorrelationUnavailable means nothing was running that this proxy knew
	// about, so the call belongs to no turn it can name.
	CorrelationUnavailable = "unavailable"
)

// maximumContexts bounds what one registry holds. A harness that starts turns
// and never ends them — because it crashed, or because it does not implement
// the ending half — would otherwise grow this without limit. The oldest is
// dropped first, on the reasoning that a turn still open after 256 others
// began is not the one a call belongs to.
const maximumContexts = 256

// Context is who a model call belongs to.
//
// It is supplied by something that can see the turn — a harness bridge, the
// worker — because the proxy cannot know. A model endpoint receives a request
// with no agent in it, and inventing one from the connection would attribute
// calls by whichever process happened to be listening.
type Context struct {
	AgentID     string
	DisplayName string
	Harness     string
	Model       string
	EndpointID  string
	SessionID   string
	TurnID      string
	Correlation string
}

// ErrInvalidContext is returned for a context that cannot be stored as one.
var ErrInvalidContext = errors.New("invalid correlation context")

// Registry tracks the turns that are running right now.
//
// It is an ordered map rather than a plain one because eviction has to drop
// the oldest, and because resolving to "the only active turn" needs to be able
// to tell one from many.
type Registry struct {
	mutex    sync.Mutex
	order    []string
	active   map[string]Context
	fallback Context
}

// NewRegistry builds a registry whose fallback is used when no turn can be
// named. The fallback carries the endpoint's own identity, so a call made
// outside any turn is still attributed to the endpoint that served it rather
// than being dropped.
func NewRegistry(fallback Context) *Registry {
	fallback.Correlation = CorrelationUnavailable
	return &Registry{active: map[string]Context{}, fallback: fallback}
}

// Start records a turn as running and returns how many now are.
//
// Every identifier is validated here rather than at the edge, because this is
// the last place before the value becomes part of an event: a context id with
// a newline in it would become a grouping key with a newline in it.
func (r *Registry) Start(contextID string, context Context) (int, error) {
	if identifierOrEmpty(contextID) == "" {
		return 0, ErrInvalidContext
	}
	for _, required := range []string{context.AgentID, context.SessionID, context.TurnID} {
		if safeIdentifier(required) == "" {
			return 0, ErrInvalidContext
		}
	}
	for _, optional := range []string{context.Harness, context.Model, context.EndpointID} {
		if optional != "" && safeIdentifier(optional) == "" {
			return 0, ErrInvalidContext
		}
	}
	if label := safeLabel(context.DisplayName); label == "" {
		return 0, ErrInvalidContext
	} else {
		context.DisplayName = label
	}
	context.Correlation = CorrelationExact

	r.mutex.Lock()
	// The endpoint being measured is this proxy's own, and a harness that did
	// not name it was not disagreeing. Leaving these empty would drop what is
	// already known rather than record an uncertainty.
	if context.Model == "" {
		context.Model = r.fallback.Model
	}
	if context.EndpointID == "" {
		context.EndpointID = r.fallback.EndpointID
	}
	defer r.mutex.Unlock()
	if _, existing := r.active[contextID]; existing {
		// A restarted turn keeps its place in the order rather than jumping to
		// the front. Refreshing it would let one busy turn hold the registry
		// open against every other.
		r.active[contextID] = context
		return len(r.active), nil
	}
	r.active[contextID] = context
	r.order = append(r.order, contextID)
	for len(r.order) > maximumContexts {
		delete(r.active, r.order[0])
		r.order = r.order[1:]
	}
	return len(r.active), nil
}

// End records that a turn finished. Ending one that was never started is not
// an error: a harness that reports the end of a turn the proxy missed the
// start of has still told the truth about the turn.
func (r *Registry) End(contextID string) (int, error) {
	if identifierOrEmpty(contextID) == "" {
		return 0, ErrInvalidContext
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, found := r.active[contextID]; found {
		delete(r.active, contextID)
		for index, id := range r.order {
			if id == contextID {
				r.order = append(r.order[:index], r.order[index+1:]...)
				break
			}
		}
	}
	return len(r.active), nil
}

// Resolve decides which turn a call belongs to.
//
// Named wins, because the caller knew. One active turn is exact, because there
// is nothing else it could be. Several are ambiguous, and the ambiguity is
// recorded rather than resolved by picking: attributing a call to the wrong
// agent is worse than attributing it to none, because the wrong attribution is
// indistinguishable from a right one once it is a row in a table.
func (r *Registry) Resolve(contextID string) Context {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if contextID != "" {
		if found, ok := r.active[contextID]; ok {
			return found
		}
	}
	switch len(r.order) {
	case 0:
		return r.fallback
	case 1:
		return r.active[r.order[0]]
	default:
		// The endpoint's identity is kept because it is still known; the turn
		// and session are dropped because they are exactly what is not.
		ambiguous := r.fallback
		ambiguous.SessionID = ""
		ambiguous.TurnID = ""
		ambiguous.Correlation = CorrelationAmbiguous
		return ambiguous
	}
}

// Active is how many turns the registry believes are running. It exists for
// the context route's answer, which is what lets a harness notice that it has
// been leaking turns.
func (r *Registry) Active() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.active)
}

// safeIdentifier bounds a value that becomes a grouping key. The rules are the
// collector's: it will refuse anything else at ingest, and a measurement
// refused at ingest is a measurement lost after the call it described is gone.
func safeIdentifier(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for index, letter := range value {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9':
		case index > 0 && (letter == '.' || letter == '_' || letter == ':' ||
			letter == '/' || letter == '+' || letter == '@' || letter == '=' || letter == '-'):
		default:
			return ""
		}
	}
	return value
}

// safeLabel bounds a value meant for a person to read. It allows more than an
// identifier because a display name is prose, and less than free text because
// it still ends up in a table cell.
func safeLabel(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, letter := range value {
		if letter < 0x20 || letter == 0x7f {
			return ""
		}
	}
	return value
}
