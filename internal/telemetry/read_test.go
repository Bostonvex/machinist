package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, server *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func body(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return decoded
}

func TestAReadRouteAnswersWithoutACredential(t *testing.T) {
	// The boundary is the loopback bind. A token these routes required would be
	// a token in the page source and in the browser's history.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	for _, path := range []string{SummaryPath, AgentsPath, TurnsPath, SamplesPath, DimensionsPath} {
		if recorder := get(t, server, path); recorder.Code != http.StatusOK {
			t.Fatalf("%s answered %d", path, recorder.Code)
		}
	}
}

// nowish is a timestamp inside the default window a request with no dates gets.
func nowish() string { return time.Now().UTC().Add(-time.Minute).Format(timeLayout) }

func TestARequestWithNoWindowGetsOneRatherThanEverything(t *testing.T) {
	// An omitted parameter must cost a month of rows, not the whole database.
	server, store := newTestServer(t)
	turnAt(t, store, "ancient", "agent-a", "turn-old", "2020-01-01T00:00:00.000Z", nil)
	turnAt(t, store, "recent", "agent-a", "turn-new", nowish(), nil)

	turns := body(t, get(t, server, TurnsPath))["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want only the one inside the default window", len(turns))
	}
}

func TestAWindowLargerThanTheCapIsRefusedRatherThanAnswered(t *testing.T) {
	server, _ := newTestServer(t)
	recorder := get(t, server, TurnsPath+"?since=2000-01-01T00:00:00Z&until=2026-01-01T00:00:00Z")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("a 26-year window answered %d", recorder.Code)
	}
	if got := body(t, recorder)["error"]; got != "date_range_too_large" {
		t.Fatalf("error = %v", got)
	}
}

func TestAnInvertedWindowIsRefused(t *testing.T) {
	server, _ := newTestServer(t)
	recorder := get(t, server, TurnsPath+"?since=2026-09-05T12:00:00Z&until=2026-09-05T11:00:00Z")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an inverted window answered %d", recorder.Code)
	}
}

func TestAnUnreadableDateIsRefusedRatherThanIgnored(t *testing.T) {
	// Ignoring it would widen the window silently, and the caller would read
	// numbers for a period they did not ask about.
	server, _ := newTestServer(t)
	if recorder := get(t, server, TurnsPath+"?since=yesterday"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable date answered %d", recorder.Code)
	}
}

func TestAnOutcomeNoTurnCanHaveIsRefused(t *testing.T) {
	server, _ := newTestServer(t)
	if recorder := get(t, server, TurnsPath+"?outcome=exploded"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an impossible outcome answered %d", recorder.Code)
	}
}

func TestALimitThatIsNotANumberFallsBackRatherThanFailing(t *testing.T) {
	// A truncated URL should show a dashboard, not an error page.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)
	recorder := get(t, server, TurnsPath+"?limit=lots")
	if recorder.Code != http.StatusOK {
		t.Fatalf("a nonsense limit answered %d", recorder.Code)
	}
	if body(t, recorder)["limit"] != float64(defaultListLimit) {
		t.Fatalf("limit = %v, want the default", body(t, recorder)["limit"])
	}
}

func TestPagingOffersANextPageOnlyWhenThereMayBeOne(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)
	turnAt(t, store, "two", "agent-a", "turn-2", nowish(), nil)

	full := body(t, get(t, server, TurnsPath+"?limit=1"))
	if full["next_offset"] != float64(1) {
		t.Fatalf("next_offset = %v, want 1 after a full page", full["next_offset"])
	}
	short := body(t, get(t, server, TurnsPath+"?limit=50"))
	if short["next_offset"] != nil {
		t.Fatalf("next_offset = %v, want nothing after a short page", short["next_offset"])
	}
}

func TestSelectingAHarnessDoesNotHideTheSharedReadings(t *testing.T) {
	server, store := newTestServer(t)
	sampleAt(t, store, "gpu", nowish(), "gpu_utilization", 61, map[string]any{"unit": "percent"})

	samples := body(t, get(t, server, SamplesPath+"?harness=codex"))["samples"].([]any)
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1: shared hardware must survive an agent filter", len(samples))
	}
}

func TestATurnThatWasNeverRecordedIsNotFoundRatherThanEmpty(t *testing.T) {
	server, _ := newTestServer(t)
	recorder := get(t, server, TurnsPath+"/turn-missing")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a missing turn answered %d", recorder.Code)
	}
	if got := body(t, recorder)["error"]; got != "turn_not_found" {
		t.Fatalf("error = %v", got)
	}
}

func TestATurnDetailComesBackWithItsTimeline(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)
	recorder := get(t, server, TurnsPath+"/turn-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("turn detail answered %d: %s", recorder.Code, recorder.Body)
	}
	decoded := body(t, recorder)
	if decoded["turn"] == nil || decoded["timeline"] == nil || decoded["shared_context"] == nil {
		t.Fatalf("turn detail is missing a section: %v", decoded)
	}
}

func TestAQueryFailureDoesNotDescribeTheDatabase(t *testing.T) {
	// A storage error can name a column, a file path or a constraint. The
	// operator reading the log can use that; a caller cannot.
	server, store := newTestServer(t)
	store.Close()
	recorder := get(t, server, SummaryPath)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("a broken store answered %d", recorder.Code)
	}
	if got := body(t, recorder)["error"]; got != "query_failure" {
		t.Fatalf("error = %v, want an opaque failure", got)
	}
}

func TestASummaryIsNotRecomputedForEveryPoll(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)
	first := body(t, get(t, server, SummaryPath))

	// A turn added after the first read must not appear until the cache lapses.
	turnAt(t, store, "two", "agent-a", "turn-2", nowish(), nil)
	cached := body(t, get(t, server, SummaryPath))
	if !sameTurnCount(first, cached) {
		t.Fatal("the summary was recomputed on the very next poll")
	}

	server.now = func() time.Time { return time.Now().Add(summaryCacheFor + time.Second) }
	if sameTurnCount(first, body(t, get(t, server, SummaryPath))) {
		t.Fatal("the summary was still cached after its interval lapsed")
	}
}

func sameTurnCount(left, right map[string]any) bool {
	fleet := func(value map[string]any) any {
		return value["fleet"].(map[string]any)["turn_count"]
	}
	return fleet(left) == fleet(right)
}

func TestTheSummaryCacheCannotBeGrownFromAQueryString(t *testing.T) {
	server, _ := newTestServer(t)
	for index := 0; index < maximumCachedSummaries*2; index++ {
		get(t, server, SummaryPath+"?agent=agent-"+strings.Repeat("x", index%20)+string(rune('a'+index%26)))
	}
	if len(server.summaries) > maximumCachedSummaries {
		t.Fatalf("the cache holds %d entries, want at most %d", len(server.summaries), maximumCachedSummaries)
	}
}

func mustEncode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(encoded)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
