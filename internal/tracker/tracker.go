// Package tracker defines the tracker-adapter interface the orchestrator
// depends on (SPEC §11.1). Concrete adapters live in subpackages.
package tracker

import (
	"context"

	"github.com/tomi/my-symphony/internal/domain"
)

// Client is the tracker adapter contract (SPEC §11.1). The orchestrator depends
// only on this interface, never on a concrete adapter.
type Client interface {
	// FetchCandidateIssues returns issues in configured active states for the
	// configured project (SPEC §11.1(1)).
	FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error)
	// FetchIssuesByStates returns issues in the given states, used for startup
	// terminal cleanup (SPEC §11.1(2)).
	FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error)
	// FetchIssueStatesByIDs returns minimal normalized issues for reconciliation
	// (SPEC §11.1(3)).
	FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error)
}

// Error is a typed tracker error carrying a spec error code (SPEC §11.4).
type Error struct {
	Code    string
	Msg     string
	Wrapped error
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}
func (e *Error) Unwrap() error { return e.Wrapped }

// Error codes (SPEC §11.4).
const (
	CodeUnsupportedTrackerKind    = "unsupported_tracker_kind"
	CodeMissingTrackerAPIKey      = "missing_tracker_api_key"
	CodeMissingTrackerProjectSlug = "missing_tracker_project_slug"
	CodeLinearAPIRequest          = "linear_api_request"
	CodeLinearAPIStatus           = "linear_api_status"
	CodeLinearGraphQLErrors       = "linear_graphql_errors"
	CodeLinearUnknownPayload      = "linear_unknown_payload"
	CodeLinearMissingEndCursor    = "linear_missing_end_cursor"
)
