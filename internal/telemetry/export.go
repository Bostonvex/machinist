package telemetry

import (
	"context"
	"fmt"
)

// maximumExportRows is the most turns one export may carry.
//
// The window a caller may ask for reaches six months, and six months of a busy
// fleet is more rows than any spreadsheet opens. An export past this is refused
// rather than truncated: a file that quietly stops at a round number is a wrong
// answer that looks like a complete one, and whoever reads it later has no way
// to tell. Narrowing the window is the caller's to do, because only the caller
// knows which part of it they wanted.
const maximumExportRows = 200_000

// CountTurns reports how many turns a filter selects.
//
// It exists so an export can be refused before a single row is written. Once a
// body has started there is no status code left to change, so the only way to
// answer "too much" honestly is to ask first.
func (s *Store) CountTurns(ctx context.Context, filter Filter) (int, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return 0, err
	}
	where, values := filter.turnConditions("t").where()
	var count int
	query := `SELECT count(*) FROM turns t JOIN agents a ON a.id = t.agent_id` + where
	if err := s.read.QueryRowContext(ctx, query, values...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count turns: %w", err)
	}
	return count, nil
}

// ExportTurns passes every turn a filter selects to yield, newest first.
//
// One statement, streamed. Paging the export with limit and offset would read
// the same rows the turns route does, but between two pages a turn that started
// after the export began sorts ahead of everything already sent — and every
// later page repeats a row. A single query has no gap for that to happen in.
//
// The order is the one the turns route uses, so a spreadsheet and the dashboard
// above it list the same turns in the same sequence.
func (s *Store) ExportTurns(ctx context.Context, filter Filter, yield func(TurnRow) error) error {
	filter, err := filter.Normalized()
	if err != nil {
		return err
	}
	where, values := filter.turnConditions("t").where()
	values = append(values, maximumExportRows)
	rows, err := s.read.QueryContext(ctx, `
		SELECT `+turnColumns+`
		FROM turns t JOIN agents a ON a.id = t.agent_id`+where+`
		ORDER BY t.started_at DESC, t.id LIMIT ?`, values...)
	if err != nil {
		return fmt.Errorf("export turns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		turn, err := scanTurn(rows.Scan)
		if err != nil {
			return fmt.Errorf("read turn: %w", err)
		}
		if err := yield(turn); err != nil {
			return err
		}
	}
	return rows.Err()
}
