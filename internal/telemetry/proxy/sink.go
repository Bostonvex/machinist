package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// What the delivery queue is allowed to cost.
const (
	// QueueCapacity is how many events wait for delivery. Past this the oldest
	// is dropped rather than the newest: a collector that has been down for a
	// while has already lost the beginning, and what an operator looking at a
	// stalled system wants is the most recent minute rather than the first.
	QueueCapacity = 512

	// MaximumBatch is how many events one submission carries. It is under the
	// collector's own limit of 100 so that a batch is never refused for its
	// size, which is a refusal the proxy could not distinguish from any other.
	MaximumBatch = 50

	// DeliveryTimeout bounds one submission. Delivery happens on its own
	// goroutine and never blocks a model call, so this is generous: the cost
	// of a slow collector is a delayed event, and the cost of a short timeout
	// is a lost one.
	DeliveryTimeout = 5 * time.Second

	// idleDelay is how long the sender waits when the queue is empty before
	// looking again. It is short enough that an event is delivered while the
	// call it describes is still interesting, and long enough that an idle
	// proxy is not a spinning one.
	idleDelay = 100 * time.Millisecond
)

// collectorIngestPath is the collector's only ingest route. It is written here
// rather than imported so that the collector package stays free to embed this
// one; a test asserts the two agree, so the duplication cannot drift silently.
const collectorIngestPath = "/api/v1/events"

// CollectorConfig is where measurements are sent.
type CollectorConfig struct {
	// URL is the collector's ingest endpoint. It must be loopback, for the
	// same reason the proxy itself listens only on loopback: this stream is a
	// live description of what every agent on this machine is doing.
	URL string

	// Token authenticates to the collector. A collector that requires one and
	// a proxy that has none produces a silent, total loss, so this is checked
	// against a probe at startup rather than discovered from a dashboard that
	// stayed empty.
	Token string

	Timeout time.Duration
}

// Collector delivers events to the collector's ingest route.
//
// Enqueue never blocks and never fails a model call. A proxy whose telemetry
// slowed down the calls it was measuring would be changing the thing it exists
// to observe, and one that failed a call because a collector was down would
// have made observability a dependency of the work.
type Collector struct {
	endpoint string
	token    string
	client   *http.Client

	mutex   sync.Mutex
	queue   []Event
	dropped int64
	sent    int64
	failed  int64
	closed  bool
	woken   chan struct{}

	done chan struct{}
	stop context.CancelFunc
}

// Stats is what the sink can say about itself.
//
// Dropped is the number that matters: it is the only way an operator can tell
// a quiet system from a lost stream, and without it an empty dashboard has two
// explanations that look identical.
type Stats struct {
	Queued  int
	Sent    int64
	Dropped int64
	Failed  int64
}

// NewCollector validates the destination and starts the sender.
func NewCollector(config CollectorConfig) (*Collector, error) {
	endpoint, err := collectorEndpoint(config.URL)
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DeliveryTimeout
	}
	ctx, stop := context.WithCancel(context.Background())
	collector := &Collector{
		endpoint: endpoint,
		token:    config.Token,
		client:   &http.Client{Timeout: timeout},
		woken:    make(chan struct{}, 1),
		done:     make(chan struct{}),
		stop:     stop,
	}
	go collector.send(ctx)
	return collector, nil
}

// collectorEndpoint refuses anything that is not the collector's loopback
// ingest URL. The path is required to be the ingest path because a URL that is
// loopback but points somewhere else would forward this machine's telemetry to
// whatever else is listening on this machine.
func collectorEndpoint(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("the telemetry sink needs a collector URL")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("collector URL is not a URL")
	}
	if parsed.Scheme != "http" {
		return "", errors.New("collector URL must be http on loopback")
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "::1" {
		return "", errors.New("the collector is reached only on loopback")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("collector URL cannot carry credentials, a query or a fragment")
	}
	if parsed.Path != collectorIngestPath {
		return "", errors.New("collector URL must end in " + collectorIngestPath)
	}
	return parsed.String(), nil
}

// Enqueue takes an event for delivery and reports whether it was kept.
//
// A false answer means the queue was full and the oldest event was dropped to
// make room. It is returned rather than logged, because the caller is the only
// one that knows whether this loss matters.
func (c *Collector) Enqueue(event Event) bool {
	c.mutex.Lock()
	if c.closed {
		c.mutex.Unlock()
		return false
	}
	kept := true
	if len(c.queue) >= QueueCapacity {
		c.queue = c.queue[1:]
		c.dropped++
		kept = false
	}
	c.queue = append(c.queue, event)
	c.mutex.Unlock()

	select {
	case c.woken <- struct{}{}:
	default:
	}
	return kept
}

// Stats reports what has happened to the events handed over so far.
func (c *Collector) Stats() Stats {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return Stats{Queued: len(c.queue), Sent: c.sent, Dropped: c.dropped, Failed: c.failed}
}

// Close stops the sender, giving it until the context is done to deliver what
// is already queued. It is safe to call twice.
func (c *Collector) Close(ctx context.Context) error {
	c.mutex.Lock()
	if c.closed {
		c.mutex.Unlock()
		return nil
	}
	c.closed = true
	c.mutex.Unlock()

	// The sender is asked to finish rather than cancelled outright, so that a
	// shutdown does not discard the events describing what happened just
	// before it.
	c.flush(ctx)
	c.stop()
	<-c.done
	return ctx.Err()
}

// send is the delivery loop.
func (c *Collector) send(ctx context.Context) {
	defer close(c.done)
	for {
		if delivered := c.deliverOnce(ctx); delivered {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-c.woken:
		case <-time.After(idleDelay):
		}
	}
}

// flush delivers what is queued until the queue is empty or the deadline
// passes. A shutdown that dropped the last batch would lose exactly the events
// describing whatever caused the shutdown.
func (c *Collector) flush(ctx context.Context) {
	for ctx.Err() == nil {
		if !c.deliverOnce(ctx) {
			return
		}
	}
}

// deliverOnce sends one batch and reports whether there was anything to send.
func (c *Collector) deliverOnce(ctx context.Context) bool {
	batch := c.take()
	if len(batch) == 0 {
		return false
	}
	if err := c.post(ctx, batch); err != nil {
		// The batch is not requeued. A collector that refused it will refuse
		// it again, and one that was unreachable will be handed the events
		// that come after — which are the ones an operator is looking at.
		// Retrying here would trade a bounded loss for an unbounded queue.
		c.mutex.Lock()
		c.failed += int64(len(batch))
		c.mutex.Unlock()
		return true
	}
	c.mutex.Lock()
	c.sent += int64(len(batch))
	c.mutex.Unlock()
	return true
}

func (c *Collector) take() []Event {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	size := min(len(c.queue), MaximumBatch)
	if size == 0 {
		return nil
	}
	batch := make([]Event, size)
	copy(batch, c.queue[:size])
	c.queue = c.queue[size:]
	return batch
}

func (c *Collector) post(ctx context.Context, batch []Event) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// The body is drained so the connection can be reused; it is not read for
	// meaning. A refusal names a field of one event in a batch, and there is
	// nothing this process can do about it at the time it finds out.
	_, _ = response.Body.Read(make([]byte, 512))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("collector refused the batch: " + response.Status)
	}
	return nil
}
