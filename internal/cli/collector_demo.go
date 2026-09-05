package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/owainlewis/machinist/internal/telemetry"
	"github.com/spf13/cobra"
)

// demoAgentPrefix marks every agent this verb invents.
//
// It is a prefix rather than a flag on the event because the wire contract has
// no field for "this is not real" — and adding one would mean every reader of
// every event had to check it. A name an operator can see in the dashboard and
// filter on is the honest version of the same thing.
const demoAgentPrefix = "demo-agent-"

func newCollectorDemoCommand(options *commandOptions) *cobra.Command {
	var confirmed bool
	demo := &cobra.Command{
		Use:   "demo",
		Short: "Ingest synthetic agent turns so the dashboard has something to show",
		Long: "Send two complete synthetic turns through the collector's ingest route.\n\n" +
			"This exists to answer \"is any of this working\" without waiting for a real\n" +
			"agent. It goes over HTTP with the configured token rather than writing to\n" +
			"the database directly, so a success means the listener, the token, the\n" +
			"schema and the store all agree — which is the question being asked.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			// The confirmation is required because these turns are
			// indistinguishable from real ones once stored, except by the
			// agent names. They move the averages an operator reads. Same
			// reasoning as purge, and a flag rather than a prompt for the same
			// reason: this runs from scripts as well as terminals.
			if !confirmed {
				return errors.New("demo writes synthetic turns into the telemetry record: pass --confirm-synthetic-events")
			}
			collectorConfig, err := enabledCollectorConfig(options)
			if err != nil {
				return err
			}
			token, err := telemetry.LoadOrCreateToken(collectorConfig.TokenFile)
			if err != nil {
				return err
			}
			events := syntheticTurns(time.Now().UTC())
			accepted, inserted, err := postDemoEvents(command.Context(),
				"http://"+collectorConfig.Listen+telemetry.IngestPath, token, events)
			if err != nil {
				return err
			}
			// Both numbers, because they can differ: the collector deduplicates
			// on event id, and an operator running this twice should be told
			// that the second run stored nothing rather than left to wonder why
			// the dashboard did not change.
			fmt.Fprintf(options.stdout,
				"machinist: the collector accepted %d synthetic events across 2 turns and stored %d\n",
				accepted, inserted)
			fmt.Fprintf(options.stdout,
				"machinist: they belong to agents named %s*; purge or ignore them accordingly\n",
				demoAgentPrefix)
			return nil
		},
	}
	demo.Flags().BoolVar(&confirmed, "confirm-synthetic-events", false,
		"acknowledge that synthetic turns are being written into the telemetry record")
	return demo
}

// syntheticTurns builds two complete turns: one that ran quickly and one that
// took longer and used a tool.
//
// Two rather than one, because a dashboard with a single row shows nothing
// about how it ranks or compares, which is most of what it is for. They are
// complete rather than partial because a turn with no completion sits in the
// live view forever, and an operator running this to check the collector would
// be left with an artefact that looks like a hung agent.
func syntheticTurns(at time.Time) []telemetry.Event {
	agents := []struct {
		name     string
		duration float64
		ttfa     float64
		ttfvt    float64
		tools    int64
	}{
		{"Synthetic implementor", 1050, 150, 220, 0},
		{"Synthetic reviewer", 1200, 180, 260, 1},
	}

	var events []telemetry.Event
	for index, agent := range agents {
		id := fmt.Sprintf("%s%d", demoAgentPrefix, index+1)
		turn := "demo-turn-" + identifier()
		emit := func(eventType telemetry.EventType, offset float64, attributes map[string]any) {
			events = append(events, telemetry.Event{
				SchemaVersion:     telemetry.SchemaVersion,
				EventID:           identifier(),
				EventType:         eventType,
				ObservedAt:        at.Add(time.Duration(offset) * time.Millisecond).Format(time.RFC3339Nano),
				MonotonicOffsetMS: offset,
				Producer: telemetry.Producer{
					Name: "machinist", Version: "demo", InstanceID: id,
				},
				Agent:      telemetry.Agent{ID: id, DisplayName: agent.name},
				Harness:    demoText("machinist-demo"),
				Model:      demoText("synthetic"),
				EndpointID: demoText("synthetic"),
				SessionID:  demoText("demo-session-" + id),
				TurnID:     &turn,
				Attributes: attributes,
			})
		}

		emit(telemetry.EventTurnStarted, 0, map[string]any{
			"turn_class": "implementation", "measurement_quality": "exact",
		})
		emit(telemetry.EventTurnFirstActivity, agent.ttfa, map[string]any{
			"elapsed_ms": agent.ttfa, "update_kind": "agent_message_chunk",
			"measurement_quality": "exact",
		})
		emit(telemetry.EventTurnFirstVisibleText, agent.ttfvt, map[string]any{
			"elapsed_ms": agent.ttfvt, "measurement_quality": "exact",
		})
		emit(telemetry.EventTurnCompleted, agent.duration, map[string]any{
			"duration_ms": agent.duration, "ttfa_ms": agent.ttfa, "ttfvt_ms": agent.ttfvt,
			"max_stall_ms": 210.0, "tool_count": agent.tools,
			"tool_observation_mode": "acp_updates", "outcome": "completed",
			"measurement_quality": "exact",
		})
	}
	return events
}

// postDemoEvents submits the batch and reports how many were accepted.
func postDemoEvents(ctx context.Context, endpoint, token string, events []telemetry.Event) (int, int, error) {
	body, err := json.Marshal(events)
	if err != nil {
		return 0, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		// The collector not being up is the most likely reason to be here, and
		// it is the one an operator can act on.
		return 0, 0, fmt.Errorf("the collector is not answering on %s: start it with `machinist collector start` (%w)",
			endpoint, err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		// The body is included rather than summarised. A refusal from ingest
		// names the field it refused, and that is the whole diagnostic.
		return 0, 0, fmt.Errorf("the collector refused the synthetic events with HTTP %d: %s",
			response.StatusCode, bytes.TrimSpace(answer))
	}
	var taken struct {
		Accepted *int `json:"accepted"`
		Inserted *int `json:"inserted"`
	}
	if err := json.Unmarshal(answer, &taken); err != nil || taken.Accepted == nil || taken.Inserted == nil {
		// A 202 whose body does not say what was taken is not a success this
		// command can report. Guessing "all of them" would be the one answer
		// that cannot be wrong on the screen and right in the database.
		return 0, 0, fmt.Errorf("the collector accepted the batch but did not say how many events it took: %s",
			bytes.TrimSpace(answer))
	}
	return *taken.Accepted, *taken.Inserted, nil
}

func demoText(value string) *string { return &value }

// identifier is a UUIDv4, which is what the ingest contract requires of an
// event id.
func identifier() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic("machinist: no randomness for a demo identifier: " + err.Error())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
