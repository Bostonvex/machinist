package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

// Bounds on a json_command configuration document and the output it produces.
// They exist so a command that floods, or a config written wrong, costs one
// poll rather than the process or the store.
const (
	maximumConfigBytes = 64 * 1024
	maximumJSONSamples = 128
)

// jsonCommandConfigFields are the only fields a config document may carry. A
// document with any other field was written against a different schema, and a
// collector that guessed what it meant would be reading configuration it never
// asked for.
var jsonCommandConfigFields = map[string]bool{
	"schema_version":  true,
	"scope":           true,
	"provider_id":     true,
	"node_id":         true,
	"endpoint_id":     true,
	"argv":            true,
	"allowed_metrics": true,
	"timeout_seconds": true,
}

// jsonCommandSampleFields are the only fields an emitted sample may carry.
var jsonCommandSampleFields = map[string]bool{
	"metric_name":         true,
	"value":               true,
	"unit":                true,
	"measurement_quality": true,
}

// JsonCommand runs an operator-configured command and reads a JSON document of
// samples from its standard output.
//
// A command and its arguments are never assembled from a URL or a request;
// they come from a config file an operator wrote, and RunBounded runs exactly
// that argv, with no shell and no stdin.
type JsonCommand struct {
	name           string
	scope          Scope
	argv           []string
	allowedMetrics map[string]bool
	providerID     string
	nodeID         string
	endpointID     string
	timeout        time.Duration
	run            Runner
}

// NewJsonCommand builds a provider from values that were themselves read from
// configuration.
func NewJsonCommand(scope Scope, argv []string, allowedMetrics []string, providerID, nodeID, endpointID string, timeout time.Duration, run Runner) (*JsonCommand, error) {
	if scope != ScopeServer && scope != ScopeHardware {
		return nil, errors.New("json provider scope must be server or hardware")
	}
	if len(argv) == 0 || len(argv) > maximumArguments {
		return nil, errors.New("json provider argv is invalid")
	}
	if !strings.HasPrefix(argv[0], "/") {
		return nil, errors.New("json provider executable must be an absolute path")
	}
	for _, argument := range argv {
		if argument == "" || len(argument) > maximumArgument || strings.ContainsFunc(argument, func(r rune) bool { return r < 0x20 }) {
			return nil, errors.New("json provider argv is invalid")
		}
	}
	if len(allowedMetrics) == 0 || len(allowedMetrics) > maximumJSONSamples {
		return nil, errors.New("json provider requires 1 to 128 allowed metrics")
	}
	allowlist := make(map[string]bool, len(allowedMetrics))
	for _, metric := range allowedMetrics {
		if !identifierPattern.MatchString(metric) {
			return nil, fmt.Errorf("allowed metric %q is not a safe identifier", metric)
		}
		allowlist[metric] = true
	}
	if scope == ScopeHardware {
		if !identifierPattern.MatchString(providerID) {
			return nil, errors.New("json provider provider_id is not a safe identifier")
		}
		if !identifierPattern.MatchString(nodeID) {
			return nil, errors.New("json provider node_id is not a safe identifier")
		}
		if endpointID != "" {
			return nil, errors.New("hardware json providers cannot set endpoint_id")
		}
	} else {
		if !identifierPattern.MatchString(endpointID) {
			return nil, errors.New("json provider endpoint_id is not a safe identifier")
		}
		if providerID != "" || nodeID != "" {
			return nil, errors.New("server json providers cannot set hardware identity")
		}
	}
	if timeout <= 0 {
		timeout = defaultCommandRun
	}
	if timeout > maximumTimeout {
		return nil, fmt.Errorf("json provider timeout must not exceed %s", maximumTimeout)
	}
	if run == nil {
		run = RunBounded
	}
	return &JsonCommand{
		name: "json-command", scope: scope, argv: argv, allowedMetrics: allowlist,
		providerID: providerID, nodeID: nodeID, endpointID: endpointID, timeout: timeout, run: run,
	}, nil
}

// NewJsonCommandFromFile loads a provider from a bounded config file, the way
// the Python JsonCommandProvider.from_file does.
//
// The file is capped before it is read, and the argv it carries is never put
// anywhere a URL or a request could assemble it from something else. An
// unreadable or invalid document is an error, never a permissive default.
func NewJsonCommandFromFile(path string, run Runner) (*JsonCommand, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("json provider config could not be read")
	}
	if info.Size() > maximumConfigBytes {
		return nil, errors.New("json provider config exceeded the limit")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("json provider config could not be read")
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil || config == nil {
		return nil, errors.New("json provider config could not be read")
	}
	if len(config) != len(jsonCommandConfigFields) {
		return nil, errors.New("json provider config fields do not match schema version 1")
	}
	for field := range config {
		if !jsonCommandConfigFields[field] {
			return nil, errors.New("json provider config fields do not match schema version 1")
		}
	}
	schema, ok := config["schema_version"].(float64)
	if !ok || schema != 1 {
		return nil, errors.New("unsupported json provider config schema")
	}
	scopeValue, ok := config["scope"].(string)
	if !ok {
		return nil, errors.New("unsupported json provider config schema")
	}
	argv, err := jsonCommandArray(config["argv"], "argv")
	if err != nil {
		return nil, err
	}
	allowed, err := jsonCommandArray(config["allowed_metrics"], "allowed_metrics")
	if err != nil {
		return nil, err
	}
	providerID, err := jsonCommandID(config["provider_id"], "provider_id")
	if err != nil {
		return nil, err
	}
	nodeID, err := jsonCommandID(config["node_id"], "node_id")
	if err != nil {
		return nil, err
	}
	endpointID, err := jsonCommandID(config["endpoint_id"], "endpoint_id")
	if err != nil {
		return nil, err
	}
	timeout, err := jsonCommandTimeout(config["timeout_seconds"])
	if err != nil {
		return nil, err
	}
	return NewJsonCommand(
		Scope(scopeValue), argv, allowed,
		providerID, nodeID, endpointID,
		timeout,
		run,
	)
}

// jsonCommandID reads an optional identity field. A null or missing value is
// blank; any other value must be a safe identifier, because a malformed
// identifier is an error, never a permissive default.
func jsonCommandID(value any, name string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("json provider %s must be a string", name)
	}
	if !identifierPattern.MatchString(text) {
		return "", fmt.Errorf("json provider %s is not a safe identifier", name)
	}
	return text, nil
}

// jsonCommandTimeout reads the optional timeout. A missing value takes the
// default; anything else must be a number this process can act on.
//
// Returning the default for a value it could not read would be the worst of
// the three outcomes available here. An operator who wrote "30s", or 30 as a
// string, or misspelled a nested key, would get a provider that runs on a
// timeout they did not choose and never said so — and a timeout is exactly the
// setting whose being wrong shows up as an unrelated symptom much later. A
// malformed timeout is an error, never a permissive default.
func jsonCommandTimeout(value any) (time.Duration, error) {
	if value == nil {
		return 0, nil
	}
	seconds, ok := value.(float64)
	if !ok {
		return 0, errors.New("json provider timeout_seconds must be a number")
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, errors.New("json provider timeout_seconds must be a finite number")
	}
	if seconds <= 0 {
		// Zero is how the caller says "use the default", so a configured zero
		// would be indistinguishable from an unset one. An operator who wrote
		// it meant something, and this cannot tell what.
		return 0, errors.New("json provider timeout_seconds must be greater than zero")
	}
	if seconds > maximumTimeout.Seconds() {
		return 0, fmt.Errorf("json provider timeout_seconds must not exceed %s", maximumTimeout)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func jsonCommandArray(value any, field string) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("json provider %s must be an array", field)
	}
	result := make([]string, len(items))
	for i, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("json provider %s must contain only strings", field)
		}
		result[i] = text
	}
	return result, nil
}

func (j *JsonCommand) Name() string { return j.name }

func (j *JsonCommand) Poll(ctx context.Context) ([]Sample, error) {
	body, err := j.run(ctx, j.argv, j.timeout, maximumOutput)
	if err != nil {
		return nil, err
	}
	return parseJSONCommand(body, j.scope, j.allowedMetrics, j.providerID, j.nodeID, j.endpointID)
}

// parseJSONCommand reduces a JSON document to the allowlisted samples.
//
// Output that does not parse is a failed poll, not a partial one: refusing a
// malformed document is the difference between a collector that tells an
// operator its command was wrong and one that quietly drops readings.
func parseJSONCommand(body []byte, scope Scope, allowedMetrics map[string]bool, providerID, nodeID, endpointID string) ([]Sample, error) {
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil || document == nil {
		return nil, errors.New("json provider output is invalid")
	}
	if len(document) != 2 {
		return nil, errors.New("json provider output fields do not match schema")
	}
	if version, ok := document["schema_version"].(float64); !ok || version != 1 {
		return nil, errors.New("json provider output schema is invalid")
	}
	samplesList, ok := document["samples"].([]any)
	if !ok {
		return nil, errors.New("json provider output schema is invalid")
	}
	if len(samplesList) > maximumJSONSamples {
		return nil, errors.New("json provider returned too many samples")
	}

	samples := make([]Sample, 0, len(samplesList))
	for _, item := range samplesList {
		raw, ok := item.(map[string]any)
		if !ok || len(raw) != len(jsonCommandSampleFields) {
			return nil, errors.New("json provider sample fields do not match schema")
		}
		for field := range raw {
			if !jsonCommandSampleFields[field] {
				return nil, errors.New("json provider sample fields do not match schema")
			}
		}
		metricName, ok := raw["metric_name"].(string)
		if !ok || !allowedMetrics[metricName] {
			return nil, errors.New("json provider returned a metric outside the allowlist")
		}
		value, ok := raw["value"].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maximumValue {
			return nil, errors.New("json provider returned an invalid value")
		}
		unit, ok := raw["unit"].(string)
		if !ok {
			return nil, errors.New("json provider returned an invalid value")
		}
		quality, ok := raw["measurement_quality"].(string)
		if !ok {
			return nil, errors.New("json provider returned an invalid value")
		}
		// The sample carries whichever identity its scope wants, and Valid
		// refuses a sample whose identity does not fit its scope. A malformed
		// identifier from the allowlist or the document fails the whole poll.
		sample := Sample{
			Scope: scope, MetricName: metricName, Value: value, Unit: unit, Quality: quality,
			EndpointID: endpointID, ProviderID: providerID, NodeID: nodeID,
		}
		if err := sample.Valid(); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, nil
}
