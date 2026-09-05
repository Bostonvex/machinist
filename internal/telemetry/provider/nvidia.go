package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The query is fixed rather than configurable. An operator who could choose the
// fields could choose ones this parser does not expect, and the parser's
// refusal to accept a row of a different width is only meaningful because the
// width is known here.
const (
	nvidiaQuery  = "index,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw"
	nvidiaFormat = "csv,noheader,nounits"
)

// remoteHostPattern bounds an SSH destination. It admits an optional user and a
// hostname, and nothing that could become another argument or an option.
var remoteHostPattern = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// nvidiaMetrics are the columns after the index, in order. The parser pairs
// them positionally, so this list and nvidiaQuery must describe the same query.
var nvidiaMetrics = []struct{ name, unit string }{
	{"utilization_percent", "percent"},
	{"memory_used_mib", "MiB"},
	{"memory_total_mib", "MiB"},
	{"temperature_celsius", "celsius"},
	{"power_watts", "watts"},
}

// Nvidia reads GPU telemetry from nvidia-smi, locally or over SSH.
type Nvidia struct {
	name    string
	nodeID  string
	argv    []string
	timeout time.Duration
	run     Runner
}

// NewNvidia builds a provider for the GPUs of one node.
//
// remoteHost is empty for this machine, or an SSH destination. The remote form
// sets BatchMode and StrictHostKeyChecking: a poller that runs unattended every
// few seconds must never sit at a passphrase prompt, and must never be the
// thing that accepts an unknown host key on an operator's behalf.
func NewNvidia(nodeID, remoteHost string, timeout time.Duration, run Runner) (*Nvidia, error) {
	if !identifierPattern.MatchString(nodeID) {
		return nil, errors.New("node_id is not a safe identifier")
	}
	if timeout <= 0 {
		timeout = defaultCommandRun
	}
	if run == nil {
		run = RunBounded
	}

	provider := &Nvidia{name: "nvidia-smi", nodeID: nodeID, timeout: timeout, run: run}
	if remoteHost == "" {
		binary, err := absoluteExecutable("nvidia-smi")
		if err != nil {
			return nil, err
		}
		provider.argv = []string{binary, "--query-gpu=" + nvidiaQuery, "--format=" + nvidiaFormat}
		return provider, nil
	}

	if !remoteHostPattern.MatchString(remoteHost) {
		return nil, errors.New("remote NVIDIA host is not a safe SSH destination")
	}
	binary, err := absoluteExecutable("ssh")
	if err != nil {
		return nil, err
	}
	provider.name = "nvidia-smi-remote"
	seconds := int(min(max(timeout.Seconds(), 1), 30))
	provider.argv = []string{binary,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		// "--" ends option parsing, so a destination that begins with a dash is
		// a bad destination rather than an option to ssh.
		"--", remoteHost,
		"nvidia-smi", "--query-gpu=" + nvidiaQuery, "--format=" + nvidiaFormat}
	return provider, nil
}

func (n *Nvidia) Name() string { return n.name }

func (n *Nvidia) Poll(ctx context.Context) ([]Sample, error) {
	body, err := n.run(ctx, n.argv, n.timeout, 256*1024)
	if err != nil {
		return nil, err
	}
	return parseNvidiaCSV(string(body), n.nodeID)
}

func parseNvidiaCSV(text, nodeID string) ([]Sample, error) {
	var samples []Sample
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ",")
		// The row width is the check that the output is the query that was
		// asked for. A different width means a different nvidia-smi, a
		// different query, or something else entirely on stdout — and pairing
		// columns positionally against any of those would file readings under
		// the wrong metric names.
		if len(fields) != len(nvidiaMetrics)+1 {
			return nil, fmt.Errorf("%w: output does not match the fixed query", ErrCommand)
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil || index < 0 || index > 1024 {
			return nil, fmt.Errorf("%w: output does not match the fixed query", ErrCommand)
		}
		for position, metric := range nvidiaMetrics {
			value, reported, err := nvidiaNumber(fields[position+1])
			if err != nil {
				return nil, err
			}
			// A field a GPU does not support reads "[N/A]". That is the card
			// saying it cannot measure this, which is different from measuring
			// zero, so no sample is emitted rather than a false reading.
			if !reported {
				continue
			}
			samples = append(samples, Sample{
				Scope:      ScopeHardware,
				MetricName: fmt.Sprintf("gpu.%d.%s", index, metric.name),
				Value:      value,
				Unit:       metric.unit,
				ProviderID: "nvidia-smi",
				NodeID:     nodeID,
			})
		}
	}
	return samples, nil
}

var unsupportedReadings = map[string]bool{
	"n/a": true, "[n/a]": true, "na": true, "[na]": true,
	"not supported": true, "[not supported]": true,
	"unknown error": true, "[unknown error]": true,
}

func nvidiaNumber(field string) (float64, bool, error) {
	trimmed := strings.TrimSpace(field)
	if unsupportedReadings[strings.ToLower(trimmed)] {
		return 0, false, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: output does not match the fixed query", ErrCommand)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maximumValue {
		return 0, false, fmt.Errorf("%w: reading is outside the safe range", ErrCommand)
	}
	return value, true, nil
}

// absoluteExecutable resolves a name on PATH to a full path.
//
// The resolution happens once, when the provider is built, rather than on every
// poll: a relative name resolved repeatedly could pick up a different binary
// halfway through the collector's life, and an operator would have no way to
// tell which one produced a reading.
func absoluteExecutable(name string) (string, error) {
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	resolved, err := exec.LookPath(name)
	if err != nil || !strings.HasPrefix(resolved, "/") {
		return "", fmt.Errorf("%s was not found on PATH", name)
	}
	return resolved, nil
}
