package telemetry

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The dashboard is embedded rather than read from a directory beside the
// binary. A collector that loaded its own interface from the filesystem at
// request time would serve whatever was in that directory — including nothing,
// on a machine where the binary was copied but the directory was not, and
// including something else entirely on one where the directory is writable.
//
//go:embed dashboard/index.html dashboard/app.js dashboard/styles.css
var dashboardFiles embed.FS

// asset is one embedded file, with its type and a validator computed once.
type asset struct {
	body        []byte
	contentType string
	etag        string
}

var dashboardAssets = mustLoadDashboard()

func mustLoadDashboard() map[string]asset {
	types := map[string]string{
		"index.html": "text/html; charset=utf-8",
		"app.js":     "text/javascript; charset=utf-8",
		"styles.css": "text/css; charset=utf-8",
	}
	loaded := make(map[string]asset, len(types))
	for name, contentType := range types {
		body, err := dashboardFiles.ReadFile("dashboard/" + name)
		if err != nil {
			// The files are compiled in, so a missing one is a build that
			// cannot be correct rather than a condition to recover from.
			panic(fmt.Sprintf("telemetry: dashboard asset %s: %v", name, err))
		}
		sum := sha256.Sum256(body)
		loaded[name] = asset{
			body: body, contentType: contentType,
			etag: `"` + hex.EncodeToString(sum[:16]) + `"`,
		}
	}
	return loaded
}

// dashboardRoutes serves the interface beside the API it reads.
//
// The paths are the Python collector's, so a bookmark still opens the page.
func (s *Server) dashboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.dashboardAsset("index.html"))
	for _, name := range sortedAssetNames() {
		mux.HandleFunc("GET /"+name, s.dashboardAsset(name))
	}
}

func sortedAssetNames() []string {
	names := make([]string, 0, len(dashboardAssets))
	for name := range dashboardAssets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// dashboardAsset serves one embedded file.
//
// The validator is the content's own hash, and the cache directive is
// no-cache: revalidate every time, and send nothing when the browser already
// holds this exact body. That is what replaces the Python collector's
// dashboard_version handshake, which existed because a browser could hold a
// stale app.js against a newer API and there was no way to tell. A validator
// derived from the bytes cannot go stale — an app.js that changed has a
// different one — so the page cannot be older than the code that answers it.
func (s *Server) dashboardAsset(name string) http.HandlerFunc {
	file := dashboardAssets[name]
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("ETag", file.etag)
		response.Header().Set("Cache-Control", "no-cache")
		// The dashboard renders values agents named. Declaring the type
		// removes the browser's guess, and the policy keeps the page to the
		// scripts and styles this binary compiled in: an injected value cannot
		// pull in anything, and there is nowhere off this origin for one to
		// send what it can read.
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; "+
				"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; "+
				"frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")

		if matches(request.Header.Get("If-None-Match"), file.etag) {
			response.WriteHeader(http.StatusNotModified)
			return
		}
		response.Header().Set("Content-Type", file.contentType)
		response.Header().Set("Content-Length", strconv.Itoa(len(file.body)))
		if request.Method == http.MethodHead {
			return
		}
		if _, err := response.Write(file.body); err != nil {
			// A browser that navigated away mid-response is the ordinary case.
			// It is not a collector fault and does not belong in the log.
			return
		}
	}
}

// matches reports whether an If-None-Match header names this validator. The
// header is a list, and a browser holding several will send them all.
func matches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
