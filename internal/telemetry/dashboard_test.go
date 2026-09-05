package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func fetch(t *testing.T, server *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestTheDashboardIsServedFromTheBinary(t *testing.T) {
	// It is embedded, not read from a directory beside the binary: a collector
	// copied to a machine without that directory would otherwise serve nothing
	// and call it a deployment.
	server, _ := newTestServer(t)
	for path, want := range map[string]string{
		"/":           "text/html; charset=utf-8",
		"/index.html": "text/html; charset=utf-8",
		"/app.js":     "text/javascript; charset=utf-8",
		"/styles.css": "text/css; charset=utf-8",
	} {
		recorder := fetch(t, server, path, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s answered %d", path, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != want {
			t.Fatalf("%s Content-Type = %q, want %q", path, got, want)
		}
		if recorder.Body.Len() == 0 {
			t.Fatalf("%s served an empty body", path)
		}
	}
}

func TestTheDashboardRootAndIndexAreTheSamePage(t *testing.T) {
	server, _ := newTestServer(t)
	root := fetch(t, server, "/", nil).Body.String()
	index := fetch(t, server, "/index.html", nil).Body.String()
	if root != index {
		t.Fatalf("/ and /index.html served different pages")
	}
}

func TestAStalePageCannotOutliveTheCodeThatAnswersIt(t *testing.T) {
	// This is what replaces the Python collector's dashboard_version
	// handshake. The validator is the content's own hash, so an app.js that
	// changed has a different one and a browser cannot hold the old file
	// against a newer API.
	server, _ := newTestServer(t)
	first := fetch(t, server, "/app.js", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the dashboard served no validator")
	}
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want revalidation on every request", got)
	}

	again := fetch(t, server, "/app.js", map[string]string{"If-None-Match": etag})
	if again.Code != http.StatusNotModified {
		t.Fatalf("a browser holding this exact body was sent %d", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Fatalf("304 carried a body of %d bytes", again.Body.Len())
	}

	stale := fetch(t, server, "/app.js", map[string]string{"If-None-Match": `"0000"`})
	if stale.Code != http.StatusOK {
		t.Fatalf("a browser holding a different body was sent %d", stale.Code)
	}
}

func TestEachAssetHasItsOwnValidator(t *testing.T) {
	// One shared validator would let a changed app.js be served under the
	// styles it no longer matches.
	server, _ := newTestServer(t)
	seen := map[string]string{}
	for _, path := range []string{"/index.html", "/app.js", "/styles.css"} {
		etag := fetch(t, server, path, nil).Header().Get("ETag")
		if other, clash := seen[etag]; clash {
			t.Fatalf("%s and %s share the validator %s", path, other, etag)
		}
		seen[etag] = path
	}
}

func TestTheValidatorIsAcceptedInTheFormsABrowserSendsIt(t *testing.T) {
	server, _ := newTestServer(t)
	etag := fetch(t, server, "/styles.css", nil).Header().Get("ETag")
	for _, header := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		recorder := fetch(t, server, "/styles.css", map[string]string{"If-None-Match": header})
		if recorder.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match %q was answered %d", header, recorder.Code)
		}
	}
}

func TestThePageIsConfinedToWhatThisBinaryCompiledIn(t *testing.T) {
	// The dashboard renders values agents named. The policy means an injected
	// value cannot pull anything in, and has nowhere off this origin to send
	// what it can read.
	server, _ := newTestServer(t)
	recorder := fetch(t, server, "/", nil)
	policy := recorder.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'none'", "script-src 'self'", "connect-src 'self'",
		"frame-ancestors 'none'", "base-uri 'none'",
	} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("policy %q is missing %q", policy, directive)
		}
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestTheDashboardDoesNotShadowTheAPI(t *testing.T) {
	// The root pattern is /{$} rather than /, so it matches exactly one path.
	// A bare / would match everything the API does not, and an API route lost
	// to a typo would answer with the page rather than a 404.
	server, _ := newTestServer(t)
	if recorder := fetch(t, server, "/api/v1/nothing", nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("an unknown API path answered %d, want a refusal", recorder.Code)
	}
	if recorder := fetch(t, server, TurnsPath, nil); recorder.Code != http.StatusOK {
		t.Fatalf("a read route answered %d", recorder.Code)
	}
}

// apiPath finds every /api/v1/... path the dashboard fetches. The set is
// derived from the page rather than restated beside it: a list written by hand
// would keep passing after the page started calling something else, which is
// the only way this test can fail to notice the thing it exists to notice.
var apiPath = regexp.MustCompile(`/api/v1/[A-Za-z0-9._${}()/-]*`)

func TestEveryRouteTheDashboardCallsIsOneThisCollectorServes(t *testing.T) {
	// The page was written against the Python collector. A path it fetches
	// that does not exist here is a dashboard that loads and stays empty, and
	// nothing about the port would say so.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	found := map[string]bool{}
	for _, path := range apiPath.FindAllString(fetch(t, server, "/app.js", nil).Body.String(), -1) {
		// An interpolated identifier is replaced with one that exists, so the
		// request tests the route rather than the argument.
		path = strings.ReplaceAll(path, "${encodeURIComponent(agentId)}", "agent-a")
		path = strings.ReplaceAll(path, "${encodeURIComponent(turnId)}", "turn-1")
		if strings.Contains(path, "${") {
			t.Fatalf("the dashboard builds a path this test cannot resolve: %s", path)
		}
		found[path] = true
	}
	if len(found) == 0 {
		t.Fatal("no API paths were found in the page")
	}
	for _, path := range sortedPaths(found) {
		// A deadline, because one of the routes the page calls is a stream
		// that is meant never to end on its own. Excluding it by name would
		// mean the one route whose absence a reader would notice last is the
		// one this test does not check.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		cancel()
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("the dashboard calls %s and this collector does not serve it", path)
		}
	}
}

func sortedPaths(set map[string]bool) []string {
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func TestTheHandshakeTheValidatorReplacedIsGone(t *testing.T) {
	server, _ := newTestServer(t)
	if strings.Contains(fetch(t, server, "/app.js", nil).Body.String(), "dashboard_version") {
		t.Fatal("the page still sends the handshake the content validator replaced")
	}
}
