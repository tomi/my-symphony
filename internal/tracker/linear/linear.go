// Package linear implements the tracker.Client interface against the Linear
// GraphQL API (SPEC §11.2–§11.4). Query construction is kept isolated here so
// the exact fields/types can be audited and tested.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/tracker"
)

// DefaultEndpoint is the Linear GraphQL endpoint (SPEC §5.3.1).
const DefaultEndpoint = "https://api.linear.app/graphql"

// DefaultPageSize is the candidate pagination page size (SPEC §11.2).
const DefaultPageSize = 50

// requestTimeout is the per-request network timeout (SPEC §11.2).
const requestTimeout = 30 * time.Second

// Client is a Linear GraphQL adapter.
type Client struct {
	endpoint     string
	apiKey       string
	projectSlug  string
	activeStates []string
	pageSize     int
	httpClient   *http.Client
}

// Options configures a Client.
type Options struct {
	Endpoint     string
	APIKey       string
	ProjectSlug  string
	ActiveStates []string
	PageSize     int
	HTTPClient   *http.Client
}

// New constructs a Linear Client, validating required config (SPEC §11.4).
func New(opts Options) (*Client, error) {
	if opts.APIKey == "" {
		return nil, &tracker.Error{Code: tracker.CodeMissingTrackerAPIKey, Msg: "tracker.api_key is required"}
	}
	if opts.ProjectSlug == "" {
		return nil, &tracker.Error{Code: tracker.CodeMissingTrackerProjectSlug, Msg: "tracker.project_slug is required"}
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{
		endpoint:     endpoint,
		apiKey:       opts.APIKey,
		projectSlug:  opts.ProjectSlug,
		activeStates: opts.ActiveStates,
		pageSize:     pageSize,
		httpClient:   hc,
	}, nil
}

// FetchCandidateIssues returns paginated active-state issues for the project
// (SPEC §11.1(1), §11.2).
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	filter := map[string]any{
		"project": map[string]any{"slugId": map[string]any{"eq": c.projectSlug}},
	}
	if len(c.activeStates) > 0 {
		filter["state"] = map[string]any{"name": map[string]any{"in": c.activeStates}}
	}
	return c.paginateIssues(ctx, filter)
}

// FetchIssuesByStates returns issues in the given states for the project. An
// empty state list short-circuits with no API call (SPEC §11.1(2), §17.3).
func (c *Client) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if len(states) == 0 {
		return []domain.Issue{}, nil
	}
	filter := map[string]any{
		"project": map[string]any{"slugId": map[string]any{"eq": c.projectSlug}},
		"state":   map[string]any{"name": map[string]any{"in": states}},
	}
	return c.paginateIssues(ctx, filter)
}

// FetchIssueStatesByIDs returns minimal normalized issues for reconciliation,
// typing ids as [ID!] (SPEC §11.1(3), §11.2).
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	if len(ids) == 0 {
		return []domain.Issue{}, nil
	}
	query := `query IssueStates($ids: [ID!], $first: Int, $after: String) {
  issues(filter: { id: { in: $ids } }, first: $first, after: $after) {
    nodes { ` + issueFields + ` }
    pageInfo { hasNextPage endCursor }
  }
}`
	vars := map[string]any{"ids": ids, "first": c.pageSize}
	return c.paginateWith(ctx, query, vars)
}

// paginateIssues runs the candidate/state query with the given filter.
func (c *Client) paginateIssues(ctx context.Context, filter map[string]any) ([]domain.Issue, error) {
	query := `query CandidateIssues($filter: IssueFilter, $first: Int, $after: String) {
  issues(filter: $filter, first: $first, after: $after) {
    nodes { ` + issueFields + ` }
    pageInfo { hasNextPage endCursor }
  }
}`
	vars := map[string]any{"filter": filter, "first": c.pageSize}
	return c.paginateWith(ctx, query, vars)
}

// paginateWith executes a cursor-paginated issue query, preserving order across
// pages (SPEC §11.2, §17.3).
func (c *Client) paginateWith(ctx context.Context, query string, baseVars map[string]any) ([]domain.Issue, error) {
	var all []domain.Issue
	var after *string
	for {
		vars := make(map[string]any, len(baseVars)+1)
		for k, v := range baseVars {
			vars[k] = v
		}
		if after != nil {
			vars["after"] = *after
		}
		resp, err := c.do(ctx, query, vars)
		if err != nil {
			return nil, err
		}
		page := resp.Data.Issues
		for _, node := range page.Nodes {
			all = append(all, normalizeIssue(node))
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		if page.PageInfo.EndCursor == nil || *page.PageInfo.EndCursor == "" {
			return nil, &tracker.Error{
				Code: tracker.CodeLinearMissingEndCursor,
				Msg:  "page reports hasNextPage but omits endCursor",
			}
		}
		after = page.PageInfo.EndCursor
	}
	return all, nil
}

// do executes one GraphQL request and maps transport/status/graphql errors
// (SPEC §11.4).
func (c *Client) do(ctx context.Context, query string, vars map[string]any) (*graphQLResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearUnknownPayload, Msg: err.Error(), Wrapped: err}
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearAPIRequest, Msg: err.Error(), Wrapped: err}
	}
	req.Header.Set("Content-Type", "application/json")
	// Auth token in the Authorization header; never logged (SPEC §11.2, §15.3).
	req.Header.Set("Authorization", c.apiKey)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearAPIRequest, Msg: err.Error(), Wrapped: err}
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearAPIRequest, Msg: err.Error(), Wrapped: err}
	}
	if res.StatusCode != http.StatusOK {
		return nil, &tracker.Error{
			Code: tracker.CodeLinearAPIStatus,
			Msg:  fmt.Sprintf("status %d", res.StatusCode),
		}
	}

	var parsed graphQLResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearUnknownPayload, Msg: err.Error(), Wrapped: err}
	}
	if len(parsed.Errors) > 0 {
		return nil, &tracker.Error{
			Code: tracker.CodeLinearGraphQLErrors,
			Msg:  parsed.Errors[0].Message,
		}
	}
	if parsed.Data == nil {
		return nil, &tracker.Error{Code: tracker.CodeLinearUnknownPayload, Msg: "response has no data"}
	}
	return &parsed, nil
}

var _ tracker.Client = (*Client)(nil)
