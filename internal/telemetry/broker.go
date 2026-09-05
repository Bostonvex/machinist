package telemetry

import "sync"

// subscriberDepth is how far behind one live reader may fall. A slow reader
// past this loses its oldest pending events rather than holding the ingest path
// while it catches up: a browser tab left open on a laptop that went to sleep
// must not be able to slow down the collector every agent on the machine is
// writing to.
const subscriberDepth = 128

// LiveEvent is the summary a live subscriber receives.
//
// It is a summary and not the event. The stream is read by a page in a browser,
// and an event's attributes carry whatever a producer put in them; forwarding
// the whole payload would put that on a channel nothing validated it for.
type LiveEvent struct {
	EventID          string  `json:"event_id"`
	EventType        string  `json:"event_type"`
	ObservedAt       string  `json:"observed_at"`
	AgentID          string  `json:"agent_id"`
	AgentDisplayName string  `json:"agent_display_name"`
	Harness          *string `json:"harness"`
	Model            *string `json:"model"`
	TurnID           *string `json:"turn_id"`
}

func liveEventFrom(event Event) LiveEvent {
	return LiveEvent{
		EventID: event.EventID, EventType: string(event.EventType),
		ObservedAt: event.ObservedAt, AgentID: event.Agent.ID,
		AgentDisplayName: event.Agent.DisplayName,
		Harness:          event.Harness, Model: event.Model, TurnID: event.TurnID,
	}
}

// broker fans stored events out to live subscribers.
type broker struct {
	mutex       sync.Mutex
	subscribers map[chan LiveEvent]struct{}
}

func newBroker() *broker {
	return &broker{subscribers: map[chan LiveEvent]struct{}{}}
}

func (b *broker) subscribe() chan LiveEvent {
	stream := make(chan LiveEvent, subscriberDepth)
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.subscribers[stream] = struct{}{}
	return stream
}

func (b *broker) unsubscribe(stream chan LiveEvent) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if _, known := b.subscribers[stream]; !known {
		return
	}
	delete(b.subscribers, stream)
	// Closed under the lock, after removal, so publish can never send on a
	// closed channel: it holds the same lock and no longer holds this one.
	close(stream)
}

// publish sends to every subscriber without blocking on any of them.
func (b *broker) publish(events []Event) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	for _, event := range events {
		live := liveEventFrom(event)
		for stream := range b.subscribers {
			select {
			case stream <- live:
			default:
				// The subscriber is full. Its oldest pending event is dropped
				// to make room, so a stalled reader loses the beginning of what
				// it missed rather than the end — the newest state is the one
				// a live view is for.
				select {
				case <-stream:
				default:
				}
				select {
				case stream <- live:
				default:
				}
			}
		}
	}
}

func (b *broker) subscriberCount() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return len(b.subscribers)
}
