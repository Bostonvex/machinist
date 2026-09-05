package telemetry

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limits on what one request may be. They are the reason a producer that
// misbehaves costs the collector a rejection rather than its memory.
const (
	// MaximumBody is the largest request body read. A batch of 100 events is
	// roughly 60KB; the rest is headroom for long identifiers.
	MaximumBody = 256 * 1024
	// IngestPath is the only path that accepts events.
	IngestPath = "/api/v1/events"
	// HealthPath answers whether the collector is up and what it holds.
	HealthPath = "/healthz"
	// maximumCachedSummaries bounds the summary cache. Past this it is cleared
	// rather than trimmed: the cache exists to absorb a few dashboards polling
	// the same few windows, and one entry per distinct query string would be a
	// way to spend this process's memory from a URL.
	maximumCachedSummaries = 32
	// defaultSampleLimit is the shared-reading page size. Samples arrive far
	// more often than turns do, so their default page is smaller.
	defaultSampleLimit = 200
)

// Server is the loopback ingest endpoint.
//
// It listens on 127.0.0.1 and nowhere else. Telemetry is metadata rather than
// content, but it is still a live description of what every agent on this
// machine is doing, and the difference between that being local and being on a
// network is one line of configuration nobody should be able to get wrong by
// accident. Reaching it from another host is a deliberate act — an SSH tunnel —
// not a default.
type Server struct {
	store     *Store
	token     string
	retention time.Duration
	logger    *log.Logger
	now       func() time.Time

	purgeMutex sync.Mutex
	lastPurge  time.Time

	broker *broker

	summaryMutex sync.Mutex
	summaries    map[Filter]cachedSummary

	diagnosticsMutex sync.Mutex
	diagnostics      func() any
}

// cachedSummary is one computed summary and when it was computed.
type cachedSummary struct {
	at      time.Time
	summary FleetSummary
}

// NewServer builds the ingest handler. An empty token is refused: an
// unauthenticated ingest endpoint would let anything that can open a loopback
// socket write into the record other tools are read from.
func NewServer(store *Store, token string, retention time.Duration, logger *log.Logger) (*Server, error) {
	if store == nil {
		return nil, errors.New("telemetry server requires a store")
	}
	if len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("telemetry server requires an ingest token of at least 32 characters")
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		store: store, token: token, retention: retention, logger: logger, now: time.Now,
		broker: newBroker(), summaries: map[Filter]cachedSummary{},
	}, nil
}

// Listen binds the collector to loopback. A host other than 127.0.0.1 is
// refused rather than bound: see Server.
func Listen(address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if host != "127.0.0.1" && host != "localhost" {
		return nil, errors.New("the telemetry collector listens only on loopback")
	}
	return net.Listen("tcp", address)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+IngestPath, s.ingest)
	mux.HandleFunc("GET "+HealthPath, s.health)
	s.readRoutes(mux)
	return mux
}

type failure struct {
	Error string `json:"error"`
	Path  string `json:"path,omitempty"`
}

func write(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func (s *Server) ingest(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		// The refusal says only that the credential was wrong. Saying whether
		// the header was missing, malformed, or simply not this token tells a
		// caller which of those to fix, and the only caller that needs that is
		// one that does not have the token.
		write(response, http.StatusUnauthorized, failure{Error: "invalid_token"})
		return
	}
	if mediaType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); !strings.EqualFold(mediaType, "application/json") {
		write(response, http.StatusUnsupportedMediaType, failure{Error: "content_type_must_be_json"})
		return
	}

	// The body is capped before it is read, not after. A cap applied to a body
	// already in memory is not a cap.
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, MaximumBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			write(response, http.StatusRequestEntityTooLarge, failure{Error: "body_too_large"})
			return
		}
		write(response, http.StatusBadRequest, failure{Error: "unreadable_body"})
		return
	}
	if len(body) == 0 {
		write(response, http.StatusBadRequest, failure{Error: "empty_body"})
		return
	}

	var submitted any
	if err := json.Unmarshal(body, &submitted); err != nil {
		write(response, http.StatusBadRequest, failure{Error: "invalid_json"})
		return
	}

	events, err := ValidateBatch(submitted, DefaultMaximumBatch)
	if err != nil {
		// The validator's code and path are returned so a producer can fix the
		// field it got wrong. Neither carries the rejected value, so a batch
		// refused for holding something it should not have does not put that
		// thing into an HTTP response as well.
		var invalid ValidationError
		if errors.As(err, &invalid) {
			write(response, http.StatusUnprocessableEntity, failure{Error: invalid.Code, Path: invalid.Path})
			return
		}
		write(response, http.StatusUnprocessableEntity, failure{Error: "invalid_batch"})
		return
	}

	inserted, err := s.store.Insert(request.Context(), events)
	if err != nil {
		// Logged here and not returned. A storage error can name a column, a
		// file path, or a constraint, and a producer can do nothing with any of
		// it; the operator reading the log can.
		s.logger.Printf("telemetry: store %d events: %v", len(events), err)
		write(response, http.StatusInternalServerError, failure{Error: "storage_failure"})
		return
	}
	s.maintainRetention(request.Context())
	// Published after the write returned, so a live view never shows an event
	// that a reader following it would then fail to find in the store.
	s.broker.publish(events)

	write(response, http.StatusAccepted, map[string]int{"accepted": len(events), "inserted": inserted})
}

// SetProviderDiagnostics supplies what the health endpoint reports about the
// infrastructure providers feeding this collector.
//
// The value is opaque to the collector on purpose. Providers import this
// package to build the events they emit, so this package cannot import theirs
// to name their status type, and restating that type here would be a second
// copy to keep in step with the first. Whoever wires the providers owns their
// shape; the collector only publishes it.
//
// Nothing set means no providers were configured, which is reported as such
// rather than as an empty set: a collector with no providers and one whose
// providers have not reported yet are different things.
func (s *Server) SetProviderDiagnostics(diagnostics func() any) {
	s.diagnosticsMutex.Lock()
	defer s.diagnosticsMutex.Unlock()
	s.diagnostics = diagnostics
}

func (s *Server) providerDiagnostics() (any, bool) {
	s.diagnosticsMutex.Lock()
	diagnostics := s.diagnostics
	s.diagnosticsMutex.Unlock()
	if diagnostics == nil {
		return nil, false
	}
	return diagnostics(), true
}

// Ingest stores events produced inside this process and publishes them to live
// readers, the same way an accepted request does.
//
// Infrastructure providers poll on their own schedule instead of posting to the
// ingest endpoint: they are in this process, and making them authenticate to it
// over loopback would mean holding the token in order to talk to the thing that
// issued it. Routing them through here rather than straight at the store is
// what keeps a live view from going quiet whenever the only thing happening is
// hardware.
//
// A failure is logged and nothing is published. There is no caller to return it
// to -- a poller has no request to fail -- and publishing an event the store
// rejected would show a live reader something it could never look up.
func (s *Server) Ingest(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}
	if _, err := s.store.Insert(ctx, events); err != nil {
		s.logger.Printf("telemetry: store %d provider event(s): %v", len(events), err)
		return
	}
	s.maintainRetention(ctx)
	s.broker.publish(events)
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	health, err := s.store.Health(request.Context())
	if err != nil {
		s.logger.Printf("telemetry: health: %v", err)
		write(response, http.StatusServiceUnavailable, failure{Error: "unavailable"})
		return
	}
	if diagnostics, configured := s.providerDiagnostics(); configured {
		health["providers"] = diagnostics
	}
	write(response, http.StatusOK, health)
}

// authorized compares in constant time. A byte-by-byte comparison that returns
// early leaks the token's prefix to anything that can time a request, and this
// endpoint is reachable by every process on the machine.
func (s *Server) authorized(request *http.Request) bool {
	supplied := request.Header.Get("Authorization")
	expected := "Bearer " + s.token
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}

// maintainRetention purges at most every five minutes, on the ingest path
// rather than from a timer. A collector nobody is sending to does not need to
// wake up to delete nothing, and one being written to hard is exactly the one
// whose disk is filling.
func (s *Server) maintainRetention(ctx context.Context) {
	s.purgeMutex.Lock()
	defer s.purgeMutex.Unlock()
	now := s.now()
	if !s.lastPurge.IsZero() && now.Sub(s.lastPurge) < 5*time.Minute {
		return
	}
	s.lastPurge = now
	if _, err := s.store.Purge(ctx, now.Add(-s.retention)); err != nil {
		// A failed purge is not a failed ingest. The events were stored; the
		// disk is a little fuller than intended, and that is the operator's
		// problem to see in a log rather than the producer's to retry.
		s.logger.Printf("telemetry: purge: %v", err)
	}
}
