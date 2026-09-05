package telemetry

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Bounds on what one query may ask for.
const (
	maximumQueryOffset = 100_000
	// maximumQueryRange caps the window a single request may cover. Past six
	// months the query stops being a dashboard and becomes an export, and a
	// caller that asked for everything by leaving the window off would hold a
	// read connection open behind every other reader.
	maximumQueryRange = 180 * 24 * time.Hour
	// defaultQueryRange is the window a request that names none gets. It is a
	// window rather than everything, so an omitted parameter costs a month of
	// rows and not the whole database.
	defaultQueryRange = 30 * 24 * time.Hour
	// derivedWindowStep quantises a window boundary this collector supplied
	// rather than the caller.
	//
	// Without it a dashboard polling with no dates asks a slightly different
	// question every time — the window's edge moves by however long the last
	// request took — so no two polls can share an answer and two tabs never
	// agree on what they are looking at. A boundary the caller named is used as
	// given; only the one invented here is rounded.
	derivedWindowStep = 5 * time.Second
)

// queryFilters reads a filter from a URL query.
//
// The parameter names are the ones the Python collector used, so a bookmarked
// dashboard URL still selects what it selected. The storage column names are
// not exposed: agent, not agent_id.
func queryFilters(query url.Values, now time.Time) (Filter, error) {
	filter := Filter{
		AgentID:    query.Get("agent"),
		Harness:    query.Get("harness"),
		Model:      query.Get("model"),
		EndpointID: query.Get("endpoint"),
		Outcome:    query.Get("outcome"),
		Since:      query.Get("since"),
		Until:      query.Get("until"),
	}
	// A window is always set, even when the caller named neither end. Leaving
	// it open would make the cheapest request to write the most expensive one
	// to answer.
	until := now.UTC().Truncate(derivedWindowStep)
	if filter.Until != "" {
		parsed, err := parseTimestamp(filter.Until)
		if err != nil {
			return Filter{}, errors.New("invalid_until")
		}
		until = parsed.UTC()
	}
	if filter.Since == "" {
		filter.Since = until.Add(-defaultQueryRange).Format(timeLayout)
	}
	since, err := parseTimestamp(filter.Since)
	if err != nil {
		return Filter{}, errors.New("invalid_since")
	}
	span := until.Sub(since.UTC())
	if span < 0 {
		return Filter{}, errors.New("invalid_date_range")
	}
	if span > maximumQueryRange {
		return Filter{}, errors.New("date_range_too_large")
	}

	normalized, err := filter.Normalized()
	if err != nil {
		// The message names the parameter rather than repeating the value. A
		// query string is logged, and echoing it back is how a reflected value
		// reaches a page that renders errors.
		return Filter{}, fmt.Errorf("invalid_filter")
	}
	return normalized, nil
}

// queryLimit reads a row limit, clamped into what this store answers quickly.
// A value that is not a number falls back to the default rather than failing:
// a truncated URL should show a dashboard, not an error page.
func queryLimit(query url.Values, fallback int) int {
	value, err := strconv.Atoi(query.Get("limit"))
	if err != nil {
		return fallback
	}
	return bound(value, fallback, maximumListLimit)
}

func queryOffset(query url.Values) int {
	value, err := strconv.Atoi(query.Get("offset"))
	if err != nil || value < 0 {
		return 0
	}
	return min(value, maximumQueryOffset)
}
