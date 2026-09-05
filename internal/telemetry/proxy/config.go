// Package proxy is a content-blind timing proxy for OpenAI- and
// Anthropic-compatible model endpoints.
//
// A harness points its model base URL at this process, which forwards every
// request to one upstream fixed at startup and measures what it can see from
// the outside: when the connection opened, when the first response byte
// arrived, when the last one did. Request bodies pass through without being
// parsed, and neither they nor the credentials that accompany them are read,
// logged or stored.
//
// The measurement is the reason it exists. A harness reports what it believes
// about its own timing; this reports what the wire did.
package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Limits on what one request may be. They are bounds on this process's memory
// and on how long a stalled upstream can hold a connection, not opinions about
// what a model call should look like.
const (
	// MaximumRequestBytes is refused above rather than buffered. The body is
	// streamed, so this is a declared-length check and not an allocation.
	MaximumRequestBytes = 256 * 1024 * 1024

	// DefaultResponseTimeout bounds the wait between response bytes. A model
	// generating slowly is the ordinary case; an upstream that has stopped
	// speaking without closing is not, and without this the proxy would hold
	// the harness open for as long as the upstream stayed silent.
	DefaultResponseTimeout = 10 * time.Minute

	// DefaultConnectTimeout bounds reaching the upstream at all.
	DefaultConnectTimeout = 10 * time.Second
)

// DefaultPaths are the endpoints a model client calls. The set is closed:
// a request cannot choose a path any more than it can choose a host, because
// the whole security claim of this process is that it forwards model calls to
// one place and nothing else.
var DefaultPaths = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/responses",
	"/v1/messages",
}

// ContextHeaderPrefix marks the correlation headers a harness sends to tie a
// model call to the turn that made it. They are stripped before forwarding:
// they are this system's, and an upstream has no use for them.
const ContextHeaderPrefix = "x-machinist-telemetry-"

// Config is a proxy fixed at startup.
type Config struct {
	// Listen is the loopback address to serve on. A zero port asks the
	// operating system for an ephemeral one, which is the normal way to run
	// this: the harness is told the address after it is chosen.
	Listen string

	// Upstream is the single origin every request is forwarded to.
	Upstream string

	// Model and EndpointID name what is being measured. They are recorded on
	// every event, because a timing without the identity of what produced it
	// is a number nobody can act on.
	Model      string
	EndpointID string

	// Paths is the closed set of request paths forwarded. Empty means
	// DefaultPaths.
	Paths []string

	ConnectTimeout  time.Duration
	ResponseTimeout time.Duration
}

// Settings is a validated configuration. The unexported fields are the reason
// this type exists: a Config is what an operator wrote and a Settings is what
// was checked, and nothing downstream has to wonder which it holds.
type Settings struct {
	listen          string
	upstream        *url.URL
	model           string
	endpointID      string
	paths           map[string]struct{}
	connectTimeout  time.Duration
	responseTimeout time.Duration
}

func (s Settings) Listen() string                 { return s.listen }
func (s Settings) Upstream() *url.URL             { return s.upstream }
func (s Settings) Model() string                  { return s.model }
func (s Settings) EndpointID() string             { return s.endpointID }
func (s Settings) ConnectTimeout() time.Duration  { return s.connectTimeout }
func (s Settings) ResponseTimeout() time.Duration { return s.responseTimeout }

// Allows reports whether a request path is one this proxy forwards.
func (s Settings) Allows(path string) bool {
	_, ok := s.paths[path]
	return ok
}

// Paths returns the forwarded set, for a diagnostic that has to print it.
func (s Settings) Paths() []string {
	paths := make([]string, 0, len(s.paths))
	for path := range s.paths {
		paths = append(paths, path)
	}
	return paths
}

// Validate checks a configuration and returns what was checked.
//
// Every refusal here is a refusal to start. A proxy that started with an
// upstream it could not verify, or on an address it did not intend, would be
// discovered by an operator reading a dashboard rather than by the process
// that could still have declined.
func Validate(config Config) (Settings, error) {
	listen, err := loopbackListen(config.Listen)
	if err != nil {
		return Settings{}, err
	}
	upstream, err := upstreamOrigin(config.Upstream)
	if err != nil {
		return Settings{}, err
	}
	if err := identifier(config.Model, "model"); err != nil {
		return Settings{}, err
	}
	if err := identifier(config.EndpointID, "endpoint id"); err != nil {
		return Settings{}, err
	}

	names := config.Paths
	if len(names) == 0 {
		names = DefaultPaths
	}
	paths := make(map[string]struct{}, len(names))
	for _, path := range names {
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") ||
			strings.ContainsAny(path, "?#") || strings.ContainsFunc(path, control) {
			return Settings{}, fmt.Errorf("proxy path %q is not a path", path)
		}
		paths[path] = struct{}{}
	}

	settings := Settings{
		listen: listen, upstream: upstream,
		model: config.Model, endpointID: config.EndpointID, paths: paths,
		connectTimeout:  or(config.ConnectTimeout, DefaultConnectTimeout),
		responseTimeout: or(config.ResponseTimeout, DefaultResponseTimeout),
	}
	return settings, nil
}

func or(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// loopbackListen refuses any address that is not loopback.
//
// This process forwards a harness's credentials to a model endpoint. Bound to
// a routable interface it would be an open relay for whatever key the harness
// holds, reachable by anything that can address this machine. Making that a
// configuration mistake rather than an impossibility is not a boundary.
func loopbackListen(address string) (string, error) {
	if address == "" {
		return "127.0.0.1:0", nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("proxy listen address must be host:port")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", errors.New("proxy listen address has no usable port")
	}
	// The name is resolved rather than compared. "localhost" is loopback on
	// every machine this will run on, but it is a name, and a name is resolved
	// by something outside this process.
	if host == "localhost" {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return "", errors.New("the model proxy listens only on loopback")
	}
	return net.JoinHostPort(host, port), nil
}

// upstreamOrigin refuses anything that is not a bare origin.
//
// A path, query or fragment here would be silently combined with the request's
// own path, and the result is a request going somewhere neither the operator
// nor the harness named. Credentials are refused because a URL is copied,
// logged and pasted, and the model client already has the header for them.
func upstreamOrigin(value string) (*url.URL, error) {
	if value == "" {
		return nil, errors.New("the model proxy needs an upstream")
	}
	if len(value) > 2048 || strings.ContainsFunc(value, control) {
		return nil, errors.New("upstream URL is unsafe")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.New("upstream URL is not a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("upstream URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("upstream URL must name a host")
	}
	if parsed.User != nil {
		return nil, errors.New("upstream URL cannot carry credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("upstream URL cannot carry a query or a fragment")
	}
	// A base path is kept, because an upstream really can live under one —
	// /api/anthropic is a real deployment. It is normalised so that joining it
	// with a request path cannot produce a doubled or missing separator.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.Contains(parsed.Path, "..") {
		return nil, errors.New("upstream URL path cannot traverse")
	}
	return parsed, nil
}

func identifier(value, label string) error {
	if value == "" {
		return fmt.Errorf("the model proxy needs a %s", label)
	}
	if len(value) > 128 || strings.ContainsFunc(value, control) {
		return fmt.Errorf("%s is not a safe identifier", label)
	}
	return nil
}

func control(r rune) bool { return r < 0x20 || r == 0x7f }
