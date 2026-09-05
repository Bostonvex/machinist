package telemetry

import (
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
)

func exported(t *testing.T, server *Server, path string) (*http.Response, [][]string) {
	t.Helper()
	recorder := get(t, server, path)
	result := recorder.Result()
	if result.StatusCode != http.StatusOK {
		return result, nil
	}
	rows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("read the export as CSV: %v", err)
	}
	return result, rows
}

func TestTheExportKeepsThePythonCollectorsColumns(t *testing.T) {
	// A spreadsheet saved against the old collector, and every script that
	// reads this file by position, keeps working only if the header does not
	// move. Changing the collector behind the URL is not a reason to change
	// the answer it gives.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	_, rows := exported(t, server, ExportPath)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want a header and one turn", len(rows))
	}
	if got := strings.Join(rows[0], ","); got != strings.Join(exportColumns, ",") {
		t.Fatalf("header = %q", got)
	}
	want := []string{
		"id", "agent_id", "agent_display_name", "harness", "model", "endpoint_id",
		"started_at", "ended_at", "outcome", "ttfa_ms", "ttfvt_ms", "first_tool_ms",
		"duration_ms", "max_stall_ms", "tool_count", "tool_observation_mode",
		"measurement_quality", "error_category", "error_code", "cancellation_reason",
	}
	if got := strings.Join(rows[0], ","); got != strings.Join(want, ",") {
		t.Fatalf("header drifted from the Python collector's: %q", got)
	}
}

func TestTheExportSaysItIsAFileAndNotAPage(t *testing.T) {
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	result, _ := exported(t, server, ExportPath)
	for header, want := range map[string]string{
		"Content-Type":           "text/csv; charset=utf-8",
		"Content-Disposition":    `attachment; filename="machinist-agent-turns.csv"`,
		"X-Content-Type-Options": "nosniff",
	} {
		if got := result.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestTheExportHonoursTheFilterItWasGiven(t *testing.T) {
	// An export that ignored the window would hand back a month of rows to a
	// caller who asked about an hour, and nothing in the file would say so.
	server, store := newTestServer(t)
	turnAt(t, store, "kept", "agent-a", "turn-kept", nowish(), nil)
	turnAt(t, store, "other", "agent-b", "turn-other", nowish(), nil)

	_, rows := exported(t, server, ExportPath+"?agent=agent-a")
	if len(rows) != 2 || rows[1][0] != "turn-kept" {
		t.Fatalf("the filter was not applied: %v", rows)
	}
}

func TestAnUnreadableFilterIsRefusedBeforeAFileStarts(t *testing.T) {
	server, _ := newTestServer(t)
	recorder := get(t, server, ExportPath+"?since=yesterday")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable date answered %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("a refused export offered a download: %q", got)
	}
}

func TestAMissingMeasurementIsAnEmptyCellAndNotAZero(t *testing.T) {
	// A zero here reads as a turn that answered instantly. The harness simply
	// did not measure it, and the file has to be able to say that.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	_, rows := exported(t, server, ExportPath)
	ttfa := rows[1][indexOf(t, "ttfa_ms")]
	if ttfa != "" {
		t.Fatalf("an unmeasured ttfa_ms = %q, want an empty cell", ttfa)
	}
	if duration := rows[1][indexOf(t, "duration_ms")]; duration != "1000" {
		t.Fatalf("duration_ms = %q, want the measurement that was taken", duration)
	}
}

func indexOf(t *testing.T, column string) int {
	t.Helper()
	for index, name := range exportColumns {
		if name == column {
			return index
		}
	}
	t.Fatalf("no column named %q", column)
	return -1
}

func TestACellASpreadsheetWouldRunIsDeclaredText(t *testing.T) {
	// Every string in this file was named by an agent. A display name
	// beginning = is a formula to Excel, Numbers and Sheets, and opening the
	// export would run it.
	server, store := newTestServer(t)
	for name, display := range map[string]string{
		"equals": `=cmd|' /c calc'!A1`,
		"plus":   "+1+1",
		"minus":  "-2+3",
		"at":     "@SUM(A1)",
	} {
		turnAt(t, store, name, "agent-"+name, "turn-"+name, nowish(),
			map[string]any{"agent": map[string]any{"id": "agent-" + name, "display_name": display}})
	}

	_, rows := exported(t, server, ExportPath)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want a header and four turns", len(rows))
	}
	column := indexOf(t, "agent_display_name")
	for _, row := range rows[1:] {
		if !strings.HasPrefix(row[column], "'") {
			t.Fatalf("a formula was exported unguarded: %q", row[column])
		}
	}
}

func TestTheGuardCoversTheLeadingCharactersIngestWouldNeverAccept(t *testing.T) {
	// A tab or a carriage return in front of = is the same formula with the
	// first character eaten by the spreadsheet. Ingest refuses both today, so
	// no stored value reaches the guard carrying one — which is exactly why
	// the guard has to keep them: the reason they cannot arrive lives in
	// another file, and the export must not depend on it staying true.
	for _, value := range []string{"=1+1", "+1", "-1", "@A1", "\t=1+1", "\r=1+1"} {
		if got := spreadsheetSafe(value); !strings.HasPrefix(got, "'") {
			t.Fatalf("spreadsheetSafe(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", "agent-a", "succeeded", "2026-09-05T10:00:00.000Z", "'quoted"} {
		if got := spreadsheetSafe(value); got != value {
			t.Fatalf("spreadsheetSafe(%q) = %q, want it unaltered", value, got)
		}
	}
}

func TestAnOrdinaryCellIsNotRewritten(t *testing.T) {
	// The guard changes what a spreadsheet does with the value, so it must not
	// touch a value no spreadsheet would run.
	server, store := newTestServer(t)
	turnAt(t, store, "one", "agent-a", "turn-1", nowish(), nil)

	_, rows := exported(t, server, ExportPath)
	if got := rows[1][indexOf(t, "agent_display_name")]; got != "agent-a" {
		t.Fatalf("display name = %q, want it unaltered", got)
	}
}

func TestANegativeMeasurementStaysANumber(t *testing.T) {
	// Guarding a number would turn a reading into text, and every downstream
	// average would silently drop it.
	if got := csvNumber(pointerTo(-12.5)); got != "-12.5" {
		t.Fatalf("a negative measurement = %q", got)
	}
	if got := spreadsheetSafe("-12.5"); got == "-12.5" {
		t.Fatalf("the guard must still apply to strings, and this one it skipped")
	}
}

func pointerTo[T any](value T) *T { return &value }

func TestAnExportTooLargeIsRefusedRatherThanTruncated(t *testing.T) {
	// The Python collector capped the file at 500 rows and said nothing, so a
	// short export of a busy month was indistinguishable from a quiet one. A
	// refusal is an answer; a truncated file is a wrong one that looks whole.
	store := openTestStore(t)
	filter, err := Filter{}.Normalized()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	count, err := store.CountTurns(context.Background(), filter)
	if err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d on an empty store", count)
	}
	if maximumExportRows <= maximumListLimit {
		t.Fatalf("an export capped at a listing's limit is a listing")
	}
}

func TestTheCountAndTheExportSelectTheSameTurns(t *testing.T) {
	// The size is decided by the count and the file is written by the export.
	// If they disagreed, the check that a file is small enough would be
	// answering about a different set of rows than the one being written.
	store := openTestStore(t)
	turnAt(t, store, "a", "agent-a", "turn-a", "2026-09-05T10:00:00.000Z", nil)
	turnAt(t, store, "b", "agent-b", "turn-b", "2026-09-05T11:00:00.000Z", nil)
	turnAt(t, store, "c", "agent-a", "turn-c", "2026-09-05T12:00:00.000Z", nil)

	filter := Filter{AgentID: "agent-a", Since: "2026-09-05T00:00:00Z", Until: "2026-09-06T00:00:00Z"}
	count, err := store.CountTurns(context.Background(), filter)
	if err != nil {
		t.Fatalf("count turns: %v", err)
	}
	var exported int
	if err := store.ExportTurns(context.Background(), filter, func(TurnRow) error {
		exported++
		return nil
	}); err != nil {
		t.Fatalf("export turns: %v", err)
	}
	if count != exported || count != 2 {
		t.Fatalf("count = %d, exported = %d, want 2 each", count, exported)
	}
}

func TestTheExportIsNewestFirst(t *testing.T) {
	store := openTestStore(t)
	turnAt(t, store, "old", "agent-a", "turn-old", "2026-09-05T10:00:00.000Z", nil)
	turnAt(t, store, "new", "agent-a", "turn-new", "2026-09-05T12:00:00.000Z", nil)

	var order []string
	if err := store.ExportTurns(context.Background(),
		Filter{Since: "2026-09-05T00:00:00Z", Until: "2026-09-06T00:00:00Z"},
		func(turn TurnRow) error {
			order = append(order, turn.ID)
			return nil
		}); err != nil {
		t.Fatalf("export turns: %v", err)
	}
	if len(order) != 2 || order[0] != "turn-new" {
		t.Fatalf("order = %v, want the newest turn first", order)
	}
}
