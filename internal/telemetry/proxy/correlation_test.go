package proxy

import (
	"strconv"
	"testing"
)

func itoa(value int) string { return strconv.Itoa(value) }

func endpoint() Context {
	return Context{
		AgentID: "dgx-primary", DisplayName: "ds-0731",
		Model: "ds-0731", EndpointID: "dgx-primary",
	}
}

func turn(id string) Context {
	return Context{
		AgentID: "reviewer", DisplayName: "Reviewer", Harness: "codex",
		Model: "ds-0731", EndpointID: "dgx-primary",
		SessionID: "session-1", TurnID: id,
	}
}

func TestATurnResolvesToItselfWhenItIsNamed(t *testing.T) {
	registry := NewRegistry(endpoint())
	if _, err := registry.Start("ctx-a", turn("turn-a")); err != nil {
		t.Fatalf("start a: %v", err)
	}
	if _, err := registry.Start("ctx-b", turn("turn-b")); err != nil {
		t.Fatalf("start b: %v", err)
	}

	resolved := registry.Resolve("ctx-b")
	if resolved.TurnID != "turn-b" || resolved.Correlation != CorrelationExact {
		t.Fatalf("resolved = %+v, want turn-b exactly", resolved)
	}
}

func TestTheOnlyRunningTurnIsAnExactAnswer(t *testing.T) {
	// Nothing else could have made the call, so naming it is not a guess.
	registry := NewRegistry(endpoint())
	if _, err := registry.Start("ctx-a", turn("turn-a")); err != nil {
		t.Fatalf("start: %v", err)
	}

	resolved := registry.Resolve("")
	if resolved.TurnID != "turn-a" || resolved.Correlation != CorrelationExact {
		t.Fatalf("resolved = %+v, want the sole turn exactly", resolved)
	}
}

func TestSeveralRunningTurnsRefuseToPickOne(t *testing.T) {
	registry := NewRegistry(endpoint())
	for _, id := range []string{"ctx-a", "ctx-b"} {
		if _, err := registry.Start(id, turn("turn-"+id)); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}

	resolved := registry.Resolve("")
	if resolved.Correlation != CorrelationAmbiguous {
		t.Fatalf("correlation = %q, want ambiguous", resolved.Correlation)
	}
	if resolved.TurnID != "" || resolved.SessionID != "" {
		t.Fatalf("resolved = %+v, want no turn named", resolved)
	}
	if resolved.EndpointID != "dgx-primary" {
		t.Fatal("the endpoint is still known and should not have been dropped")
	}
}

func TestAnUnknownContextIDDoesNotBecomeAGuess(t *testing.T) {
	// A caller naming a turn the proxy never saw is a caller who is out of
	// step. Silently resolving to some other running turn would attribute its
	// call to an agent that did not make it.
	registry := NewRegistry(endpoint())
	for _, id := range []string{"ctx-a", "ctx-b"} {
		if _, err := registry.Start(id, turn("turn-"+id)); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}

	if resolved := registry.Resolve("ctx-missing"); resolved.Correlation != CorrelationAmbiguous {
		t.Fatalf("resolved = %+v, want ambiguous", resolved)
	}
}

func TestNoRunningTurnFallsBackToTheEndpoint(t *testing.T) {
	registry := NewRegistry(endpoint())
	resolved := registry.Resolve("ctx-a")
	if resolved.Correlation != CorrelationUnavailable {
		t.Fatalf("correlation = %q, want unavailable", resolved.Correlation)
	}
	if resolved.AgentID != "dgx-primary" || resolved.Model != "ds-0731" {
		t.Fatalf("resolved = %+v, want the endpoint's own identity", resolved)
	}
}

func TestEndingATurnRestoresTheSoleRemainingOne(t *testing.T) {
	registry := NewRegistry(endpoint())
	for _, id := range []string{"ctx-a", "ctx-b"} {
		if _, err := registry.Start(id, turn("turn-"+id)); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	active, err := registry.End("ctx-a")
	if err != nil || active != 1 {
		t.Fatalf("end = %d, %v", active, err)
	}
	if resolved := registry.Resolve(""); resolved.TurnID != "turn-ctx-b" {
		t.Fatalf("resolved = %+v, want the remaining turn", resolved)
	}
}

func TestEndingATurnTheProxyNeverSawIsNotAnError(t *testing.T) {
	// The harness told the truth about its turn; the proxy missed the start.
	// Refusing here would make a correct caller look broken.
	registry := NewRegistry(endpoint())
	if _, err := registry.End("ctx-never-started"); err != nil {
		t.Fatalf("end: %v", err)
	}
}

func TestARestartedTurnDoesNotHoldTheRegistryOpen(t *testing.T) {
	// Restating a turn refreshes what it says, not how long it lives. If it
	// moved to the front of the order, one chatty harness could keep its own
	// turn resident while evicting every other harness's.
	registry := NewRegistry(endpoint())
	if _, err := registry.Start("ctx-held", turn("turn-held")); err != nil {
		t.Fatalf("start: %v", err)
	}
	for index := 0; index < maximumContexts-1; index++ {
		if _, err := registry.Start("ctx-fill-"+itoa(index), turn("turn-fill")); err != nil {
			t.Fatalf("fill %d: %v", index, err)
		}
	}
	if _, err := registry.Start("ctx-held", turn("turn-held-again")); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if resolved := registry.Resolve("ctx-held"); resolved.TurnID != "turn-held-again" {
		t.Fatalf("resolved = %+v, want the restated turn", resolved)
	}

	// One more turn takes the registry over its bound, and the oldest is the
	// held one whether or not it was restated.
	if _, err := registry.Start("ctx-last", turn("turn-last")); err != nil {
		t.Fatalf("last: %v", err)
	}
	if registry.Active() != maximumContexts {
		t.Fatalf("active = %d, want %d", registry.Active(), maximumContexts)
	}
	if registry.Resolve("ctx-held").TurnID == "turn-held-again" {
		t.Fatal("restating a turn moved it to the front of the eviction order")
	}
}

func TestTheRegistryEvictsTheOldestTurn(t *testing.T) {
	registry := NewRegistry(endpoint())
	for index := 0; index < maximumContexts+5; index++ {
		if _, err := registry.Start("ctx-"+itoa(index), turn("turn-"+itoa(index))); err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
	}
	if registry.Active() != maximumContexts {
		t.Fatalf("active = %d, want %d", registry.Active(), maximumContexts)
	}
	if registry.Resolve("ctx-0").Correlation != CorrelationAmbiguous {
		t.Fatal("the oldest turn survived eviction")
	}
	if registry.Resolve("ctx-260").TurnID != "turn-260" {
		t.Fatal("the newest turn was evicted")
	}
}

func TestATurnMissingAnIdentifierIsRefused(t *testing.T) {
	// Every one of these becomes a grouping key. A turn stored without one is
	// a row that cannot be joined to anything, discovered long after the call.
	registry := NewRegistry(endpoint())
	for name, context := range map[string]Context{
		"no agent":   {DisplayName: "R", SessionID: "s", TurnID: "t"},
		"no session": {AgentID: "a", DisplayName: "R", TurnID: "t"},
		"no turn":    {AgentID: "a", DisplayName: "R", SessionID: "s"},
		"no name":    {AgentID: "a", SessionID: "s", TurnID: "t"},
	} {
		if _, err := registry.Start("ctx-a", context); err == nil {
			t.Fatalf("%s: a context with no %s was accepted", name, name)
		}
	}
}

func TestAnUnsafeIdentifierIsRefused(t *testing.T) {
	registry := NewRegistry(endpoint())
	unsafe := map[string]Context{
		"newline in the turn":     turnWith(func(c *Context) { c.TurnID = "turn\na" }),
		"control in the name":     turnWith(func(c *Context) { c.DisplayName = "Rev\x00iewer" }),
		"leading punctuation":     turnWith(func(c *Context) { c.AgentID = "-reviewer" }),
		"space in the session":    turnWith(func(c *Context) { c.SessionID = "session one" }),
		"unsafe optional harness": turnWith(func(c *Context) { c.Harness = "codex cli" }),
	}
	for name, context := range unsafe {
		if _, err := registry.Start("ctx-a", context); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := registry.Start("ctx bad", turn("turn-a")); err == nil {
		t.Fatal("an unsafe context id was accepted")
	}
}

func TestATurnInheritsTheEndpointItDidNotName(t *testing.T) {
	// The proxy knows which endpoint it forwards to. A harness that omitted it
	// was not disagreeing, and dropping it would lose a fact already held.
	registry := NewRegistry(endpoint())
	bare := turn("turn-a")
	bare.Model, bare.EndpointID = "", ""
	if _, err := registry.Start("ctx-a", bare); err != nil {
		t.Fatalf("start: %v", err)
	}
	resolved := registry.Resolve("ctx-a")
	if resolved.Model != "ds-0731" || resolved.EndpointID != "dgx-primary" {
		t.Fatalf("resolved = %+v, want the endpoint filled in", resolved)
	}
}

func turnWith(change func(*Context)) Context {
	context := turn("turn-a")
	change(&context)
	return context
}
