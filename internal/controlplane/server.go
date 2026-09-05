package controlplane

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factoryrun"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/review"
)

// The runner records up to 64 MiB before base64 encoding. The JSON envelope remains
// below this limit, including per-event and string-escaping overhead.
const maxCompletionBytes = 96 << 20

const maxRequestBytes = 1 << 20

const maxObservabilityResponseBytes = 1 << 20

// Collector endpoints differ in cost by three orders of magnitude: /healthz
// answers in milliseconds while /api/v1/turns and /api/v1/samples take tens of
// seconds once the telemetry database is large. Only health stays on the
// request path. Every other view is refreshed in the background and served
// from the last good copy, so a slow collector costs freshness, not the screen.
const observabilityHealthTimeout = 5 * time.Second

const observabilityViewTimeout = 90 * time.Second

const observabilityViewRefresh = 20 * time.Second

const observabilityViewRetry = 2 * time.Minute

// observabilityViewGrace bounds how long one request waits for a view that has
// never loaded. It applies only before that view's first success, so a fast
// collector fills the first page load and a slow one does not stall it.
const observabilityViewGrace = 5 * time.Second

const observabilitySummaryRefresh = time.Minute

const observabilitySummaryRetry = 15 * time.Minute

const workerAvailabilityWindow = 15 * time.Second

//go:embed web/dist/* web/dist/assets/*
var webAssets embed.FS

type Server struct {
	store              *Store
	definitionPath     string
	triggers           []config.ResolvedTrigger
	githubRepositories map[string]string
	github             githubTriggerClient
	markers            *factoryrun.Updater
	pullRequests       gitHubPullRequests
	reviews            review.Engine
	schedulerEvery     time.Duration
	now                func() time.Time
	schedulerError     func(error)
	shutdownTimeout    time.Duration
	maxConcurrentJobs  int
	requireFleetLease  bool
	workerToken        string
	csrfToken          string
	handler            http.Handler
	observabilityURL   string
	observabilityHTTP  *http.Client
	observabilityMu    sync.Mutex
	observabilityViews map[string]*observabilityViewState
	observabilityGrace time.Duration
	observabilityFetch time.Duration
}

// observabilityViewState is the last good copy of one collector view together
// with the bookkeeping that keeps at most one refresh of it in flight.
type observabilityViewState struct {
	path      string
	refresh   time.Duration
	retry     time.Duration
	waitFirst bool

	body       json.RawMessage
	fetchedAt  time.Time
	attemptAt  time.Time
	refreshing bool
	loaded     chan struct{}
}

func newObservabilityViews() map[string]*observabilityViewState {
	live := func(path string) *observabilityViewState {
		return &observabilityViewState{
			path: path, refresh: observabilityViewRefresh, retry: observabilityViewRetry,
			waitFirst: true, loaded: make(chan struct{}),
		}
	}
	return map[string]*observabilityViewState{
		"agents":  live("/api/v1/agents?limit=100"),
		"turns":   live("/api/v1/turns?limit=100"),
		"samples": live("/api/v1/samples?limit=500"),
		"summary": {
			path: "/api/v1/summary", refresh: observabilitySummaryRefresh,
			retry: observabilitySummaryRetry, loaded: make(chan struct{}),
		},
	}
}

type ServerOptions struct {
	ObservabilityURL string
	HTTPClient       *http.Client
	// RequireFleetLease turns fleet leasing on. Off, the mechanism does not
	// exist and nothing changes. On, it fails closed: a fleet with no lease,
	// an expired one, or an unreadable one is offered no new work.
	//
	// It is a setting rather than the default because absence-means-refusal
	// would stop every existing deployment on upgrade. Turning it on is the
	// operator stating that they intend to hold that rule.
	RequireFleetLease bool
}

type observabilityResponse struct {
	Enabled   bool            `json:"enabled"`
	Available bool            `json:"available"`
	Error     string          `json:"error,omitempty"`
	Health    json.RawMessage `json:"health,omitempty"`
	Summary   json.RawMessage `json:"summary,omitempty"`
	Agents    json.RawMessage `json:"agents,omitempty"`
	Turns     json.RawMessage `json:"turns,omitempty"`
	Samples   json.RawMessage `json:"samples,omitempty"`
	Pending   []string        `json:"pending,omitempty"`
}

type statusResponse struct {
	Snapshot
	Commands     []string `json:"commands"`
	Repositories []string `json:"repositories"`
	CSRFToken    string   `json:"csrf_token"`
}

type submitRequest struct {
	Prompt        string `json:"prompt"`
	Repository    string `json:"repository"`
	Command       string `json:"command"`
	Model         string `json:"model"`
	ExecutionMode string `json:"execution_mode"`
	Origin        string `json:"origin"`
}

type commandDefinitionResponse struct {
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Timeout  string `json:"timeout"`
	Hash     string `json:"hash"`
	Prompt   string `json:"prompt"`
}

type definitionsResponse struct {
	Commands []commandDefinitionResponse `json:"commands"`
}

type catalogResponse struct {
	Commands     []string `json:"commands"`
	Repositories []string `json:"repositories"`
}

func NewServer(store *Store, definitionPath, workerToken string, maxConcurrentJobs int) (*Server, error) {
	return NewServerWithOptions(store, definitionPath, workerToken, maxConcurrentJobs, ServerOptions{})
}

func NewServerWithOptions(store *Store, definitionPath, workerToken string, maxConcurrentJobs int, options ServerOptions) (*Server, error) {
	if maxConcurrentJobs < 0 {
		return nil, errors.New("max concurrent jobs cannot be negative")
	}
	observabilityURL, err := validateObservabilityURL(options.ObservabilityURL)
	if err != nil {
		return nil, err
	}
	observabilityHTTP := options.HTTPClient
	if observabilityHTTP == nil {
		observabilityHTTP = &http.Client{
			Timeout: observabilityViewTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("observability redirects are disabled")
			},
		}
	}
	csrfToken, err := randomID("csrf", 24)
	if err != nil {
		return nil, err
	}
	managedTriggers, err := config.LoadTriggers(definitionPath)
	if err != nil {
		return nil, err
	}
	startup := time.Now().UTC()
	definitions := make([]TriggerDefinition, 0, len(managedTriggers))
	for _, trigger := range managedTriggers {
		definitions = append(definitions, TriggerDefinition{
			Identity: trigger.Identity, Family: trigger.Family,
			ConfigSignature: trigger.Signature, NextDueAt: trigger.FirstDue(startup),
		})
	}
	if err := store.SyncTriggers(context.Background(), definitions); err != nil {
		return nil, fmt.Errorf("restore managed triggers: %w", err)
	}
	githubRepositories, err := config.LoadGitHubRepositories(definitionPath)
	if err != nil {
		return nil, err
	}
	githubCLI := NewGitHubCLI("gh", 30*time.Second)
	server := &Server{
		store: store, definitionPath: definitionPath, triggers: managedTriggers,
		githubRepositories: githubRepositories,
		github:             githubCLI, markers: factoryrun.NewUpdater(newGitHubMarkerStore(githubCLI)),
		pullRequests: githubCLI, now: time.Now,
		schedulerEvery: 30 * time.Second, shutdownTimeout: 5 * time.Second,
		schedulerError:    func(err error) { log.Printf("scheduler: %v", err) },
		maxConcurrentJobs: maxConcurrentJobs, requireFleetLease: options.RequireFleetLease,
		workerToken: workerToken, csrfToken: csrfToken,
		observabilityURL: observabilityURL, observabilityHTTP: observabilityHTTP,
		observabilityViews: newObservabilityViews(), observabilityGrace: observabilityViewGrace,
		observabilityFetch: observabilityViewTimeout,
	}
	server.handler, err = server.routes()
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	if err := validateLoopbackListen(listen); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	schedulerCtx, stopScheduler := context.WithCancel(ctx)
	defer stopScheduler()
	schedulerDone := make(chan error, 1)
	go func() { schedulerDone <- s.runScheduler(schedulerCtx) }()
	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		var forceCloseErr error
		if shutdownErr != nil {
			forceCloseErr = httpServer.Close()
		}
		// Shutdown can run before Serve registers the listener when the
		// callback cancels the context. Close it explicitly and wait for the
		// serving goroutine so every cancellation path releases the socket.
		closeErr := listener.Close()
		<-done
		var shutdownFailure error
		if shutdownErr != nil {
			shutdownFailure = fmt.Errorf("stop control plane: %w", shutdownErr)
		}
		if forceCloseErr != nil && !errors.Is(forceCloseErr, http.ErrServerClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("force close control plane: %w", forceCloseErr))
		}
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			shutdownFailure = errors.Join(shutdownFailure, fmt.Errorf("stop control plane listener: %w", closeErr))
		}
		return shutdownFailure
	}
	select {
	case err := <-done:
		stopScheduler()
		return errors.Join(err, <-schedulerDone)
	case err := <-schedulerDone:
		if shutdownErr := stopHTTP(); shutdownErr != nil && err == nil {
			return shutdownErr
		}
		return err
	case <-ctx.Done():
		stopScheduler()
		shutdownErr := stopHTTP()
		return errors.Join(shutdownErr, <-schedulerDone)
	}
}

func (s *Server) runScheduler(ctx context.Context) error {
	var schedulers sync.WaitGroup
	loop := func(delayFirst bool, work func(context.Context) error) {
		schedulers.Add(1)
		go func() {
			defer schedulers.Done()
			if delayFirst && !sleep(ctx, s.schedulerEvery) {
				return
			}
			for {
				s.reportSchedulerError(work(ctx))
				if !sleep(ctx, s.schedulerEvery) {
					return
				}
			}
		}()
	}
	for _, trigger := range s.triggers {
		loop(false, func(ctx context.Context) error {
			if err := s.processManagedTrigger(ctx, trigger); err != nil {
				return fmt.Errorf("trigger %q: %w", trigger.Identity, err)
			}
			return nil
		})
	}
	loop(true, s.maintainState)
	loop(true, s.publishFactoryRunMarkers)
	loop(true, s.assignReviewers)
	<-ctx.Done()
	schedulers.Wait()
	return nil
}

// sleep waits for the duration and reports false when ctx ends first.
func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) maintainState(ctx context.Context) error {
	_, reclaimErr := s.store.ReclaimExpiredLeases(ctx)
	_, pruneErr := s.store.PruneSupersededWorkers(ctx, s.store.now().UTC().Add(-workerAvailabilityWindow))
	return errors.Join(reclaimErr, pruneErr)
}

func (s *Server) reportSchedulerError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && s.schedulerError != nil {
		s.schedulerError(err)
	}
}

func (s *Server) routes() (http.Handler, error) {
	dist, err := fs.Sub(webAssets, "web/dist")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/definitions", s.definitions)
	mux.HandleFunc("GET /api/v1/observability", s.observability)
	mux.HandleFunc("POST /api/v1/jobs", s.authorizeSubmission(s.submit))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.authorizeSubmission(s.cancelJob))
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.authorizeSubmission(s.deleteJob))
	mux.HandleFunc("GET /api/v1/leases", s.readLeases)
	mux.HandleFunc("GET /api/v1/board", s.board)
	mux.HandleFunc("GET /api/v1/claims", s.readClaims)
	mux.HandleFunc("POST /api/v1/claims", s.authorizeSubmission(s.writeClaim))
	mux.HandleFunc("POST /api/v1/leases", s.authorizeSubmission(s.writeLease))
	mux.HandleFunc("POST /api/v1/workers/poll", s.authorizeWorker(s.poll))
	mux.HandleFunc("POST /api/v1/runs/{id}/heartbeat", s.authorizeWorker(s.heartbeat))
	mux.HandleFunc("POST /api/v1/runs/{id}/terminal", s.authorizeWorker(s.bindTerminal))
	mux.HandleFunc("POST /api/v1/runs/{id}/complete", s.authorizeWorker(s.complete))
	mux.HandleFunc("POST /api/v1/runs/{id}/review", s.authorizeWorker(s.submitReview))
	mux.Handle("/", http.FileServer(http.FS(dist)))
	return securityHeaders(mux), nil
}

func validateObservabilityURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || (parsed.Path != "" && parsed.Path != "/") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("observability URL must be a literal loopback http://127.0.0.1 origin")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func (s *Server) observability(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.observabilityURL == "" {
		writeJSON(response, http.StatusOK, observabilityResponse{Enabled: false})
		return
	}
	healthCtx, cancel := context.WithTimeout(request.Context(), observabilityHealthTimeout)
	health, healthErr := s.fetchObservability(healthCtx, "/healthz")
	cancel()
	payload := observabilityResponse{Enabled: true, Available: healthErr == nil, Health: health}
	s.refreshObservabilityViews()
	s.awaitFirstObservabilityViews(request.Context())
	for _, name := range []string{"agents", "turns", "samples", "summary"} {
		body, loaded := s.observabilityViewBody(name)
		if !loaded {
			payload.Pending = append(payload.Pending, name)
			continue
		}
		switch name {
		case "agents":
			payload.Agents = body
		case "turns":
			payload.Turns = body
		case "samples":
			payload.Samples = body
		case "summary":
			payload.Summary = body
		}
	}
	switch {
	case healthErr != nil:
		payload.Error = "observability collector unavailable or returned invalid data"
	case len(payload.Pending) > 0:
		payload.Error = "some observability views are still loading; showing available data"
	}
	writeJSON(response, http.StatusOK, payload)
}

// refreshObservabilityViews starts a background fetch for every view whose copy
// is stale, keeping at most one fetch per view in flight. A view that failed
// backs off for its retry interval so a broken collector is not hammered.
func (s *Server) refreshObservabilityViews() {
	now := time.Now()
	s.observabilityMu.Lock()
	defer s.observabilityMu.Unlock()
	for _, view := range s.observabilityViews {
		if view.refreshing {
			continue
		}
		if !view.fetchedAt.IsZero() && now.Sub(view.fetchedAt) < view.refresh {
			continue
		}
		if !view.attemptAt.IsZero() && !view.attemptAt.Before(view.fetchedAt) && now.Sub(view.attemptAt) < view.retry {
			continue
		}
		view.refreshing = true
		view.attemptAt = now
		go s.fetchObservabilityView(view)
	}
}

func (s *Server) fetchObservabilityView(view *observabilityViewState) {
	ctx, cancel := context.WithTimeout(context.Background(), s.observabilityFetch)
	defer cancel()
	body, err := s.fetchObservability(ctx, view.path)
	s.observabilityMu.Lock()
	defer s.observabilityMu.Unlock()
	view.refreshing = false
	if err != nil {
		return
	}
	first := view.fetchedAt.IsZero()
	view.body = slices.Clone(body)
	view.fetchedAt = time.Now()
	if first {
		close(view.loaded)
	}
}

// awaitFirstObservabilityViews gives a live view that has never loaded a
// bounded chance to arrive, so a responsive collector fills the first page
// load. A view already holding a copy is never waited on again.
func (s *Server) awaitFirstObservabilityViews(ctx context.Context) {
	var pending []<-chan struct{}
	s.observabilityMu.Lock()
	for _, view := range s.observabilityViews {
		if view.waitFirst && view.fetchedAt.IsZero() && view.refreshing {
			pending = append(pending, view.loaded)
		}
	}
	s.observabilityMu.Unlock()
	if len(pending) == 0 {
		return
	}
	grace := time.NewTimer(s.observabilityGrace)
	defer grace.Stop()
	for _, loaded := range pending {
		select {
		case <-loaded:
		case <-grace.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) observabilityViewBody(name string) (json.RawMessage, bool) {
	s.observabilityMu.Lock()
	defer s.observabilityMu.Unlock()
	view := s.observabilityViews[name]
	if view == nil || view.fetchedAt.IsZero() {
		return nil, false
	}
	return slices.Clone(view.body), true
}

func (s *Server) fetchObservability(ctx context.Context, path string) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.observabilityURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.observabilityHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("collector returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxObservabilityResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxObservabilityResponseBytes {
		return nil, errors.New("collector response exceeds limit")
	}
	if !json.Valid(body) {
		return nil, errors.New("collector response is not JSON")
	}
	return body, nil
}

func (s *Server) definitions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	commands := make([]commandDefinitionResponse, 0, len(definition.Commands))
	for _, name := range definition.CommandNames() {
		command, err := definition.ResolveCommand(name)
		if err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		commands = append(commands, commandDefinitionResponse{Name: command.Name, Executor: command.Executor, Timeout: command.Timeout.String(), Hash: command.Hash, Prompt: command.Prompt})
	}
	writeJSON(response, http.StatusOK, definitionsResponse{Commands: commands})
}

func (s *Server) status(response http.ResponseWriter, request *http.Request) {
	if err := s.maintainState(request.Context()); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	snapshot, err := s.store.Snapshot(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := s.store.now().UTC()
	for index := range snapshot.Workers {
		snapshot.Workers[index].Connected = !snapshot.Workers[index].LastSeenAt.Before(now.Add(-workerAvailabilityWindow))
	}
	repositories, repositoryErr := s.store.AvailableRepositories(request.Context(), now.Add(-workerAvailabilityWindow))
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	writeJSON(response, http.StatusOK, statusResponse{
		Snapshot:     snapshot,
		Commands:     definition.CommandNames(),
		Repositories: repositories,
		CSRFToken:    s.csrfToken,
	})
}

func (s *Server) catalog(response http.ResponseWriter, request *http.Request) {
	definition, err := config.LoadDefinitions(s.definitionPath)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, catalogResponse{
		Commands:     definition.CommandNames(),
		Repositories: repositories,
	})
}

func (s *Server) submit(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input submitRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.Repository) == "" {
		writeError(response, http.StatusBadRequest, errors.New("repository is required"))
		return
	}
	repositories, repositoryErr := s.store.KnownRepositories(request.Context())
	if repositoryErr != nil {
		writeError(response, http.StatusInternalServerError, repositoryErr)
		return
	}
	if !slices.Contains(repositories, input.Repository) {
		writeError(response, http.StatusBadRequest, fmt.Errorf("repository %q is not defined in the control plane", input.Repository))
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if len(input.Model) > 128 || strings.ContainsAny(input.Model, "\x00\r\n") {
		writeError(response, http.StatusBadRequest, errors.New("model must be at most 128 characters on one line"))
		return
	}
	if strings.TrimSpace(input.Command) == "" {
		writeError(response, http.StatusBadRequest, errors.New("command is required"))
		return
	}
	command, err := config.LoadCommand(s.definitionPath, input.Command)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	command, err = config.RenderPrompt(command, input.Prompt)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	command.Model = input.Model
	input.ExecutionMode = strings.ToLower(strings.TrimSpace(input.ExecutionMode))
	if input.ExecutionMode == "" {
		input.ExecutionMode = "process"
	}
	if input.ExecutionMode != "process" && input.ExecutionMode != "herdr" {
		writeError(response, http.StatusBadRequest, errors.New("execution_mode must be process or herdr"))
		return
	}
	input.Origin = strings.ToLower(strings.TrimSpace(input.Origin))
	if input.Origin == "" {
		input.Origin = "machinist"
	}
	if len(input.Origin) > 64 || !validOrigin(input.Origin) {
		writeError(response, http.StatusBadRequest, errors.New("origin is invalid"))
		return
	}
	jobID, err := s.store.CreateJobWithOptions(request.Context(), input.Prompt, input.Repository, input.Command, command, CreateJobOptions{ExecutionMode: input.ExecutionMode, Origin: input.Origin})
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]string{"id": jobID})
}

func validOrigin(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) bindTerminal(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.BindTerminalRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" || input.AttemptID == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id, lease_token, and attempt_id are required"))
		return
	}
	err := s.store.BindTerminal(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteJob(response http.ResponseWriter, request *http.Request) {
	err := s.store.DeleteJob(request.Context(), request.PathValue("id"))
	if errors.Is(err, ErrJobActive) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) cancelJob(response http.ResponseWriter, request *http.Request) {
	err := s.store.CancelJob(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// leaseView is what a lease looks like from outside. It carries the answer the
// mechanism actually gives — may this fleet take work, right now — rather than
// leaving every reader to recompute it from the state and the expiry and get
// the expired-but-allowed case wrong.
type leaseView struct {
	Lease
	Allowed  bool `json:"allowed"`
	Required bool `json:"required"`
}

// claimView is what a claim looks like from outside. It carries whether the
// claim is still live, because every reader that recomputes that from a state
// and an expiry is one more place the expired-but-held case can be got wrong.
type claimView struct {
	Claim
	Live bool `json:"live"`
}

func (s *Server) readClaims(response http.ResponseWriter, request *http.Request) {
	claims, err := s.store.Claims(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := s.store.now().UTC()
	views := make([]claimView, 0, len(claims))
	for _, claim := range claims {
		views = append(views, claimView{Claim: claim, Live: claim.Live(now)})
	}
	writeJSON(response, http.StatusOK, map[string]any{"claims": views})
}

type claimRequest struct {
	Action     string `json:"action"`
	Repository string `json:"repository"`
	Issue      int    `json:"issue"`
	Holder     string `json:"holder"`
	Branch     string `json:"branch"`
	Reason     string `json:"reason"`
	Transfer   string `json:"transfer"`
	ExpiresAt  string `json:"expires_at"`
}

func (s *Server) writeClaim(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input claimRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	// The expiry is parsed here because the wire carries it as text and the
	// store does not. Everything else a claim may say is left to the store, so
	// there is one gate rather than two that can drift.
	var expires time.Time
	if trimmed := strings.TrimSpace(input.ExpiresAt); trimmed != "" {
		parsed, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			writeError(response, http.StatusBadRequest, fmt.Errorf("expires_at must be an RFC 3339 time: %w", err))
			return
		}
		expires = parsed
	}
	var stored Claim
	var err error
	switch strings.TrimSpace(input.Action) {
	case "take":
		stored, err = s.store.TakeClaim(request.Context(), Claim{
			Repository: input.Repository, Issue: input.Issue, Holder: input.Holder,
			Branch: input.Branch, Reason: input.Reason, ExpiresAt: expires,
		})
	case "release":
		stored, err = s.store.ReleaseClaim(request.Context(), input.Repository, input.Issue, input.Holder, input.Reason)
	case "hold":
		stored, err = s.store.HoldClaim(request.Context(), input.Repository, input.Issue,
			input.Holder, input.Reason, expires, input.Transfer)
	default:
		// An unrecognised action is refused rather than defaulted. Guessing
		// which transition was meant is guessing about whether work is being
		// taken away from someone.
		writeError(response, http.StatusBadRequest,
			fmt.Errorf("unknown claim action %q: expected take, release or hold", input.Action))
		return
	}
	if err != nil {
		writeClaimError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, claimView{Claim: stored, Live: stored.Live(s.store.now().UTC())})
}

// writeClaimError separates the caller's mistakes from this control plane's. A
// refused claim is a 409 and not a 400: the request was well formed and the
// answer is that somebody else has the work, which is a different thing for the
// caller to do something about.
func writeClaimError(response http.ResponseWriter, err error) {
	var taken *ErrClaimTaken
	if errors.As(err, &taken) {
		writeError(response, http.StatusConflict, err)
		return
	}
	var missing *ErrNoClaim
	if errors.As(err, &missing) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, ErrInvalidClaim) {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	writeError(response, http.StatusInternalServerError, err)
}

// board serves the lane view of everything this control plane is tracking. It
// is a read of state the control plane already owns, so it needs no
// authorization beyond reaching the port, exactly like the status view.
func (s *Server) board(response http.ResponseWriter, request *http.Request) {
	view, err := s.store.Board(request.Context())
	if err != nil {
		// The board is never served half-read. An empty board is a statement
		// that there is no work, and this is not that.
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) readLeases(response http.ResponseWriter, request *http.Request) {
	leases, err := s.store.Leases(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := s.store.now().UTC()
	views := make([]leaseView, 0, len(leases))
	for _, lease := range leases {
		views = append(views, leaseView{Lease: lease, Allowed: lease.Allows(now), Required: s.requireFleetLease})
	}
	writeJSON(response, http.StatusOK, map[string]any{"leases": views, "required": s.requireFleetLease})
}

type leaseRequest struct {
	Fleet     string `json:"fleet"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
	Reason    string `json:"reason"`
}

func (s *Server) writeLease(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input leaseRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	// The expiry is parsed here because the wire carries it as text and the
	// store does not. Everything else about the lease is left to SetLease, so
	// there is one place — not two that can drift — deciding what a lease may
	// say.
	expires, err := time.Parse(time.RFC3339, strings.TrimSpace(input.ExpiresAt))
	if err != nil {
		writeError(response, http.StatusBadRequest, fmt.Errorf("expires_at must be an RFC 3339 time: %w", err))
		return
	}
	lease := Lease{Fleet: input.Fleet, State: LeaseState(input.State), ExpiresAt: expires, Reason: input.Reason}
	if err := s.store.SetLease(request.Context(), lease); err != nil {
		if errors.Is(err, ErrInvalidLease) {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		// The lease was well formed and the write still failed, which is this
		// control plane's problem and not the operator's to fix by editing it.
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	stored, err := s.store.Lease(request.Context(), lease.Fleet)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, leaseView{
		Lease: stored, Allowed: stored.Allows(s.store.now().UTC()), Required: s.requireFleetLease})
}

func (s *Server) poll(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.PollRequest
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if strings.TrimSpace(input.InstanceID) == "" || strings.TrimSpace(input.Name) == "" {
		writeError(response, http.StatusBadRequest, errors.New("worker instance_id and name are required"))
		return
	}
	run, err := s.store.poll(request.Context(), input, s.maxConcurrentJobs, s.requireFleetLease)
	var refused *ErrFleetRefused
	if errors.As(err, &refused) {
		// A refusal is not a failure. The worker is working correctly and is
		// being told not to take work, which is an ordinary answer to a poll
		// and must not look to the worker like the control plane is broken.
		writeJSON(response, http.StatusOK, protocol.PollResponse{Refused: refused.Error()})
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, protocol.PollResponse{Run: run})
}

func (s *Server) complete(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxCompletionBytes) {
		return
	}
	var input protocol.Completion
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	err := s.store.Complete(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if errors.Is(err, ErrInvalidCompletion) {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		log.Printf("complete run %q: %v", request.PathValue("id"), err)
		writeError(response, http.StatusInternalServerError, errors.New("complete run"))
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) heartbeat(response http.ResponseWriter, request *http.Request) {
	if !limitRequestBody(response, request, maxRequestBytes) {
		return
	}
	var input protocol.Heartbeat
	if err := decodeJSON(request, &input); err != nil {
		writeDecodeError(response, err)
		return
	}
	if input.InstanceID == "" || input.LeaseToken == "" {
		writeError(response, http.StatusBadRequest, errors.New("instance_id and lease_token are required"))
		return
	}
	result, err := s.store.Heartbeat(request.Context(), request.PathValue("id"), input)
	if errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrRunState) {
		writeError(response, http.StatusConflict, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(response, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) authorizeWorker(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !s.validBearerRequest(request) {
			writeUnauthorized(response)
			return
		}
		next(response, request)
	}
}

func (s *Server) authorizeSubmission(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			if !s.validBearerRequest(request) {
				writeUnauthorized(response)
				return
			}
		} else if !s.validBrowserRequest(request) {
			writeError(response, http.StatusForbidden, errors.New("invalid submission origin or CSRF token"))
			return
		}
		next(response, request)
	}
}

func (s *Server) validBearerRequest(request *http.Request) bool {
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	return provided != request.Header.Get("Authorization") && subtle.ConstantTimeCompare([]byte(provided), []byte(s.workerToken)) == 1
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	writeError(response, http.StatusUnauthorized, errors.New("invalid worker token"))
}

func (s *Server) validBrowserRequest(request *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Machinist-CSRF")), []byte(s.csrfToken)) != 1 {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != "http" || !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	hostname := origin.Hostname()
	return hostname == "localhost" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback()
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must use a loopback host", listen)
	}
	return nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON: %w", err)
		}
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func limitRequestBody(response http.ResponseWriter, request *http.Request, limit int64) bool {
	if request.ContentLength > limit {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	return true
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}

func writeDecodeError(response http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(response, http.StatusRequestEntityTooLarge, errors.New("request body is too large"))
		return
	}
	writeError(response, http.StatusBadRequest, err)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}
