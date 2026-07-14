package claude

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
)

// streamEvent is a loosely-typed stream-json event. The Claude Code CLI is the
// source of truth for the exact shapes; we read leniently (SPEC §10.3).
type streamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	IsError   *bool           `json:"is_error"`
	Usage     json.RawMessage `json:"usage"`
	Result    json.RawMessage `json:"result"`
	Message   json.RawMessage `json:"message"`
	raw       map[string]any
}

func parseLine(line []byte) (*streamEvent, error) {
	var evt streamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(line, &evt.raw)
	return &evt, nil
}

// handleEvent dispatches one parsed event. It returns (done, result, error).
// done=true with a nil error means the turn completed successfully.
func (c *Client) handleEvent(s *Session, turnID string, pid *string, evt *streamEvent,
	sessionStartedEmitted *bool, emit func(agent.Event)) (bool, *TurnResult, error) {

	now := time.Now().UTC()

	switch evt.Type {
	case "system":
		if evt.Subtype == "init" && evt.SessionID != "" {
			s.SessionID = evt.SessionID
			if !*sessionStartedEmitted {
				*sessionStartedEmitted = true
				emit(agent.Event{Event: agent.EventSessionStarted, Timestamp: now,
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID})
			}
		} else {
			emit(agent.Event{Event: agent.EventOtherMessage, Timestamp: now,
				AgentPID: pid, SessionID: s.SessionID, TurnID: turnID})
		}
		return false, nil, nil

	case "assistant":
		// Mid-stream assistant usage deltas are ignored (SPEC §13.5).
		emit(agent.Event{Event: agent.EventNotification, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
			Message: summarizeMessage(evt.Message), RateLimits: extractRateLimits(evt.raw)})
		return false, nil, nil

	case "user":
		emit(agent.Event{Event: agent.EventOtherMessage, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
			Message: summarizeMessage(evt.Message)})
		return false, nil, nil

	case "result":
		// The terminal result may carry the session id as a fallback (SPEC §10.2).
		if evt.SessionID != "" {
			s.SessionID = evt.SessionID
			if !*sessionStartedEmitted {
				*sessionStartedEmitted = true
				emit(agent.Event{Event: agent.EventSessionStarted, Timestamp: now,
					AgentPID: pid, SessionID: s.SessionID, TurnID: turnID})
			}
		}
		usage := extractUsage(evt.Usage)
		rate := extractRateLimits(evt.raw)
		isErr := isErrorResult(evt)
		if isErr {
			emit(agent.Event{Event: agent.EventTurnEndedWithError, Timestamp: now,
				AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
				Usage: usage, RateLimits: rate, Message: "result reported error"})
			return false, nil, &Error{Code: CodeResponseError, Msg: "result is_error"}
		}
		emit(agent.Event{Event: agent.EventTurnCompleted, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
			Usage: usage, RateLimits: rate})
		return true, &TurnResult{Usage: usage}, nil

	default:
		emit(agent.Event{Event: agent.EventOtherMessage, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID})
		return false, nil, nil
	}
}

func isErrorResult(evt *streamEvent) bool {
	if evt.IsError != nil && *evt.IsError {
		return true
	}
	st := strings.ToLower(evt.Subtype)
	if st != "" && st != "success" && strings.Contains(st, "error") {
		return true
	}
	return false
}

// extractUsage reads token counts leniently from the result usage map (SPEC §13.5).
func extractUsage(raw json.RawMessage) *agent.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	input := asInt(m["input_tokens"]) + asInt(m["cache_read_input_tokens"]) + asInt(m["cache_creation_input_tokens"])
	output := asInt(m["output_tokens"])
	return &agent.Usage{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
	}
}

// extractRateLimits returns the rate-limit payload if the event carries one
// (SPEC §13.5). Best-effort: any "rate_limits" field.
func extractRateLimits(raw map[string]any) any {
	if raw == nil {
		return nil
	}
	if rl, ok := raw["rate_limits"]; ok {
		return rl
	}
	return nil
}

func summarizeMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	// Try to pull a concise text summary from content blocks.
	if content, ok := m["content"].([]any); ok {
		var parts []string
		for _, block := range content {
			if bm, ok := block.(map[string]any); ok {
				if t, ok := bm["text"].(string); ok {
					parts = append(parts, t)
				} else if typ, ok := bm["type"].(string); ok {
					parts = append(parts, typ)
				}
			}
		}
		s := strings.TrimSpace(strings.Join(parts, " "))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	return ""
}

func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	default:
		return 0
	}
}
