package proxy

import (
	"strings"
	"testing"
	"time"
)

func valid() Config {
	return Config{Upstream: "http://127.0.0.1:8000", Model: "ds-0731", EndpointID: "dgx-primary"}
}

func TestAProxyRefusesToListenAnywhereButLoopback(t *testing.T) {
	// This process forwards the harness's credentials to a model endpoint.
	// Bound to a routable interface it is an open relay for whatever key the
	// harness holds, reachable by anything that can address this machine.
	for _, address := range []string{
		"0.0.0.0:9000", "192.168.1.10:9000", "[::]:9000", ":9000", "example.com:9000",
	} {
		config := valid()
		config.Listen = address
		if _, err := Validate(config); err == nil {
			t.Fatalf("listening on %s was allowed", address)
		}
	}
}

func TestLoopbackIsAllowedInTheFormsAnOperatorWritesIt(t *testing.T) {
	for address, want := range map[string]string{
		"127.0.0.1:9000": "127.0.0.1:9000",
		"localhost:9000": "127.0.0.1:9000",
		"127.0.0.1:0":    "127.0.0.1:0",
		"[::1]:9000":     "[::1]:9000",
	} {
		config := valid()
		config.Listen = address
		settings, err := Validate(config)
		if err != nil {
			t.Fatalf("%s was refused: %v", address, err)
		}
		if settings.Listen() != want {
			t.Fatalf("%s became %q, want %q", address, settings.Listen(), want)
		}
	}
}

func TestAnUnsetListenAddressAsksForAnEphemeralLoopbackPort(t *testing.T) {
	// The normal way to run this. The harness is told the address after the
	// operating system chooses it, so nothing has to reserve a port in advance
	// or fail because something else took it.
	settings, err := Validate(valid())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if settings.Listen() != "127.0.0.1:0" {
		t.Fatalf("default listen = %q", settings.Listen())
	}
}

func TestAnUpstreamThatIsNotABareOriginIsRefused(t *testing.T) {
	// A query or fragment would be silently combined with the request's own
	// path, and the result is a request going somewhere neither the operator
	// nor the harness named. Credentials are refused because a URL is copied,
	// logged and pasted.
	for _, upstream := range []string{
		"", "ftp://host", "http://", "http://user:pass@host", "http://host?key=1",
		"http://host#fragment", "http://host/../etc", "not a url\n",
	} {
		config := valid()
		config.Upstream = upstream
		if _, err := Validate(config); err == nil {
			t.Fatalf("upstream %q was allowed", upstream)
		}
	}
}

func TestAnUpstreamMayLiveUnderABasePath(t *testing.T) {
	// /api/anthropic is a real deployment. The path is kept and normalised so
	// joining it with a request path cannot double or drop the separator.
	for upstream, want := range map[string]string{
		"http://127.0.0.1:8000":               "",
		"http://127.0.0.1:8000/":              "",
		"http://127.0.0.1:8000/api/anthropic": "/api/anthropic",
		"https://host/api/anthropic/":         "/api/anthropic",
	} {
		config := valid()
		config.Upstream = upstream
		settings, err := Validate(config)
		if err != nil {
			t.Fatalf("%s was refused: %v", upstream, err)
		}
		if got := settings.Upstream().Path; got != want {
			t.Fatalf("%s kept path %q, want %q", upstream, got, want)
		}
	}
}

func TestTheForwardedPathsAreAClosedSet(t *testing.T) {
	settings, err := Validate(valid())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, path := range DefaultPaths {
		if !settings.Allows(path) {
			t.Fatalf("%s is not forwarded", path)
		}
	}
	for _, path := range []string{
		"/v1/models", "/", "/v1/chat/completions/", "/admin", "/v1/files",
	} {
		if settings.Allows(path) {
			t.Fatalf("%s is forwarded and should not be", path)
		}
	}
}

func TestAConfiguredPathThatIsNotAPathIsRefused(t *testing.T) {
	for _, path := range []string{"v1/chat", "/v1/../admin", "/v1/x?y", "/v1/x#z", "/v1/\tx"} {
		config := valid()
		config.Paths = []string{path}
		if _, err := Validate(config); err == nil {
			t.Fatalf("path %q was allowed", path)
		}
	}
}

func TestAProxyWithoutAnIdentityIsRefused(t *testing.T) {
	// A timing without the identity of what produced it is a number nobody can
	// act on, so the proxy declines to produce one rather than recording it
	// under a default.
	config := valid()
	config.Model = ""
	if _, err := Validate(config); err == nil {
		t.Fatal("a proxy with no model was allowed")
	}
	config = valid()
	config.EndpointID = ""
	if _, err := Validate(config); err == nil {
		t.Fatal("a proxy with no endpoint id was allowed")
	}
	config = valid()
	config.Model = strings.Repeat("m", 200)
	if _, err := Validate(config); err == nil {
		t.Fatal("an oversized model name was allowed")
	}
}

func TestTimeoutsFallBackRatherThanBeingUnset(t *testing.T) {
	// A zero timeout on a transport means no timeout, so an unset field would
	// silently become an unbounded wait.
	settings, err := Validate(valid())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if settings.ConnectTimeout() != DefaultConnectTimeout {
		t.Fatalf("connect timeout = %v", settings.ConnectTimeout())
	}
	if settings.ResponseTimeout() != DefaultResponseTimeout {
		t.Fatalf("response timeout = %v", settings.ResponseTimeout())
	}
	config := valid()
	config.ConnectTimeout = 3 * time.Second
	settings, err = Validate(config)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if settings.ConnectTimeout() != 3*time.Second {
		t.Fatalf("a configured timeout was overridden: %v", settings.ConnectTimeout())
	}
}
