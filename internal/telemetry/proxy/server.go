package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is recorded on every event so a reader can tell which proxy produced
// a measurement.
const Version = "0.1.0"

// Server forwards model requests to one upstream and measures them.
type Server struct {
	settings Settings
	sink     Sink
	proxy    *httputil.ReverseProxy
	now      func() time.Time

	// identity is this process's producer record. It is fixed at construction
	// because a measurement whose producer changed mid-run cannot be grouped
	// with the ones around it.
	identity Producer
	registry *Registry

	mutex    sync.Mutex
	listener net.Listener
}

// New builds a proxy from validated settings. A nil sink measures nothing and
// still forwards: running without a collector is a supported deployment.
func New(settings Settings, sink Sink) *Server {
	if sink == nil {
		sink = discard{}
	}
	server := &Server{
		settings: settings, sink: sink, now: time.Now,
		identity: Producer{
			Name: "machinist-model-proxy", Version: Version, InstanceID: newIdentifier(),
		},
		// Until a turn says otherwise a model call belongs to the endpoint that
		// served it rather than to an agent. Naming a turn here without
		// evidence would attribute one agent's latency to another.
		registry: NewRegistry(Context{
			AgentID:     settings.endpointID,
			DisplayName: settings.model,
			Model:       settings.model,
			EndpointID:  settings.endpointID,
		}),
	}
	server.proxy = server.newReverseProxy()
	return server
}

// Registry is the proxy's view of which turns are running. It is exposed so a
// process that embeds the proxy can report turns directly rather than through
// the authenticated route, which exists for the harnesses that cannot.
func (s *Server) Registry() *Registry { return s.registry }

// newReverseProxy builds the forwarder.
//
// httputil.ReverseProxy is used rather than a hand-written copy loop because
// the parts that are easy to get wrong — hop-by-hop headers, the tokens named
// in a Connection header, when to flush a stream — are the parts it already
// gets right. What it does not do is measure, so the measurement is attached
// at the two points where it can be taken without touching the bytes:
// Transport, which sees the round trip, and a wrapper on the response body,
// which sees when the bytes arrive.
func (s *Server) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			target := *s.settings.upstream
			target.Path = s.settings.upstream.Path + request.In.URL.Path
			target.RawQuery = request.In.URL.RawQuery
			request.Out.URL = &target
			request.Out.Host = target.Host

			// The correlation headers are this system's own and mean nothing
			// upstream. Forwarding them would send a turn identifier to a
			// third party for no reason at all.
			for name := range request.Out.Header {
				if strings.HasPrefix(strings.ToLower(name), ContextHeaderPrefix) {
					request.Out.Header.Del(name)
				}
			}
			// Identity encoding, because a compressed response cannot be read
			// for the timing of its first generated token without being
			// decompressed — and decompressing it is reading the content.
			request.Out.Header.Set("Accept-Encoding", "identity")
			// No X-Forwarded-* is added. The upstream is a model endpoint, not
			// a service that should learn anything about the machine calling
			// it, and ReverseProxy would otherwise offer it one.
		},
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: s.settings.connectTimeout}).DialContext,
			// Compression off for the same reason the header is set: a body
			// this proxy cannot read is a body it cannot time.
			DisableCompression:    true,
			ResponseHeaderTimeout: s.settings.responseTimeout,
			ForceAttemptHTTP2:     true,
			MaxIdleConnsPerHost:   8,
		},
		// Every write is flushed. A streaming model response that this proxy
		// buffered would arrive at the harness in bursts, and the harness's
		// own first-token measurement would be measuring this process.
		FlushInterval:  -1,
		ErrorLog:       nil,
		ModifyResponse: s.observe,
		ErrorHandler:   s.upstreamFailed,
	}
}

// Handler is the proxy's routes.
//
// The context route is matched before the forwarding gate, and its path is not
// one the gate would forward: a request that reaches the upstream must be a
// model request, and a request that reaches the registry must have carried the
// token. Neither can be reached by asking for the other.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ContextPath, s.serveContext)
	mux.HandleFunc("DELETE "+ContextPath, s.serveContext)
	mux.HandleFunc("/", s.serve)
	return mux
}

// serve gates a request and then forwards it.
func (s *Server) serve(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		refuse(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	// The path is checked against the closed set before anything else happens.
	// This is the whole of what stops a proxy holding a harness's credentials
	// from being asked to send them somewhere else.
	if !s.settings.Allows(request.URL.Path) {
		refuse(response, http.StatusNotFound, "path_not_forwarded")
		return
	}
	if request.ContentLength < 0 {
		// A chunked upload has no declared length, so there is no size to
		// refuse before reading it. Requiring the length makes the limit
		// enforceable rather than advisory.
		refuse(response, http.StatusLengthRequired, "content_length_required")
		return
	}
	if request.ContentLength > MaximumRequestBytes {
		refuse(response, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	s.forward(response, request)
}

// call is one measured request. It exists so the timings taken in three
// different places — before the round trip, in the body wrapper, at the end —
// are one record rather than variables shared by closures.
type call struct {
	spanID string
	// context is who the call belongs to, resolved once when it starts. It is
	// held for the life of the call so that every event about one call carries
	// the same attribution: a turn that ended mid-generation must not make the
	// completion land on a different agent than the start did.
	context   Context
	started   time.Time
	firstByte time.Time
	// firstGenerated is when the first token the model produced reached the
	// client. It is distinct from firstByte, which is when the headers came
	// back: between the two sit the provider's preamble and, on a reasoning
	// model, an arbitrary amount of thinking.
	firstGenerated time.Time
	status         int
	failed         bool
	read           *inspector
}

type callKey struct{}

func (s *Server) forward(response http.ResponseWriter, request *http.Request) {
	measured := &call{
		spanID:  newIdentifier(),
		context: s.registry.Resolve(identifierOrEmpty(request.Header.Get(ContextHeaderPrefix + "context-id"))),
		started: s.now(),
	}
	s.emit(ModelRequestStarted, measured, s.now(), map[string]any{
		"measurement_quality": "exact",
	})

	request = request.WithContext(context.WithValue(request.Context(), callKey{}, measured))
	s.proxy.ServeHTTP(response, request)

	// A failure has already reported itself, with the reason it knows and this
	// point does not.
	if measured.failed {
		return
	}
	finished := s.now()
	attributes := map[string]any{
		"duration_ms":         milliseconds(finished.Sub(measured.started)),
		"http_status":         measured.status,
		"measurement_quality": "exact",
	}
	if !measured.firstByte.IsZero() {
		attributes["first_byte_ms"] = milliseconds(measured.firstByte.Sub(measured.started))
	}
	if !measured.firstGenerated.IsZero() {
		// Decoding is the part of the call the model spent producing output,
		// as distinct from the part it spent before producing any. Reported
		// separately because they have different causes: the first is the
		// model's speed, the second is queueing, prompt processing and
		// reasoning.
		attributes["decode_ms"] = milliseconds(finished.Sub(measured.firstGenerated))
	}
	if measured.read != nil {
		measured.read.attributes(attributes)
	}
	if measured.status < 400 && measured.read != nil && measured.read.streamError != "" {
		// The headers said the call succeeded and then the stream said it did
		// not. The stream is the later and more specific evidence, so it wins;
		// recording this as a completion would put a success in the table for
		// a call that produced an error.
		attributes["error_category"] = "stream_error"
		attributes["error_code"] = measured.read.streamError
		s.emit(ModelFailed, measured, finished, attributes)
		return
	}
	if measured.status >= 400 {
		// An upstream that answered 429 answered. The request was not lost,
		// and calling it a transport failure would put it in the same bucket
		// as an endpoint that was not there.
		attributes["error_category"] = "upstream_http"
		attributes["error_code"] = "http_" + strconv.Itoa(measured.status)
		s.emit(ModelFailed, measured, finished, attributes)
		return
	}
	s.emit(ModelCompleted, measured, finished, attributes)
}

// observe records when the response began and taps the body for when it ends.
func (s *Server) observe(response *http.Response) error {
	measured, ok := response.Request.Context().Value(callKey{}).(*call)
	if !ok {
		return nil
	}
	measured.status = response.StatusCode
	measured.firstByte = s.now()
	measured.read = newInspector(response.Header.Get("Content-Type"))
	response.Body = &tap{ReadCloser: response.Body, server: s, call: measured}
	return nil
}

// tap reads the response as the client receives it.
//
// It sits here rather than around the whole body because the point of the
// measurement is when the client could have seen a token, and the only place
// that is known is the moment the bytes pass through. Nothing it does can
// change what passes: it returns exactly what it read, and the inspection
// happens after the bytes are already in the caller's buffer.
type tap struct {
	io.ReadCloser
	server   *Server
	call     *call
	bytes    int64
	finished bool
}

func (t *tap) Read(buffer []byte) (int, error) {
	read, err := t.ReadCloser.Read(buffer)
	t.bytes += int64(read)
	at := t.server.now()
	if read > 0 {
		t.call.read.feed(buffer[:read], at)
	}
	if err != nil && !t.finished {
		// Any terminal error ends the reading, not only io.EOF: a stream cut
		// short still reported whatever usage it managed to send, and that is
		// more than nothing to record about it.
		t.finished = true
		t.call.read.finish(at)
	}
	t.server.noteFirstToken(t.call)
	return read, err
}

// Close finishes the inspection for a body that was closed rather than read to
// its end, which is what happens when the client goes away mid-generation.
func (t *tap) Close() error {
	if !t.finished {
		t.finished = true
		t.call.read.finish(t.server.now())
		t.server.noteFirstToken(t.call)
	}
	return t.ReadCloser.Close()
}

// noteFirstToken emits model.first_token the first time generation is seen.
func (s *Server) noteFirstToken(measured *call) {
	if measured.read == nil || measured.read.firstGenerated.IsZero() || !measured.firstGenerated.IsZero() {
		return
	}
	measured.firstGenerated = measured.read.firstGenerated
	s.emit(ModelFirstToken, measured, measured.firstGenerated, map[string]any{
		"elapsed_ms":          milliseconds(measured.firstGenerated.Sub(measured.started)),
		"measurement_quality": "exact",
	})
}

// upstreamFailed answers a request whose upstream could not be reached or
// stopped speaking.
//
// The message names the proxy rather than repeating the transport error. A
// harness renders what it gets back, and a transport error can carry the
// upstream host and the reason a TLS handshake failed.
func (s *Server) upstreamFailed(response http.ResponseWriter, request *http.Request, err error) {
	measured, ok := request.Context().Value(callKey{}).(*call)
	if ok {
		measured.failed = true
		finished := s.now()
		attributes := map[string]any{
			"duration_ms":         milliseconds(finished.Sub(measured.started)),
			"http_status":         http.StatusBadGateway,
			"error_category":      "upstream_transport",
			"error_code":          transportCode(err),
			"measurement_quality": "exact",
		}
		if !measured.firstByte.IsZero() {
			attributes["first_byte_ms"] = milliseconds(measured.firstByte.Sub(measured.started))
			// The response had already started, so the harness has a truncated
			// body and no status left to change. Saying so in the event is the
			// only place it can be said.
			attributes["http_status"] = measured.status
			attributes["error_code"] = "response_truncated"
		}
		s.emit(ModelFailed, measured, finished, attributes)
		if !measured.firstByte.IsZero() {
			return
		}
	}
	refuse(response, http.StatusBadGateway, "upstream_unavailable")
}

// transportCode reduces a transport error to one of a few names.
//
// A category rather than the message, because the message is the upstream's
// and this value is stored, grouped and counted. Two timeouts should be one
// row.
func transportCode(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, context.Canceled):
		return "client_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "upstream_timeout"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "upstream_timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "upstream_unresolvable"
	}
	var operation *net.OpError
	if errors.As(err, &operation) {
		return "upstream_unreachable"
	}
	return "connection_or_protocol_failure"
}

func (s *Server) emit(eventType string, measured *call, at time.Time, attributes map[string]any) {
	attributes["correlation"] = measured.context.Correlation
	agent := Agent{ID: measured.context.AgentID, DisplayName: measured.context.DisplayName}
	s.sink.Enqueue(Event{
		SchemaVersion: SchemaVersion,
		EventID:       newIdentifier(),
		EventType:     eventType,
		ObservedAt:    timestamp(at),
		// Measured against the start of this call rather than a process clock,
		// because the number's only use is ordering events within one call and
		// a wall clock can move underneath them.
		MonotonicOffsetMS: milliseconds(at.Sub(measured.started)),
		Producer:          s.identity,
		Agent:             agent,
		Harness:           optional(measured.context.Harness),
		Model:             optional(measured.context.Model),
		EndpointID:        optional(measured.context.EndpointID),
		SessionID:         optional(measured.context.SessionID),
		TurnID:            optional(measured.context.TurnID),
		SpanID:            optional(measured.spanID),
		// The turn is the parent of the model call it made. Where no turn was
		// resolved this is null, which is the honest answer: the call had a
		// parent, and this proxy does not know which.
		ParentSpanID: optional(measured.context.TurnID),
		Attributes:   attributes,
	})
}

// Serve listens and forwards until the context is done.
func (s *Server) Serve(ctx context.Context, onListening func(net.Addr)) error {
	listener, err := net.Listen("tcp", s.settings.listen)
	if err != nil {
		return fmt.Errorf("the model proxy could not listen on %s: %w", s.settings.listen, err)
	}
	s.mutex.Lock()
	s.listener = listener
	s.mutex.Unlock()
	// Reported after the bind, because the port is usually ephemeral and the
	// harness that has to be pointed here cannot be told an address that does
	// not exist yet.
	if onListening != nil {
		onListening(listener.Addr())
	}

	server := &http.Server{
		Handler: s.Handler(),
		// No read or write deadline. A model call is a long-lived stream, and
		// a deadline here would cut off a generation that was proceeding
		// normally. The bounds that matter — reaching the upstream, and the
		// upstream falling silent — are on the transport, where they can tell
		// a slow answer from no answer.
		ReadHeaderTimeout: 30 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Shutdown rather than Close, so a generation in flight finishes
		// rather than becoming a truncated response the harness has to
		// interpret.
		_ = server.Shutdown(shutdown)
		return nil
	}
}

// Address is where the proxy is listening, once it is. A zero port in the
// configuration means the answer is not known until then, which is why this
// exists rather than the caller reading the setting.
func (s *Server) Address() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// URL is the base URL to give a model client.
func (s *Server) URL() string {
	address := s.Address()
	if address == "" {
		return ""
	}
	return (&url.URL{Scheme: "http", Host: address}).String()
}

func refuse(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	// Shaped like the error a model client already knows how to render, so a
	// refusal from the proxy is legible in the same place as one from the
	// model endpoint.
	fmt.Fprintf(response, `{"error":{"type":"proxy_error","code":%q}}`, code)
}

func milliseconds(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return float64(d.Nanoseconds()) / 1e6
}
