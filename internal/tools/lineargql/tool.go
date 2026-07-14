// Package lineargql implements the OPTIONAL linear_graphql client-side tool
// (SPEC §10.5). It executes exactly one GraphQL operation per call against the
// configured Linear endpoint/auth and returns structured results the model can
// inspect in-session.
package lineargql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const requestTimeout = 30 * time.Second

// Tool executes GraphQL against Linear using Symphony's configured auth.
type Tool struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// New builds a Tool. A non-Linear tracker or missing auth is handled at execute
// time so the session never stalls (SPEC §10.5).
func New(endpoint, apiKey string, httpClient *http.Client) *Tool {
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Tool{endpoint: endpoint, apiKey: apiKey, httpClient: httpClient}
}

// Input is the preferred tool input shape (SPEC §10.5).
type Input struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// Result is the structured tool output (SPEC §10.5).
type Result struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Errors  json.RawMessage `json:"errors,omitempty"`
	Error   *ResultError    `json:"error,omitempty"`
}

// ResultError describes an invalid-input/auth/transport failure.
type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ParseInput accepts the preferred object shape or a raw query string shorthand
// (SPEC §10.5).
func ParseInput(raw json.RawMessage) (Input, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Input{}, fmt.Errorf("empty input")
	}
	// Raw string shorthand.
	if trimmed[0] == '"' {
		var q string
		if err := json.Unmarshal(trimmed, &q); err != nil {
			return Input{}, err
		}
		return Input{Query: q}, nil
	}
	var in Input
	if err := json.Unmarshal(trimmed, &in); err != nil {
		return Input{}, err
	}
	return in, nil
}

// Execute runs the tool contract and never returns an error: all failures are
// surfaced as Result{Success:false} so the session continues (SPEC §10.5).
func (t *Tool) Execute(ctx context.Context, in Input) Result {
	if strings.TrimSpace(in.Query) == "" {
		return failure("invalid_input", "query must be a non-empty string")
	}
	if n := countOperations(in.Query); n != 1 {
		return failure("invalid_input",
			fmt.Sprintf("query must contain exactly one GraphQL operation, found %d", n))
	}
	if strings.TrimSpace(t.apiKey) == "" {
		return failure("missing_auth", "Linear auth is not configured")
	}

	body, status, err := t.post(ctx, in)
	if err != nil {
		return failure("transport_error", err.Error())
	}
	if status != http.StatusOK {
		return failure("transport_error", fmt.Sprintf("status %d", status))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return failure("transport_error", "malformed GraphQL response")
	}

	res := Result{Data: envelope.Data, Errors: envelope.Errors}
	// Top-level GraphQL errors -> success=false but preserve the body (SPEC §10.5).
	if len(envelope.Errors) > 0 && string(envelope.Errors) != "null" {
		res.Success = false
		return res
	}
	res.Success = true
	return res
}

func (t *Tool) post(ctx context.Context, in Input) ([]byte, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	payload := map[string]any{"query": in.Query}
	if in.Variables != nil {
		payload["variables"] = in.Variables
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, t.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", t.apiKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return data, resp.StatusCode, nil
}

func failure(code, msg string) Result {
	return Result{Success: false, Error: &ResultError{Code: code, Message: msg}}
}
