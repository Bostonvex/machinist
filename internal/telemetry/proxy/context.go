package proxy

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// declaration is what a harness sends to the context route.
//
// The two actions are separate fields rather than one polymorphic body so that
// a malformed declaration is refused by decoding rather than by a chain of
// checks that could each be the one that was forgotten.
type declaration struct {
	Action    string      `json:"action"`
	Context   *contextRow `json:"context"`
	ContextID string      `json:"context_id"`
}

// contextRow is the wire form of Context. It exists so the wire names are
// written once, next to the decoder that reads them.
type contextRow struct {
	ContextID   string `json:"context_id"`
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name"`
	Harness     string `json:"harness"`
	Model       string `json:"model"`
	EndpointID  string `json:"endpoint_id"`
	SessionID   string `json:"session_id"`
	TurnID      string `json:"turn_id"`
}

// serveContext starts and ends turns.
//
// It is the only route that changes what this process will say about the calls
// it forwards, so it is authenticated, bounded, and closed to bodies it does
// not fully understand. A declaration that is nearly right is refused: a turn
// attributed to the wrong agent is worse than a turn nobody claimed, because
// the first is read as evidence and the second is read as a gap.
func (s *Server) serveContext(response http.ResponseWriter, request *http.Request) {
	if !s.settings.ContextsAccepted() {
		// No token was configured, so no caller can be authenticated, so there
		// is no caller this route may serve.
		refuse(response, http.StatusNotFound, "contexts_not_accepted")
		return
	}
	if !s.authenticated(request) {
		response.Header().Set("WWW-Authenticate", "Bearer")
		refuse(response, http.StatusUnauthorized, "invalid_token")
		return
	}
	if request.ContentLength < 0 || request.ContentLength > MaximumContextBytes {
		refuse(response, http.StatusRequestEntityTooLarge, "invalid_body")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaximumContextBytes+1))
	if err != nil || len(body) > MaximumContextBytes || len(body) < 2 {
		refuse(response, http.StatusRequestEntityTooLarge, "invalid_body")
		return
	}

	active, err := s.declare(body)
	if err != nil {
		refuse(response, http.StatusUnprocessableEntity, "invalid_context")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	fmt.Fprintf(response, `{"status":"ok","active_contexts":%d}`, active)
}

// authenticated compares the bearer token in constant time.
func (s *Server) authenticated(request *http.Request) bool {
	header := request.Header.Get("Authorization")
	scheme, presented, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return hmac.Equal([]byte(strings.TrimSpace(presented)), []byte(s.settings.contextToken))
}

// declare applies one declaration and reports how many turns are then active.
func (s *Server) declare(body []byte) (int, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	// An unknown field is a caller that means something this proxy does not
	// implement. Accepting the part that was understood would record a turn
	// under terms nobody agreed to.
	decoder.DisallowUnknownFields()

	var declared declaration
	if err := decoder.Decode(&declared); err != nil {
		return 0, ErrInvalidContext
	}
	if decoder.More() {
		return 0, ErrInvalidContext
	}

	switch declared.Action {
	case "start":
		if declared.Context == nil || declared.ContextID != "" {
			return 0, ErrInvalidContext
		}
		row := *declared.Context
		return s.registry.Start(row.ContextID, Context{
			AgentID:     row.AgentID,
			DisplayName: row.DisplayName,
			Harness:     row.Harness,
			Model:       row.Model,
			EndpointID:  row.EndpointID,
			SessionID:   row.SessionID,
			TurnID:      row.TurnID,
		})
	case "end":
		if declared.Context != nil {
			return 0, ErrInvalidContext
		}
		return s.registry.End(declared.ContextID)
	default:
		return 0, ErrInvalidContext
	}
}
