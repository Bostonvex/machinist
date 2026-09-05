package telemetry

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
)

// exportColumns are the columns the Python collector wrote, in its order, so a
// saved spreadsheet or a script that reads this file by position still works
// after the collector behind the URL changed.
//
// session_id is absent for the same reason it was absent there: a session
// groups turns for a reader looking at one agent, and it is not a measurement
// of anything.
var exportColumns = []string{
	"id", "agent_id", "agent_display_name", "harness", "model", "endpoint_id",
	"started_at", "ended_at", "outcome", "ttfa_ms", "ttfvt_ms", "first_tool_ms",
	"duration_ms", "max_stall_ms", "tool_count", "tool_observation_mode",
	"measurement_quality", "error_category", "error_code", "cancellation_reason",
}

// exportTurns writes the selected turns as CSV.
//
// The size is settled before anything is written. A response that began and
// then discovered it was too large would have already sent 200, and the only
// thing left to do would be to stop mid-file — which is indistinguishable from
// a dropped connection, and from a complete export of a quiet week.
func (s *Server) exportTurns(response http.ResponseWriter, request *http.Request) {
	filter, ok := s.filterFrom(response, request)
	if !ok {
		return
	}
	count, err := s.store.CountTurns(request.Context(), filter)
	if err != nil {
		s.answer(response, "export", nil, err)
		return
	}
	if count > maximumExportRows {
		write(response, http.StatusRequestEntityTooLarge, failure{Error: "export_too_large"})
		return
	}

	response.Header().Set("Content-Type", "text/csv; charset=utf-8")
	response.Header().Set("Content-Disposition", `attachment; filename="machinist-agent-turns.csv"`)
	// A CSV is opened by whatever the browser has registered for it, and that
	// program decides what to do with the bytes. Saying it is a download and
	// nothing else removes the guessing.
	response.Header().Set("X-Content-Type-Options", "nosniff")

	writer := csv.NewWriter(response)
	if err := writer.Write(exportColumns); err != nil {
		s.logger.Printf("telemetry: export: write header: %v", err)
		return
	}
	err = s.store.ExportTurns(request.Context(), filter, func(turn TurnRow) error {
		return writer.Write(exportRow(turn))
	})
	if err != nil {
		// The header is already sent, so there is no status code left to
		// change. The truncated file is what the caller gets; the log is where
		// the reason lives.
		s.logger.Printf("telemetry: export: %v", err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		s.logger.Printf("telemetry: export: flush: %v", err)
	}
}

func exportRow(turn TurnRow) []string {
	return []string{
		spreadsheetSafe(turn.ID),
		spreadsheetSafe(turn.AgentID),
		spreadsheetSafe(turn.AgentDisplayName),
		spreadsheetSafe(csvText(turn.Harness)),
		spreadsheetSafe(csvText(turn.Model)),
		spreadsheetSafe(csvText(turn.EndpointID)),
		spreadsheetSafe(turn.StartedAt),
		spreadsheetSafe(csvText(turn.EndedAt)),
		spreadsheetSafe(csvText(turn.Outcome)),
		csvNumber(turn.TTFAMS),
		csvNumber(turn.TTFVTMS),
		csvNumber(turn.FirstToolMS),
		csvNumber(turn.DurationMS),
		csvNumber(turn.MaxStallMS),
		csvCount(turn.ToolCount),
		spreadsheetSafe(csvText(turn.ToolObservationMode)),
		spreadsheetSafe(csvText(turn.MeasurementQuality)),
		spreadsheetSafe(csvText(turn.ErrorCategory)),
		spreadsheetSafe(csvText(turn.ErrorCode)),
		spreadsheetSafe(csvText(turn.CancellationReason)),
	}
}

// spreadsheetSafe prefixes a value a spreadsheet would evaluate.
//
// Every string in this file came from an agent: a display name, a model
// identifier, an error code. A cell beginning =, +, - or @ is a formula to
// Excel, Numbers and Sheets, and one beginning with a tab or a carriage return
// is the same formula with the leading character eaten first. The prefix makes
// the cell text, which is what it always was — the value is not altered, only
// declared not to be code.
//
// This applies to strings alone. Measurements are formatted from numbers, so a
// negative one is a reading rather than something an agent chose to name
// itself.
func spreadsheetSafe(value string) string {
	if value == "" {
		return value
	}
	if strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

// A nil measurement is an empty cell, not a zero. A harness that could not
// measure a turn is the common case, and a zero there would read as a turn that
// answered instantly.
func csvNumber(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func csvCount(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func csvText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
