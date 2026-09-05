package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Read paths. They are the ones the Python collector served, so a dashboard or
// script pointed at either gets the same answers from the same URLs.
const (
	SummaryPath    = "/api/v1/summary"
	AgentsPath     = "/api/v1/agents"
	TurnsPath      = "/api/v1/turns"
	SamplesPath    = "/api/v1/samples"
	DimensionsPath = "/api/v1/dimensions"
	ExportPath     = "/api/v1/export.csv"
	LivePath       = "/api/v1/live"
)

// keepalive is how long the live stream waits before writing a comment line. A
// proxy or a browser will close a connection that has said nothing for long
// enough, and a quiet fleet is exactly when a live view is left open.
const keepalive = 15 * time.Second

// summaryCacheFor is how long a computed summary is reused.
//
// A dashboard polls, and several tabs polling the same window would each run
// the full aggregation. It is the step a derived window moves in rather than a
// second figure: a cache that outlived the window it was computed for would
// answer one question with another question's numbers.
const summaryCacheFor = derivedWindowStep

// The read routes are not token-gated, and the ingest route is.
//
// The boundary here is the loopback bind, not a credential: these answers are
// read by a page in a browser, and a token the page had to hold would be a
// token in a URL, in the page source, and in the browser's history. Writing is
// different — a forged event enters the record other tools read as fact — so
// ingest keeps its token.
func (s *Server) readRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+SummaryPath, s.readSummary)
	mux.HandleFunc("GET "+AgentsPath, s.readAgents)
	mux.HandleFunc("GET "+AgentsPath+"/{id}/summary", s.readAgentSummary)
	mux.HandleFunc("GET "+TurnsPath, s.readTurns)
	mux.HandleFunc("GET "+TurnsPath+"/{id}", s.readTurn)
	mux.HandleFunc("GET "+SamplesPath, s.readSamples)
	mux.HandleFunc("GET "+DimensionsPath, s.readDimensions)
	mux.HandleFunc("GET "+ExportPath, s.exportTurns)
	mux.HandleFunc("GET "+LivePath, s.live)
}

// filterFrom reads the query, answering the caller directly if it cannot.
func (s *Server) filterFrom(response http.ResponseWriter, request *http.Request) (Filter, bool) {
	filter, err := queryFilters(request.URL.Query(), s.now())
	if err != nil {
		write(response, http.StatusBadRequest, failure{Error: err.Error()})
		return Filter{}, false
	}
	return filter, true
}

// answer writes a result, or a failure that names nothing internal.
func (s *Server) answer(response http.ResponseWriter, route string, body any, err error) {
	if err != nil {
		// A query error can name a column or a file path. The operator reading
		// the log can use that; the caller cannot.
		s.logger.Printf("telemetry: %s: %v", route, err)
		write(response, http.StatusInternalServerError, failure{Error: "query_failure"})
		return
	}
	write(response, http.StatusOK, body)
}

func (s *Server) readSummary(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	summary, err := s.cachedSummary(request.Context(), filter)
	s.answer(response, "summary", summary, err)
}

// cachedSummary computes a summary at most once per window per cache interval.
func (s *Server) cachedSummary(ctx context.Context, filter Filter) (FleetSummary, error) {
	key := filter
	now := s.now()

	s.summaryMutex.Lock()
	if entry, known := s.summaries[key]; known && now.Sub(entry.at) < summaryCacheFor {
		s.summaryMutex.Unlock()
		return entry.summary, nil
	}
	s.summaryMutex.Unlock()

	// Computed outside the lock. Holding it across the query would make every
	// reader of every other window wait for this one.
	summary, err := s.store.Summary(ctx, filter)
	if err != nil {
		return FleetSummary{}, err
	}

	s.summaryMutex.Lock()
	defer s.summaryMutex.Unlock()
	// Cleared rather than evicted one at a time. The cache exists to absorb a
	// handful of dashboards polling the same few windows; a map that grew one
	// entry per distinct filter would be a way to spend this process's memory
	// from a query string.
	if len(s.summaries) >= maximumCachedSummaries {
		s.summaries = map[Filter]cachedSummary{}
	}
	s.summaries[key] = cachedSummary{at: now, summary: summary}
	return summary, nil
}

func (s *Server) readAgents(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	agents, err := s.store.ListAgents(request.Context(), filter,
		queryLimit(request.URL.Query(), defaultListLimit))
	s.answer(response, "agents", map[string]any{"agents": agents}, err)
}

func (s *Server) readAgentSummary(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	summary, found, err := s.store.AgentSummary(request.Context(), request.PathValue("id"), filter)
	if err != nil {
		if errors.Is(err, ErrInvalidIdentifier) {
			write(response, http.StatusBadRequest, failure{Error: "invalid_agent_id"})
			return
		}
		s.answer(response, "agent summary", nil, err)
		return
	}
	if !found {
		write(response, http.StatusNotFound, failure{Error: "agent_not_found"})
		return
	}
	write(response, http.StatusOK, summary)
}

func (s *Server) readTurns(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	limit, offset := queryLimit(query, defaultListLimit), queryOffset(query)
	turns, err := s.store.ListTurns(request.Context(), filter, limit, offset)
	if err != nil {
		s.answer(response, "turns", nil, err)
		return
	}
	// next_offset is present only when a full page came back. Offering one
	// after a short page would invite a caller to walk past the end and read
	// the empty result as the data having changed under it.
	body := map[string]any{"turns": turns, "limit": limit, "offset": offset, "next_offset": nil}
	if len(turns) == limit {
		body["next_offset"] = offset + len(turns)
	}
	write(response, http.StatusOK, body)
}

func (s *Server) readTurn(response http.ResponseWriter, request *http.Request) {
	detail, found, err := s.store.TurnDetail(request.Context(), request.PathValue("id"))
	if err != nil {
		// A malformed identifier and a broken query are told apart here: the
		// first is the caller's to fix, the second is not.
		if errors.Is(err, ErrInvalidIdentifier) {
			write(response, http.StatusBadRequest, failure{Error: "invalid_turn_id"})
			return
		}
		s.answer(response, "turn", nil, err)
		return
	}
	if !found {
		write(response, http.StatusNotFound, failure{Error: "turn_not_found"})
		return
	}
	write(response, http.StatusOK, detail)
}

func (s *Server) readSamples(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	// Only the window and the endpoint narrow shared readings. A GPU sample
	// cannot be filtered by harness without answering a question the data does
	// not support, and silently returning nothing would read as an idle
	// machine.
	shared := Filter{Since: filter.Since, Until: filter.Until, EndpointID: filter.EndpointID}
	samples, err := s.store.ListSamples(request.Context(), shared,
		queryLimit(request.URL.Query(), defaultSampleLimit))
	s.answer(response, "samples", map[string]any{"samples": samples}, err)
}

func (s *Server) readDimensions(response http.ResponseWriter, request *http.Request) {
	dimensions, err := s.store.Dimensions(request.Context())
	s.answer(response, "dimensions", map[string]any{"dimensions": dimensions}, err)
}

// live streams stored events as they arrive.
func (s *Server) live(response http.ResponseWriter, request *http.Request) {
	flusher, streaming := response.(http.Flusher)
	if !streaming {
		write(response, http.StatusInternalServerError, failure{Error: "streaming_unavailable"})
		return
	}
	stream := s.broker.subscribe()
	defer s.broker.unsubscribe(stream)

	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	// A stream a proxy has buffered is not a live stream.
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	if _, err := response.Write([]byte("event: ready\ndata: {\"status\":\"connected\"}\n\n")); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-stream:
			if !open {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := response.Write(append(append([]byte("event: telemetry\ndata: "), payload...), '\n', '\n')); err != nil {
				return
			}
		case <-ticker.C:
			if _, err := response.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}
