package proxy

import (
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// stream feeds a body one chunk at a time, a second apart, and returns the
// inspector. The spacing is what makes "which chunk was the first token"
// answerable from the recorded time.
func stream(contentType string, chunks ...string) *inspector {
	read := newInspector(contentType)
	at := base
	for _, chunk := range chunks {
		read.feed([]byte(chunk), at)
		at = at.Add(time.Second)
	}
	read.finish(at)
	return read
}

func sse(events ...string) []string {
	chunks := make([]string, len(events))
	for index, event := range events {
		chunks[index] = "data: " + event + "\n\n"
	}
	return chunks
}

func at(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }

func requireCount(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s was not read", name)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}

func TestTimeToFirstTokenIsMeasuredFromContentAndNotFromThePreamble(t *testing.T) {
	// A Chat Completions stream opens with a chunk that announces the role and
	// carries no text. Counting it would report a time to first token that the
	// model had nothing to do with.
	read := stream("text/event-stream", sse(
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"content":""}}]}`,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`[DONE]`,
	)...)

	if !read.firstGenerated.Equal(at(2)) {
		t.Fatalf("first token at %v, want the first chunk carrying text (%v)", read.firstGenerated, at(2))
	}
}

func TestAToolCallCountsAsGeneration(t *testing.T) {
	// A call that only ever emits a tool call produced output. Requiring text
	// would leave every tool-using turn without a time to first token.
	read := stream("text/event-stream", sse(
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read"}}]}}]}`,
	)...)
	if !read.firstGenerated.Equal(at(1)) {
		t.Fatalf("first token at %v, want %v", read.firstGenerated, at(1))
	}
}

func TestChatCompletionsUsageIsReadFromTheFinalChunk(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":41,"completion_tokens":7,`+
			`"prompt_tokens_details":{"cached_tokens":32},`+
			`"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`[DONE]`,
	)...)

	requireCount(t, "input", read.usage.input, 41)
	requireCount(t, "output", read.usage.output, 7)
	requireCount(t, "cached", read.usage.cached, 32)
	requireCount(t, "reasoning", read.usage.reasoning, 3)
}

func TestAnAnthropicStreamIsReadForBothHalvesOfItsUsage(t *testing.T) {
	// Anthropic reports input tokens when the message opens and output tokens
	// when it closes. Reading only one of the two would halve every call.
	read := stream("text/event-stream", sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":100}}}`,
		`{"type":"content_block_start","content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`,
		`{"type":"message_delta","usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	)...)

	requireCount(t, "input", read.usage.input, 120)
	requireCount(t, "cached", read.usage.cached, 100)
	requireCount(t, "output", read.usage.output, 9)
	if !read.firstGenerated.Equal(at(2)) {
		t.Fatalf("first token at %v, want the first text delta (%v)", read.firstGenerated, at(2))
	}
}

func TestAnAnthropicTextBlockOpeningIsNotYetATokenButAToolBlockIs(t *testing.T) {
	text := stream("text/event-stream", sse(
		`{"type":"content_block_start","content_block":{"type":"text","text":""}}`,
	)...)
	if !text.firstGenerated.IsZero() {
		t.Fatal("an empty text block counted as generation")
	}
	tool := stream("text/event-stream", sse(
		`{"type":"content_block_start","content_block":{"type":"tool_use","name":"read"}}`,
	)...)
	if tool.firstGenerated.IsZero() {
		t.Fatal("a tool block opening did not count as generation, but the name is output")
	}
}

func TestAResponsesStreamIsRead(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":4,`+
			`"input_tokens_details":{"cached_tokens":8},"output_tokens_details":{"reasoning_tokens":2}}}}`,
	)...)

	if !read.firstGenerated.Equal(at(1)) {
		t.Fatalf("first token at %v, want %v", read.firstGenerated, at(1))
	}
	requireCount(t, "input", read.usage.input, 12)
	requireCount(t, "output", read.usage.output, 4)
	requireCount(t, "cached", read.usage.cached, 8)
	requireCount(t, "reasoning", read.usage.reasoning, 2)
}

func TestAResponsesDeltaWithNothingInItIsNotAToken(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"type":"response.output_text.delta","delta":""}`,
	)...)
	if !read.firstGenerated.IsZero() {
		t.Fatal("an empty delta counted as generation")
	}
}

func TestAnEventSplitAcrossChunksIsStillOneEvent(t *testing.T) {
	// The network decides where a chunk ends. A parser that only worked when a
	// chunk happened to end on a line boundary would report usage that came
	// and went with the packet size.
	read := newInspector("text/event-stream")
	read.feed([]byte(`data: {"usage":{"prompt_to`), base)
	read.feed([]byte("kens\":5,\"completion_tokens\":6}}\n"), at(1))
	read.feed([]byte("\n"), at(2))
	read.finish(at(3))

	requireCount(t, "input", read.usage.input, 5)
	requireCount(t, "output", read.usage.output, 6)
}

func TestAMultiLineEventIsJoinedTheWayTheSpecificationSaysToJoinIt(t *testing.T) {
	read := newInspector("text/event-stream")
	read.feed([]byte("data: {\"usage\":\ndata: {\"prompt_tokens\":3}}\n\n"), base)
	read.finish(at(1))
	requireCount(t, "input", read.usage.input, 3)
}

func TestAFinalEventWithNoTrailingBlankLineIsStillRead(t *testing.T) {
	// The usage block is often the last thing on the wire, and a provider that
	// closes the connection without a final blank line would otherwise cost
	// every one of its calls its token counts.
	read := newInspector("text/event-stream")
	read.feed([]byte(`data: {"usage":{"prompt_tokens":11}}`), base)
	read.finish(at(1))
	requireCount(t, "input", read.usage.input, 11)
}

func TestCommentsAndFramingFieldsAreIgnoredRatherThanParsed(t *testing.T) {
	read := newInspector("text/event-stream")
	read.feed([]byte(": keep-alive\n"), base)
	read.feed([]byte("event: message\nid: 7\nretry: 3000\n"), at(1))
	read.feed([]byte("data: {\"usage\":{\"prompt_tokens\":2}}\n\n"), at(2))
	read.finish(at(3))
	requireCount(t, "input", read.usage.input, 2)
}

func TestANonStreamingBodyIsReadWhenItEnds(t *testing.T) {
	read := stream("application/json",
		`{"id":"chatcmpl-1","choices":[{"message":{"content":"hi"}}],`,
		`"usage":{"prompt_tokens":8,"completion_tokens":2}}`,
	)
	requireCount(t, "input", read.usage.input, 8)
	requireCount(t, "output", read.usage.output, 2)
	if !read.firstGenerated.IsZero() {
		t.Fatal("a body delivered in one piece has no time to first token to report")
	}
}

func TestAnErrorInTheStreamIsSeenEvenThoughTheStatusSaidTwoHundred(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"choices":[{"delta":{"content":"partial"}}]}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`,
	)...)
	if read.streamError != "overloaded_error" {
		t.Fatalf("streamError = %q, want the provider's code", read.streamError)
	}
}

func TestAnErrorWithNoUsableCodeStillCountsAsAnError(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"type":"error","error":{"message":"something went wrong"}}`,
	)...)
	if read.streamError != "stream_error" {
		t.Fatalf("streamError = %q, want a generic code rather than none", read.streamError)
	}
}

func TestAProviderErrorCodeIsNotStoredIfItIsNotAnIdentifier(t *testing.T) {
	// The code becomes a grouping key. A provider that put prose or a newline
	// in it would otherwise turn one column into free text.
	read := stream("text/event-stream", sse(
		`{"type":"error","error":{"type":"rate limit exceeded: retry in 30s"}}`,
	)...)
	if read.streamError != "stream_error" {
		t.Fatalf("streamError = %q, want the unsafe value replaced", read.streamError)
	}
}

func TestACountThatIsNotACountIsDroppedRatherThanRounded(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"usage":{"prompt_tokens":-4,"completion_tokens":1.5,"input_tokens":"12"}}`,
	)...)
	if read.usage.input != nil || read.usage.output != nil {
		t.Fatalf("usage = %+v, want nothing read from values that are not counts", read.usage)
	}
}

func TestZeroIsRecordedAndAbsenceIsNot(t *testing.T) {
	// A cached-token count of zero is a fact: the cache missed. Absent means
	// the provider did not say. Collapsing the two would report a cache miss
	// for every provider that does not report caching at all.
	reported := stream("text/event-stream", sse(`{"usage":{"prompt_tokens_details":{"cached_tokens":0}}}`)...)
	requireCount(t, "cached", reported.usage.cached, 0)

	silent := stream("text/event-stream", sse(`{"usage":{"prompt_tokens":9}}`)...)
	if silent.usage.cached != nil {
		t.Fatalf("cached = %d, want nothing when the provider said nothing", *silent.usage.cached)
	}
}

func TestABodyBeyondTheBoundGivesUpTheMeasurementAndNotTheResponse(t *testing.T) {
	read := newInspector("application/json")
	read.feed([]byte(strings.Repeat("x", maximumInspectBytes+1)), base)
	read.feed([]byte(`{"usage":{"prompt_tokens":5}}`), at(1))
	read.finish(at(2))

	if read.enabled {
		t.Fatal("inspection stayed on past the retention bound")
	}
	if read.usage.input != nil {
		t.Fatal("a body past the bound produced a count anyway")
	}
}

func TestAStreamThatNeverSendsANewlineDoesNotGrowWithoutBound(t *testing.T) {
	read := newInspector("text/event-stream")
	read.feed([]byte("data: "+strings.Repeat("x", maximumInspectBytes+16)), base)
	if len(read.buffer) > maximumInspectBytes {
		t.Fatalf("buffer held %d bytes past the bound", len(read.buffer))
	}
	// And it resynchronises rather than staying broken for the rest of the call.
	read.feed([]byte("\ndata: {\"usage\":{\"prompt_tokens\":4}}\n\n"), at(1))
	read.finish(at(2))
	requireCount(t, "input", read.usage.input, 4)
}

func TestAnEventFieldPastTheBoundIsDroppedWholeRatherThanTruncated(t *testing.T) {
	// Half a JSON document is not a smaller JSON document. Keeping the prefix
	// would only produce parse failures with a buffer to show for them.
	read := newInspector("text/event-stream")
	read.feed([]byte("data: "+strings.Repeat("x", maximumInspectBytes+1)+"\n\n"), base)
	read.feed([]byte("data: {\"usage\":{\"prompt_tokens\":6}}\n\n"), at(1))
	read.finish(at(2))
	requireCount(t, "input", read.usage.input, 6)
}

func TestABodyThatIsNotTheShapeThisUnderstandsProducesNoMeasurement(t *testing.T) {
	read := stream("text/event-stream", sse(
		`{"some":"provider","we":["have","never","seen"]}`,
		`not json at all`,
	)...)
	if read.usage.input != nil || read.usage.output != nil || !read.firstGenerated.IsZero() || read.streamError != "" {
		t.Fatalf("an unrecognised shape produced a measurement: %+v", read)
	}
}

func TestTheAttributesCarryOnlyWhatWasRead(t *testing.T) {
	read := stream("text/event-stream", sse(`{"usage":{"prompt_tokens":3}}`)...)
	attributes := map[string]any{}
	read.attributes(attributes)

	if attributes["input_tokens"] != 3 {
		t.Fatalf("input_tokens = %v", attributes["input_tokens"])
	}
	for _, name := range []string{"output_tokens", "cached_tokens", "reasoning_tokens"} {
		if _, present := attributes[name]; present {
			t.Fatalf("%s was written without being read", name)
		}
	}
}
