package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ErrCommand reports a command that did not produce usable output.
//
// It never carries the command's arguments or its output. A provider polls
// every few seconds, and a failing one would otherwise write whatever it read
// into the log on every cycle — output the collector already refused to trust,
// now in a file that is grepped and pasted into issues.
var ErrCommand = errors.New("provider command failed")

// Bounds on what a provider may run. They exist so that a command that hangs,
// floods, or was configured wrong costs one poll rather than the process.
const (
	maximumArguments  = 32
	maximumArgument   = 2048
	maximumOutput     = 1024 * 1024
	maximumTimeout    = 30 * time.Second
	defaultCommandRun = 3 * time.Second
)

// Runner runs a command and returns its standard output. Providers take one so
// a test can supply output without a real binary on the machine.
type Runner func(ctx context.Context, argv []string, timeout time.Duration, maximumBytes int) ([]byte, error)

// RunBounded runs argv with no shell, no stdin, and a cap on time and output.
//
// No shell, because a provider's arguments include an operator-configured host
// name and a metric query; passing those through a shell would make a quoting
// mistake in configuration into command execution. No stdin, because a command
// that decides to prompt would otherwise wait for input that never comes and
// spend the whole timeout doing it.
func RunBounded(ctx context.Context, argv []string, timeout time.Duration, maximumBytes int) ([]byte, error) {
	if len(argv) == 0 || len(argv) > maximumArguments {
		return nil, fmt.Errorf("%w: argv must hold between 1 and %d entries", ErrCommand, maximumArguments)
	}
	for _, argument := range argv {
		if argument == "" || len(argument) > maximumArgument {
			return nil, fmt.Errorf("%w: arguments must be bounded and non-empty", ErrCommand)
		}
		// A control character in an argument is either a mistake in
		// configuration or an attempt to end one argument and start another in
		// something downstream that splits on newlines.
		if strings.ContainsFunc(argument, func(r rune) bool { return r < 0x20 }) {
			return nil, fmt.Errorf("%w: arguments cannot contain control characters", ErrCommand)
		}
	}
	if timeout <= 0 {
		timeout = defaultCommandRun
	}
	if timeout > maximumTimeout {
		return nil, fmt.Errorf("%w: timeout must not exceed %s", ErrCommand, maximumTimeout)
	}
	if maximumBytes < 1 || maximumBytes > 4*1024*1024 {
		maximumBytes = maximumOutput
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = nil
	command.Stderr = nil

	pipe, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: could not read output", ErrCommand)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("%w: could not start", ErrCommand)
	}

	// One byte past the cap, so a command that produced exactly the cap and one
	// that produced more are distinguishable. Truncating silently would hand a
	// parser half a line and let it read a number that was never emitted.
	var output bytes.Buffer
	_, readErr := io.Copy(&output, io.LimitReader(pipe, int64(maximumBytes)+1))

	// Once the cap is reached nothing is reading the pipe any more, so a
	// command still writing blocks on a full one. Killing it here is what makes
	// a flood cost a poll; waiting would make it cost the whole timeout, and
	// the caller would be told the command was slow when it was loud.
	overflowed := output.Len() > maximumBytes
	if overflowed {
		cancel()
	}
	waitErr := command.Wait()

	if overflowed {
		return nil, fmt.Errorf("%w: output exceeded %d bytes", ErrCommand, maximumBytes)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: timed out", ErrCommand)
	}
	if readErr != nil {
		return nil, fmt.Errorf("%w: could not read output", ErrCommand)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("%w: exited unsuccessfully", ErrCommand)
	}
	return output.Bytes(), nil
}
