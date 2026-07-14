package claude

import (
	"context"

	"github.com/tomi/my-symphony/internal/agent"
)

// Backend adapts *Client to the agent.Backend interface so the runner can treat
// the Claude Code session handle opaquely (SPEC §10.7).
type Backend struct {
	client *Client
}

// NewBackend wraps a Client as an agent.Backend.
func NewBackend(c *Client) Backend { return Backend{client: c} }

func (b Backend) StartSession(workspace, identifier, title string) any {
	return b.client.StartSession(workspace, identifier, title)
}

func (b Backend) RunTurn(ctx context.Context, session any, prompt string, emit func(agent.Event)) (*agent.TurnResult, error) {
	s := session.(*Session)
	res, err := b.client.RunTurn(ctx, s, prompt, emit)
	if err != nil {
		return nil, err
	}
	return &agent.TurnResult{Usage: res.Usage}, nil
}

func (b Backend) StopSession(session any) {
	b.client.StopSession(session.(*Session))
}

var _ agent.Backend = Backend{}
