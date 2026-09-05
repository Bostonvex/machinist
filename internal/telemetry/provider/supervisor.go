package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/telemetry"
)

// maximumSamplesPerPoll bounds one provider's return. A provider that suddenly
// reports thousands of samples has been pointed at something other than what it
// was written for, and accepting the flood would fill the database with it.
const maximumSamplesPerPoll = 128

// Provider is one source of infrastructure samples.
type Provider interface {
	Name() string
	Poll(ctx context.Context) ([]Sample, error)
}

// Status is what a provider has done so far, for an operator asking why a chart
// is empty. A provider that never worked and one that stopped working look
// nothing alike here, which is the difference between a missing binary and an
// endpoint that went down.
type Status struct {
	State    string `json:"state"`
	Samples  int    `json:"samples"`
	Failures int    `json:"failures"`
	Message  string `json:"message,omitempty"`
}

// Supervisor polls each provider on its own schedule and keeps failures local
// to the provider that had them.
//
// One provider failing must not stop the others. A machine typically runs the
// GPU reader and the inference-server reader together, and they fail for
// entirely unrelated reasons — a driver upgrade, a restarted server. Losing
// both because one broke is how a monitoring system stops monitoring at exactly
// the moment something is wrong.
type Supervisor struct {
	providers  []Provider
	emit       func(context.Context, []telemetry.Event)
	interval   time.Duration
	instanceID string
	version    string
	logger     *log.Logger
	now        func() time.Time
	newID      func() string
	started    time.Time

	mutex  sync.Mutex
	status map[string]*Status
}

// NewSupervisor builds a supervisor. An interval outside one second to one hour
// is refused: below it the poller costs more than what it measures, and above
// it the samples are too sparse to say anything about a turn.
func NewSupervisor(providers []Provider, emit func(context.Context, []telemetry.Event), interval time.Duration, version string, logger *log.Logger) (*Supervisor, error) {
	if emit == nil {
		return nil, errors.New("a supervisor needs somewhere to send samples")
	}
	if interval < time.Second || interval > time.Hour {
		return nil, errors.New("provider interval must be between 1 second and 1 hour")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if version == "" {
		version = "0.0.0"
	}
	supervisor := &Supervisor{
		providers: providers, emit: emit, interval: interval, version: version,
		logger: logger, now: time.Now, newID: newUUID,
		instanceID: newUUID(), status: map[string]*Status{},
	}
	for _, provider := range providers {
		if _, taken := supervisor.status[provider.Name()]; taken {
			// Two providers under one name would share a status row, and an
			// operator reading it could not tell which of them was failing.
			return nil, fmt.Errorf("two providers are both named %q", provider.Name())
		}
		supervisor.status[provider.Name()] = &Status{State: "pending"}
	}
	return supervisor, nil
}

// Run polls until the context is cancelled. Each provider runs in its own
// goroutine so a slow one delays only itself.
func (s *Supervisor) Run(ctx context.Context) {
	s.started = s.now()
	var group sync.WaitGroup
	for _, provider := range s.providers {
		group.Add(1)
		go func() {
			defer group.Done()
			s.loop(ctx, provider)
		}()
	}
	group.Wait()
}

// Diagnostics reports each provider's state.
func (s *Supervisor) Diagnostics() map[string]Status {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	snapshot := make(map[string]Status, len(s.status))
	for name, status := range s.status {
		snapshot[name] = *status
	}
	return snapshot
}

func (s *Supervisor) loop(ctx context.Context, provider Provider) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		started := s.now()
		s.pollOnce(ctx, provider)
		// The interval is measured from the start of the poll, so a provider
		// that takes four seconds on a ten-second interval is still polled
		// every ten seconds rather than every fourteen. A series whose spacing
		// depends on how slow the last read was cannot be rated.
		wait := s.interval - s.now().Sub(started)
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		timer.Reset(wait)
	}
}

func (s *Supervisor) pollOnce(ctx context.Context, provider Provider) {
	name := provider.Name()
	samples, err := provider.Poll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.degraded(name, err)
		return
	}
	if len(samples) > maximumSamplesPerPoll {
		s.degraded(name, fmt.Errorf("returned %d samples, more than the %d allowed", len(samples), maximumSamplesPerPoll))
		return
	}

	observedAt := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	offset := float64(s.now().Sub(s.started).Milliseconds())
	events := make([]telemetry.Event, 0, len(samples))
	for _, sample := range samples {
		event, err := sample.Event(s.instanceID, s.version, s.newID(), observedAt, offset)
		if err != nil {
			// One unusable sample does not discard the poll. A GPU reporting a
			// nonsense temperature should not also cost the utilisation reading
			// taken from the same line.
			s.logger.Printf("telemetry: provider %s produced an unusable sample: %v", name, err)
			continue
		}
		events = append(events, event)
	}
	if len(events) > 0 {
		s.emit(ctx, events)
	}
	s.record(name, "ok", len(events), false, "")
}

func (s *Supervisor) degraded(name string, err error) {
	// Logged once per failed poll and recorded as a count. The message is the
	// error's, which every provider here writes without the command, the URL,
	// or the output it came from.
	s.logger.Printf("telemetry: optional provider %s failed; collection continues: %v", name, err)
	s.record(name, "degraded", 0, true, err.Error())
}

func (s *Supervisor) record(name, state string, samples int, failed bool, message string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	status, known := s.status[name]
	if !known {
		return
	}
	status.State = state
	status.Samples += samples
	status.Message = message
	if failed {
		status.Failures++
	}
}

func newUUID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// A collector that cannot generate an identifier would emit events that
		// collide, and colliding ids are silently deduplicated by the store.
		panic("telemetry: no randomness available for event identifiers")
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
