package claude

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
)

// maxSummaryLen bounds the assistant text carried on an agent event. It is set
// generously so observability surfaces (the dashboard activity feed) show
// readable messages rather than one-line fragments; the orchestrator applies its
// own tighter truncation for the compact "last message" field.
const maxSummaryLen = 2000

// maxDetailLen bounds the expandable per-step detail (tool inputs/results,
// thinking text) carried alongside the summary. It is larger than maxSummaryLen
// because the detail is folded away by default in the dashboard.
const maxDetailLen = 4000

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
		// Mid-stream assistant usage deltas are not accumulated into turn/global
		// totals (the terminal result carries the authoritative cumulative usage,
		// SPEC §13.5). We do surface the per-message usage as StepUsage purely for
		// the observability feed's per-step token display.
		summary, detail := describeMessage(evt.Message)
		emit(agent.Event{Event: agent.EventNotification, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
			Message: summary, Detail: detail, StepUsage: extractMessageUsage(evt.Message),
			RateLimits: extractRateLimits(evt.raw)})
		return false, nil, nil

	case "user":
		summary, detail := describeMessage(evt.Message)
		emit(agent.Event{Event: agent.EventOtherMessage, Timestamp: now,
			AgentPID: pid, SessionID: s.SessionID, TurnID: turnID,
			Message: summary, Detail: detail})
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
	return usageFromMap(m)
}

// extractMessageUsage reads the per-message usage nested under an assistant
// message object (message.usage). It is used only for the per-step token display
// on the observability feed and is never accumulated into turn/global totals.
func extractMessageUsage(raw json.RawMessage) *agent.Usage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return nil
	}
	return usageFromMap(u)
}

// usageFromMap builds a Usage from a leniently-typed usage map. Input tokens fold
// in cache read/creation counts to match the terminal-result accounting.
func usageFromMap(m map[string]any) *agent.Usage {
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

// describeMessage renders a message's content blocks into two views for the
// observability feed: a compact one-line summary (tool names, block types, and
// any assistant prose) and a fuller detail (thinking text, tool inputs, tool
// results) that the dashboard folds away by default. Both are bounded.
func describeMessage(raw json.RawMessage) (summary, detail string) {
	if len(raw) == 0 {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}
	content, ok := m["content"].([]any)
	if !ok {
		return "", ""
	}
	var summaryParts, detailParts []string
	for _, block := range content {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "text":
			if t, _ := bm["text"].(string); t != "" {
				summaryParts = append(summaryParts, t)
			}
		case "thinking":
			summaryParts = append(summaryParts, "thinking")
			if t, _ := bm["thinking"].(string); t != "" {
				detailParts = append(detailParts, "thinking\n"+t)
			}
		case "tool_use":
			name, _ := bm["name"].(string)
			if name == "" {
				name = "tool_use"
			}
			summaryParts = append(summaryParts, name)
			detailParts = append(detailParts, "→ "+name+" "+encodeInput(bm["input"]))
		case "tool_result":
			summaryParts = append(summaryParts, "tool_result")
			if r := toolResultText(bm["content"]); r != "" {
				detailParts = append(detailParts, "← result\n"+r)
			}
		default:
			if typ, ok := bm["type"].(string); ok {
				summaryParts = append(summaryParts, typ)
			}
		}
	}
	summary = truncate(strings.TrimSpace(strings.Join(summaryParts, " ")), maxSummaryLen)
	detail = truncate(strings.TrimSpace(strings.Join(detailParts, "\n\n")), maxDetailLen)
	return summary, detail
}

// encodeInput renders a tool_use input payload as compact JSON, falling back to a
// best-effort string form when it cannot be marshaled.
func encodeInput(input any) string {
	if input == nil {
		return "{}"
	}
	if b, err := json.Marshal(input); err == nil {
		return string(b)
	}
	return ""
}

// toolResultText flattens a tool_result content field, which the CLI emits either
// as a plain string or as an array of text sub-blocks.
func toolResultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			if bm, ok := block.(map[string]any); ok {
				if t, _ := bm["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
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
