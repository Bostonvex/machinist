package config

import "slices"

// ErrorClass names why an attempt failed.
//
// The set is closed. An attempt that failed for a reason outside it is recorded
// as ClassUnknown rather than as a new class invented at the call site, so the
// classes an operator can configure a route against are exactly the classes the
// store can hold. Two independently maintained lists would eventually disagree,
// and the way that disagreement surfaces is a route that refuses to load over a
// reason the rest of the system uses daily.
type ErrorClass string

const (
	// The operator's setup is wrong.
	ClassConfiguration  ErrorClass = "configuration"
	ClassAuthentication ErrorClass = "authentication"
	ClassPolicy         ErrorClass = "policy"

	// The profile could not be reached, or could not finish.
	ClassRateLimit        ErrorClass = "rate_limit"
	ClassCapacity         ErrorClass = "capacity"
	ClassTransient        ErrorClass = "transient"
	ClassTransport        ErrorClass = "transport"
	ClassHarnessCrash     ErrorClass = "harness_crash"
	ClassTimeout          ErrorClass = "timeout"
	ClassModelUnavailable ErrorClass = "model_unavailable"

	// The profile ran and the work it produced did not hold up.
	ClassTestFailure ErrorClass = "test_failure"

	// Neither a failure of the work nor of the infrastructure.
	ClassCancelled ErrorClass = "cancelled"
	ClassUnknown   ErrorClass = "unknown"
)

// fallbackable records, for every class, whether a route may be configured to
// try its next profile when an attempt fails this way.
//
// Every class appears here, including the ones that are false. A map that only
// listed the permitted classes would make "absent" mean both "deliberately
// refused" and "nobody has added it yet", and the second reading is how
// test_failure came to be a class the store records, the worker reports, and a
// route could not be configured against.
var fallbackable = map[ErrorClass]bool{
	// The next profile has a real chance of doing better, because what failed
	// belonged to this one: its quota, its capacity, its process, its model.
	ClassRateLimit:        true,
	ClassCapacity:         true,
	ClassTransient:        true,
	ClassTransport:        true,
	ClassHarnessCrash:     true,
	ClassTimeout:          true,
	ClassModelUnavailable: true,

	// The work, not the infrastructure. A different model is the one thing that
	// plausibly changes the outcome, which is most of why a route lists more
	// than one profile for implementation work in the first place.
	ClassTestFailure: true,

	// The operator's setup is wrong, and the next profile is not a fix for it.
	// Falling back here would hide a broken credential or a rejected policy
	// behind whichever profile happens to be configured correctly, and the
	// operator would never learn which one was broken.
	ClassConfiguration:  false,
	ClassAuthentication: false,
	ClassPolicy:         false,

	// Someone stopped this on purpose. Starting it again elsewhere overrides
	// that decision rather than recovering from a failure.
	ClassCancelled: false,

	// The class of last resort. Permitting fallback on it would make every
	// failure nobody classified into a retry, which is retry-on-everything
	// wearing an allowlist.
	ClassUnknown: false,
}

// Valid reports whether the class is one the system recognises.
func (c ErrorClass) Valid() bool { _, ok := fallbackable[c]; return ok }

// AllowsFallback reports whether a route may list this class in fallback_on.
// An unrecognised class allows nothing.
func (c ErrorClass) AllowsFallback() bool { return fallbackable[c] }

// FallbackClasses returns the classes a route may be configured to fall back
// on, sorted, for error messages that tell an operator what was available
// instead of only what was rejected.
func FallbackClasses() []ErrorClass {
	classes := make([]ErrorClass, 0, len(fallbackable))
	for class, allowed := range fallbackable {
		if allowed {
			classes = append(classes, class)
		}
	}
	slices.Sort(classes)
	return classes
}
