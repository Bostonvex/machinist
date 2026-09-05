package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// maximumInspectBytes bounds what the inspector will hold at once.
//
// The bound is on retention, not on the response: a body larger than this is
// still forwarded byte for byte, and only the measurement is given up. That is
// the right way round. A proxy that refused a large answer to protect its own
// metrics would have made the metrics more important than the thing they
// describe.
const maximumInspectBytes = 8 * 1024 * 1024

// counters are the token counts a provider reports about a call.
//
// Pointers, because zero and absent are different facts. A provider that
// reported no usage block has not told us the call used no tokens, and
// recording a nil as 0 would put a made-up number next to measured ones.
type counters struct {
	input, output, cached, reasoning *int
}

// inspector reads a model response for the handful of facts a measurement
// needs: when generation actually started, how many tokens the provider says
// it used, and whether the stream carried an error after the headers said 200.
//
// It reads content to find those facts and keeps none of it. What it retains is
// a bounded parse buffer, dropped as soon as an event is decoded, and never
// logged, stored or put in an event. The counts and the timings are the output;
// the tokens themselves are not.
//
// The shapes it understands are OpenAI Chat Completions, OpenAI Responses and
// Anthropic Messages, streamed or not. A shape it does not understand produces
// no measurement rather than a guessed one — an unread field is missing
// evidence, and a metric invented to fill the gap would be indistinguishable
// from a measured one once it is in the table.
type inspector struct {
	streaming bool
	enabled   bool

	buffer    []byte
	eventData [][]byte
	dataBytes int

	firstGenerated time.Time
	usage          counters
	streamError    string
}

func newInspector(contentType string) *inspector {
	return &inspector{
		streaming: strings.Contains(strings.ToLower(contentType), "text/event-stream"),
		enabled:   true,
	}
}

// feed takes the bytes as they were handed to the client, with the time they
// were handed over. The time is passed in rather than read here so that the
// moment recorded is when the client could have seen the token, not when this
// code got round to parsing it.
func (i *inspector) feed(chunk []byte, at time.Time) {
	if !i.enabled || len(chunk) == 0 {
		return
	}
	if !i.streaming {
		if len(i.buffer)+len(chunk) > maximumInspectBytes {
			// Past the bound the partial body is worthless — half a JSON
			// document parses to nothing — so it is released rather than kept.
			i.buffer = nil
			i.enabled = false
			return
		}
		i.buffer = append(i.buffer, chunk...)
		return
	}

	i.buffer = append(i.buffer, chunk...)
	if len(i.buffer) > maximumInspectBytes {
		// A stream that never sends a newline is not an event stream. Drop to
		// the last line boundary and resynchronise there, so one malformed
		// producer costs the measurement of its own call and not the process.
		if newline := bytes.LastIndexByte(i.buffer, '\n'); newline >= 0 {
			i.buffer = append(i.buffer[:0], i.buffer[newline+1:]...)
		} else {
			i.buffer = i.buffer[:0]
		}
		i.resetEvent()
	}
	for {
		newline := bytes.IndexByte(i.buffer, '\n')
		if newline < 0 {
			return
		}
		line := make([]byte, newline)
		copy(line, i.buffer[:newline])
		i.buffer = append(i.buffer[:0], i.buffer[newline+1:]...)
		i.consumeLine(line, at)
	}
}

// finish reads whatever the last chunk left behind. A provider that ends its
// last event without a trailing newline still reported a usage block, and that
// block is often the only place the token counts appear.
func (i *inspector) finish(at time.Time) {
	if i.streaming {
		if len(i.buffer) > 0 {
			i.consumeLine(i.buffer, at)
		}
		i.flushEvent(at)
		i.buffer = nil
		return
	}
	if i.enabled {
		i.inspectJSON(i.buffer, at)
	}
	i.buffer = nil
}

func (i *inspector) resetEvent() {
	i.eventData = i.eventData[:0]
	i.dataBytes = 0
}

func (i *inspector) flushEvent(at time.Time) {
	if len(i.eventData) == 0 {
		return
	}
	i.inspectJSON(bytes.Join(i.eventData, []byte("\n")), at)
	i.resetEvent()
}

// consumeLine advances the server-sent-events parse by one line.
//
// Only "data:" is read. The other SSE fields — event, id, retry — name the
// framing rather than the payload, and this parser has no business acting on a
// producer's retry policy.
func (i *inspector) consumeLine(line []byte, at time.Time) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		i.flushEvent(at)
		return
	}
	data, found := bytes.CutPrefix(line, []byte("data:"))
	if !found {
		return
	}
	data = bytes.TrimPrefix(data, []byte(" "))
	if i.dataBytes+len(data) > maximumInspectBytes {
		i.resetEvent()
		return
	}
	held := make([]byte, len(data))
	copy(held, data)
	i.eventData = append(i.eventData, held)
	i.dataBytes += len(data)
}

func (i *inspector) inspectJSON(payload []byte, at time.Time) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || len(payload) > maximumInspectBytes {
		return
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		// Not an error. A stream carries keep-alive comments, framing and
		// provider-specific events that are not JSON objects, and none of them
		// is a problem with the call being measured.
		return
	}
	i.inspectObject(value, at)
}

func (i *inspector) inspectObject(value map[string]any, at time.Time) {
	// Usage is reported at the top level by Chat Completions, under "response"
	// by the Responses API, and under "message" by Anthropic's message_start.
	// All three are read because all three are the same fact.
	i.readUsage(object(value["usage"]))
	if response := object(value["response"]); response != nil {
		i.readUsage(object(response["usage"]))
	}
	if message := object(value["message"]); message != nil {
		i.readUsage(object(message["usage"]))
	}
	if text(value["type"]) == "error" {
		// A 200 followed by an error event is a failure the status line does
		// not show. Without this the call would be recorded as a success that
		// produced nothing, which is worse than not recording it.
		if failure := object(value["error"]); failure != nil {
			i.streamError = identifierOrEmpty(text(failure["type"]))
		}
		if i.streamError == "" {
			i.streamError = "stream_error"
		}
	}
	if i.firstGenerated.IsZero() && generated(value) {
		i.firstGenerated = at
	}
}

// generated reports whether this event carried the first output of the model.
//
// It is deliberately narrow. A stream opens with role announcements, content
// block headers and ping events, none of which is a token: treating them as
// generation would make time-to-first-token a measurement of the provider's
// preamble rather than of the model.
func generated(value map[string]any) bool {
	if choices, ok := value["choices"].([]any); ok {
		for _, entry := range choices {
			choice := object(entry)
			if choice == nil {
				continue
			}
			if text(choice["text"]) != "" {
				return true
			}
			delta := object(choice["delta"])
			if delta == nil {
				continue
			}
			if text(delta["content"]) != "" {
				return true
			}
			if nonEmpty(delta["tool_calls"]) || nonEmpty(delta["function_call"]) {
				return true
			}
		}
	}

	eventType := text(value["type"])
	switch eventType {
	case "content_block_start":
		// A text block's start carries no text. A tool-use block's start is
		// the model's output, because the tool name is what it generated.
		block := object(value["content_block"])
		kind := text(block["type"])
		return kind == "tool_use" || kind == "server_tool_use"
	case "content_block_delta":
		delta := object(value["delta"])
		for _, field := range []string{"text", "partial_json", "thinking"} {
			if text(delta[field]) != "" {
				return true
			}
		}
		return false
	case "response.output_text.delta",
		"response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta",
		"response.refusal.delta":
		return nonEmpty(value["delta"])
	}
	return false
}

// readUsage takes the token counts, under either of the two names each has
// been given. The Responses API renamed them; both spellings are in the field.
func (i *inspector) readUsage(value map[string]any) {
	if value == nil {
		return
	}
	assign(&i.usage.input, value, "input_tokens", "prompt_tokens")
	assign(&i.usage.output, value, "output_tokens", "completion_tokens")

	if details := object(value["input_tokens_details"]); details != nil {
		assign(&i.usage.cached, details, "cached_tokens")
	}
	if details := object(value["prompt_tokens_details"]); details != nil {
		assign(&i.usage.cached, details, "cached_tokens")
	}
	if details := object(value["output_tokens_details"]); details != nil {
		assign(&i.usage.reasoning, details, "reasoning_tokens")
	}
	if details := object(value["completion_tokens_details"]); details != nil {
		assign(&i.usage.reasoning, details, "reasoning_tokens")
	}
	// Anthropic's name for the same thing.
	assign(&i.usage.cached, value, "cache_read_input_tokens")
}

// attributes adds what was read to an event's attributes.
//
// Absent facts are left out rather than written as zero, so a reader can tell
// "the provider did not say" from "the provider said none".
func (i *inspector) attributes(into map[string]any) {
	set := func(name string, value *int) {
		if value != nil {
			into[name] = *value
		}
	}
	set("input_tokens", i.usage.input)
	set("output_tokens", i.usage.output)
	set("cached_tokens", i.usage.cached)
	set("reasoning_tokens", i.usage.reasoning)
}

// assign takes the first of the given names that holds a usable count.
func assign(target **int, value map[string]any, names ...string) {
	for _, name := range names {
		if count, ok := count(value[name]); ok {
			*target = &count
			return
		}
	}
}

// count reads a JSON number as a token count.
//
// A count is a whole number and cannot be negative. A fractional or negative
// value is not a count that was measured oddly, it is a field that does not
// mean what this code thinks it means, and it is dropped for that reason.
func count(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number != float64(int(number)) || number > 1<<53 {
		return 0, false
	}
	return int(number), true
}

func object(value any) map[string]any {
	found, _ := value.(map[string]any)
	return found
}

func text(value any) string {
	found, _ := value.(string)
	return found
}

// nonEmpty reports whether a field carries anything at all, for the events
// whose payload shape varies by provider and whose presence is the signal.
func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case bool:
		return typed
	default:
		return true
	}
}

// identifierOrEmpty keeps a provider-supplied error name only if it is safe to
// store as one. The value is a provider's, it becomes a grouping key, and a
// grouping key with a newline in it is a broken table.
func identifierOrEmpty(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, letter := range value {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9',
			letter == '_', letter == '-', letter == '.':
		default:
			return ""
		}
	}
	return value
}
