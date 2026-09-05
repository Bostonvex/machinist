package config

import (
	"strings"
	"testing"
)

// TestEveryClassStatesWhetherItMayFallBack is the guard that would have caught
// the defect this file exists because of: test_failure was a class the worker
// reported and the store accepted, but the fallback table had never heard of
// it, so a route configured against it refused to load and every command in
// that route was unrunnable.
func TestEveryClassStatesWhetherItMayFallBack(t *testing.T) {
	declared := []ErrorClass{
		ClassConfiguration, ClassAuthentication, ClassPolicy,
		ClassRateLimit, ClassCapacity, ClassTransient, ClassTransport,
		ClassHarnessCrash, ClassTimeout, ClassModelUnavailable,
		ClassTestFailure, ClassCancelled, ClassUnknown,
	}
	for _, class := range declared {
		if !class.Valid() {
			t.Errorf("class %q is declared but the fallback table does not decide it", class)
		}
	}
	if len(fallbackable) != len(declared) {
		t.Errorf("the fallback table decides %d classes, %d are declared", len(fallbackable), len(declared))
	}
}

func TestAnUnrecognisedClassAllowsNothing(t *testing.T) {
	for _, value := range []string{"", "guess", "TEST_FAILURE", " timeout", "flaky"} {
		class := ErrorClass(value)
		if class.Valid() {
			t.Errorf("%q was recognised as a class", value)
		}
		if class.AllowsFallback() {
			t.Errorf("%q was permitted as a fallback reason", value)
		}
	}
}

// A misconfiguration is not something the next profile fixes. Falling back over
// it would hide a broken credential behind whichever profile happens to be set
// up correctly, and the operator would be told nothing.
func TestTheOperatorsOwnMistakesAreNotFallbackReasons(t *testing.T) {
	for _, class := range []ErrorClass{ClassConfiguration, ClassAuthentication, ClassPolicy, ClassCancelled, ClassUnknown} {
		if !class.Valid() {
			t.Errorf("%q should still be a recordable class", class)
		}
		if class.AllowsFallback() {
			t.Errorf("a route may fall back on %q", class)
		}
	}
}

// The route in the operator's own configuration that could not load.
func TestARouteMayFallBackOnATestFailure(t *testing.T) {
	if !ClassTestFailure.AllowsFallback() {
		t.Fatal("a route cannot be configured to try another model when the work fails its tests")
	}
	routes := map[string]Route{"implementation": {
		Profiles:   []string{"one", "two"},
		FallbackOn: []string{"capacity", "rate_limit", "transient", "model_unavailable", "harness_crash", "timeout", "test_failure"},
	}}
	if err := validateRoutes(routes); err != nil {
		t.Fatalf("the configured route was refused: %v", err)
	}
}

// A refusal that only says no leaves the operator guessing. The message names
// what was available, which is the whole difference between an error that is
// actionable and one that sends someone to read the source.
func TestARefusedReasonNamesTheOnesThatWork(t *testing.T) {
	err := validateRoutes(map[string]Route{"implementation": {
		Profiles:   []string{"one"},
		FallbackOn: []string{"policy"},
	}})
	if err == nil {
		t.Fatal("a route fell back on a class that is not a fallback reason")
	}
	for _, want := range []string{"policy", "test_failure", "timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}
