package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
)

// parityCase is one event and the exact result the Python collector produced
// for it.
type parityCase struct {
	Name  string          `json:"name"`
	Event json.RawMessage `json:"event"`
	// Want is "OK" for an accepted event, or "code@path" for a refused one.
	Want string `json:"want"`
}

// TestParityWithTheCollectorItReplaces checks this validator against the one it
// is replacing.
//
// The expectations in testdata/parity.json were produced by running
// buzz-agent-observability's collector/schema.py over these exact events. They
// are not what this package was written to do — they are what the software
// being retired actually did, recorded before it is retired.
//
// The comparison includes the error path, not just the code, because a producer
// fixing a rejected event needs to be told the same field the old collector
// would have named. A port that refuses the same events for cosmetically
// different reasons is not a port a producer can migrate to without noticing.
func TestParityWithTheCollectorItReplaces(t *testing.T) {
	data, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []parityCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("only %d parity cases; the fixture has been truncated", len(cases))
	}

	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal(testCase.Event, &value); err != nil {
				t.Fatal(err)
			}
			got := "OK"
			if _, err := ValidateEvent(value); err != nil {
				var validation ValidationError
				if !errors.As(err, &validation) {
					t.Fatalf("error %v is not a ValidationError", err)
				}
				got = fmt.Sprintf("%s@%s", validation.Code, validation.Path)
			}
			if got != testCase.Want {
				t.Fatalf("got %s, the collector this replaces gave %s", got, testCase.Want)
			}
		})
	}
}
