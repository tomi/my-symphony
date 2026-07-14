package linear

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
)

// issueFields is the shared GraphQL selection for an issue node (SPEC §4.1.1,
// §11.2). It includes labels and inverse `blocks` relations for blocker
// derivation.
const issueFields = `
    id
    identifier
    title
    description
    priority
    branchName
    url
    createdAt
    updatedAt
    state { name type }
    assignee { name }
    labels { nodes { name } }
    inverseRelations { nodes { type relatedIssue { id identifier state { name } } } }
`

type graphQLResponse struct {
	Data   *graphQLData   `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type graphQLError struct {
	Message string `json:"message"`
}

type graphQLData struct {
	Issues issueConnection `json:"issues"`
}

type issueConnection struct {
	Nodes    []issueNode `json:"nodes"`
	PageInfo pageInfo    `json:"pageInfo"`
}

type pageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

type issueNode struct {
	ID          string          `json:"id"`
	Identifier  string          `json:"identifier"`
	Title       string          `json:"title"`
	Description *string         `json:"description"`
	Priority    json.RawMessage `json:"priority"`
	BranchName  *string         `json:"branchName"`
	URL         *string         `json:"url"`
	CreatedAt   *string         `json:"createdAt"`
	UpdatedAt   *string         `json:"updatedAt"`
	State       *stateNode      `json:"state"`
	Assignee    *assigneeNode   `json:"assignee"`
	Labels      *labelConn      `json:"labels"`
	Inverse     *relationConn   `json:"inverseRelations"`
}

type stateNode struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type assigneeNode struct {
	Name string `json:"name"`
}

type labelConn struct {
	Nodes []struct {
		Name string `json:"name"`
	} `json:"nodes"`
}

type relationConn struct {
	Nodes []relationNode `json:"nodes"`
}

type relationNode struct {
	Type         string     `json:"type"`
	RelatedIssue *issueNode `json:"relatedIssue"`
}

// normalizeIssue maps a raw Linear issue node into the domain model (SPEC §11.3).
func normalizeIssue(n issueNode) domain.Issue {
	iss := domain.Issue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		BranchName:  n.BranchName,
		URL:         n.URL,
		Priority:    normalizePriority(n.Priority),
		CreatedAt:   parseTime(n.CreatedAt),
		UpdatedAt:   parseTime(n.UpdatedAt),
	}
	if n.State != nil {
		iss.State = n.State.Name
	}
	if n.Assignee != nil && n.Assignee.Name != "" {
		name := n.Assignee.Name
		iss.Assignee = &name
	}
	if n.Labels != nil {
		for _, l := range n.Labels.Nodes {
			name := strings.ToLower(strings.TrimSpace(l.Name))
			if name != "" {
				iss.Labels = append(iss.Labels, name)
			}
		}
	}
	if n.Inverse != nil {
		for _, rel := range n.Inverse.Nodes {
			// blocked_by derives from inverse relations of type "blocks"
			// (SPEC §11.3).
			if strings.ToLower(strings.TrimSpace(rel.Type)) != "blocks" {
				continue
			}
			ref := domain.BlockerRef{}
			if rel.RelatedIssue != nil {
				ri := rel.RelatedIssue
				if ri.ID != "" {
					id := ri.ID
					ref.ID = &id
				}
				if ri.Identifier != "" {
					ident := ri.Identifier
					ref.Identifier = &ident
				}
				if ri.State != nil && ri.State.Name != "" {
					st := ri.State.Name
					ref.State = &st
				}
			}
			iss.BlockedBy = append(iss.BlockedBy, ref)
		}
	}
	return iss
}

// normalizePriority keeps integer priorities only; non-integers become nil
// (SPEC §11.3).
func normalizePriority(raw json.RawMessage) *int {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	if f != float64(int(f)) {
		return nil
	}
	p := int(f)
	return &p
}

// parseTime parses an ISO-8601 timestamp, returning nil on absence/parse
// failure (SPEC §11.3).
func parseTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}
