// Package provider polls machines and turns what they report into telemetry
// events.
//
// Everything here is optional and everything here is read-only. A provider that
// cannot run — no nvidia-smi on this host, a vLLM endpoint that is down — is
// not an error condition for the collector; it is a metric the collector does
// not have. Collection continues without it, because the alternative is a
// monitoring system that stops monitoring when one of the things it monitors
// breaks.
package provider

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// identifierPattern bounds every name that reaches an event. Metric names and
// node identifiers come from command output and HTTP responses, which is to say
// from outside; they end up in a database, in JSON, and on a dashboard.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@=-]{0,255}$`)

// maximumValue is the largest reading accepted. Anything past it is a parse
// that went wrong rather than a machine that is remarkable.
const maximumValue = 1e15

// Scope says what a sample describes.
//
// The two are kept apart because they carry different identity: a server sample
// names an endpoint, a hardware sample names a node and the tool that read it.
// Mixing them would let a GPU reading be filed under an inference endpoint, and
// the resulting series would average two machines together.
type Scope string

const (
	ScopeServer   Scope = "server"
	ScopeHardware Scope = "hardware"
)

// Sample is one allowlisted shared metric.
//
// It is never attributed to an individual agent. A GPU is used by everything on
// the host at once, so charging its utilisation to whichever agent happened to
// be running would invent a per-agent number out of a shared one.
type Sample struct {
	Scope      Scope
	MetricName string
	Value      float64
	Unit       string

	// EndpointID identifies a server sample's endpoint; ProviderID and NodeID
	// identify a hardware sample's machine and the tool that read it. Each set
	// belongs to exactly one scope, and Valid refuses the other.
	EndpointID string
	ProviderID string
	NodeID     string

	// Quality defaults to exact when empty. Anything computed from other
	// readings — a mean over a histogram — says so, because a derived number
	// and a measured one should not be read the same way.
	Quality string
}

var qualities = map[string]bool{"exact": true, "derived": true, "estimated": true, "unavailable": true}

// Valid reports why a sample cannot be emitted, or nil.
func (s Sample) Valid() error {
	if !identifierPattern.MatchString(s.MetricName) {
		return errors.New("metric_name is not a safe identifier")
	}
	if !identifierPattern.MatchString(s.Unit) {
		return errors.New("unit is not a safe identifier")
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) || s.Value < 0 || s.Value > maximumValue {
		return fmt.Errorf("value %v is outside the safe range", s.Value)
	}
	if quality := s.quality(); !qualities[quality] {
		return fmt.Errorf("measurement quality %q is not one of the declared ones", quality)
	}
	switch s.Scope {
	case ScopeServer:
		if !identifierPattern.MatchString(s.EndpointID) {
			return errors.New("a server sample must name its endpoint")
		}
		// A server sample carrying hardware identity would be filed against a
		// node it never measured.
		if s.ProviderID != "" || s.NodeID != "" {
			return errors.New("a server sample cannot carry hardware identity")
		}
	case ScopeHardware:
		if !identifierPattern.MatchString(s.ProviderID) || !identifierPattern.MatchString(s.NodeID) {
			return errors.New("a hardware sample must name its provider and node")
		}
		if s.EndpointID != "" {
			return errors.New("a hardware sample cannot carry an endpoint")
		}
	default:
		return fmt.Errorf("scope %q is neither server nor hardware", s.Scope)
	}
	return nil
}

func (s Sample) quality() string {
	if s.Quality == "" {
		return "exact"
	}
	return s.Quality
}

// sharedAgent is the identity every infrastructure sample is filed under.
//
// It exists so the shape of an event does not change between the two kinds.
// Nothing reads it as an agent: the store keeps samples out of the agent tables
// entirely, precisely so a host that is warm is not reported as an agent that
// is working.
var sharedAgent = telemetry.Agent{ID: "shared-infrastructure", DisplayName: "Shared infrastructure"}

// Event turns a sample into a validated event, or refuses.
//
// It goes through the same validator a producer's submission does. A provider
// running inside the collector is not more trusted than one on the far side of
// an HTTP request — it reads output from commands and endpoints it does not
// control, which is exactly where a bad value would come from.
func (s Sample) Event(instanceID, version, eventID, observedAt string, offsetMS float64) (telemetry.Event, error) {
	if err := s.Valid(); err != nil {
		return telemetry.Event{}, err
	}
	attributes := map[string]any{
		"metric_name":         s.MetricName,
		"value":               s.Value,
		"unit":                s.Unit,
		"measurement_quality": s.quality(),
	}
	document := map[string]any{
		"schema_version":      1,
		"event_id":            eventID,
		"event_type":          string(s.Scope) + ".sample",
		"observed_at":         observedAt,
		"monotonic_offset_ms": offsetMS,
		"producer": map[string]any{
			"name": "machinist-infrastructure-provider", "version": version, "instance_id": instanceID,
		},
		"agent":          map[string]any{"id": sharedAgent.ID, "display_name": sharedAgent.DisplayName},
		"harness":        nil,
		"model":          nil,
		"endpoint_id":    nil,
		"session_id":     nil,
		"turn_id":        nil,
		"span_id":        nil,
		"parent_span_id": nil,
		"attributes":     attributes,
	}
	if s.Scope == ScopeServer {
		document["endpoint_id"] = s.EndpointID
	} else {
		attributes["provider_id"] = s.ProviderID
		attributes["node_id"] = s.NodeID
	}
	return telemetry.ValidateEvent(document)
}
