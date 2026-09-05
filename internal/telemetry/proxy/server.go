package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	}
	server.proxy = server.newReverseProxy()
	return server
}

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
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
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
	spanID    string
	started   time.Time
	firstByte time.Time
	status    int
	failed    bool
}

type callKey struct{}

func (s *Server) forward(response http.ResponseWriter, request *http.Request) {
	measured := &call{spanID: newIdentifier(), started: s.now()}
	s.emit(ModelRequestStarted, measured, s.now(), map[string]any{
		"correlation":         "unavailable",
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
		"correlation":         "unavailable",
		"measurement_quality": "exact",
	}
	if !measured.firstByte.IsZero() {
		attributes["first_byte_ms"] = milliseconds(measured.firstByte.Sub(measured.started))
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
	response.Body = &tap{ReadCloser: response.Body}
	return nil
}

// tap counts a body without holding it. The count is not used yet; the reader
// exists so the response is read through one place, which is where the stream
// inspection attaches.
type tap struct {
	io.ReadCloser
	bytes int64
}

func (t *tap) Read(buffer []byte) (int, error) {
	read, err := t.ReadCloser.Read(buffer)
	t.bytes += int64(read)
	return read, err
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
			"correlation":         "unavailable",
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
	s.sink.Enqueue(Event{
		EventID:    newIdentifier(),
		EventType:  eventType,
		ObservedAt: timestamp(at),
		SpanID:     measured.spanID,
		Producer:   s.identity,
		Agent: Agent{
			// Until a correlation context says otherwise, a model call belongs
			// to the endpoint that served it rather than to an agent. Naming a
			// turn here without evidence would attribute one agent's latency
			// to another.
			ID:          s.settings.endpointID,
			DisplayName: s.settings.model,
		},
		Attributes: attributes,
	})
}

// Serve listens and forwards until the context is done.
func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.settings.listen)
	if err != nil {
		return fmt.Errorf("the model proxy could not listen on %s: %w", s.settings.listen, err)
	}
	s.mutex.Lock()
	s.listener = listener
	s.mutex.Unlock()

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

func newIdentifier() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail on any platform this runs on, and an
		// identifier derived from something weaker would collide silently.
		panic("machinist proxy: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(buffer)
}
