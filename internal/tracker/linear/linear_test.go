package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tomi/my-symphony/internal/tracker"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := New(Options{Endpoint: srv.URL, APIKey: "tok", ProjectSlug: "proj",
		ActiveStates: []string{"Todo", "In Progress"}, PageSize: 2, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestCandidate_UsesSlugIdFilterAndNormalizes(t *testing.T) {
	var gotQuery string
	var gotVars map[string]any
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "tok" {
			t.Errorf("missing auth header")
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		gotVars = req.Variables
		w.Write([]byte(`{"data":{"issues":{"nodes":[
			{"id":"i1","identifier":"AB-1","title":"T","priority":2,"state":{"name":"Todo"},
			 "labels":{"nodes":[{"name":"Backend"},{"name":" Urgent "}]},
			 "inverseRelations":{"nodes":[
			   {"type":"blocks","relatedIssue":{"id":"i9","identifier":"AB-9","state":{"name":"Done"}}},
			   {"type":"related","relatedIssue":{"id":"i8","identifier":"AB-8","state":{"name":"Todo"}}}
			 ]}}
		],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
	})
	defer srv.Close()

	issues, err := c.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(gotQuery, "IssueFilter") {
		t.Errorf("query missing IssueFilter typing")
	}
	filter, _ := gotVars["filter"].(map[string]any)
	if filter == nil {
		t.Fatalf("no filter var")
	}
	project, _ := filter["project"].(map[string]any)
	slugID, _ := project["slugId"].(map[string]any)
	if slugID["eq"] != "proj" {
		t.Errorf("filter should use slugId eq proj, got %#v", filter)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d", len(issues))
	}
	iss := issues[0]
	if len(iss.Labels) != 2 || iss.Labels[0] != "backend" || iss.Labels[1] != "urgent" {
		t.Errorf("labels not normalized: %v", iss.Labels)
	}
	if len(iss.BlockedBy) != 1 {
		t.Fatalf("blocked_by should only include 'blocks' relations: %v", iss.BlockedBy)
	}
	if *iss.BlockedBy[0].Identifier != "AB-9" || *iss.BlockedBy[0].State != "Done" {
		t.Errorf("blocker wrong: %+v", iss.BlockedBy[0])
	}
	if iss.Priority == nil || *iss.Priority != 2 {
		t.Errorf("priority = %v", iss.Priority)
	}
}

func TestPagination_PreservesOrder(t *testing.T) {
	page := 0
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			w.Write([]byte(`{"data":{"issues":{"nodes":[
				{"id":"i1","identifier":"AB-1","title":"a","state":{"name":"Todo"}},
				{"id":"i2","identifier":"AB-2","title":"b","state":{"name":"Todo"}}
			],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`))
			return
		}
		w.Write([]byte(`{"data":{"issues":{"nodes":[
			{"id":"i3","identifier":"AB-3","title":"c","state":{"name":"Todo"}}
		],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`))
	})
	defer srv.Close()

	issues, err := c.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got := []string{issues[0].ID, issues[1].ID, issues[2].ID}
	want := []string{"i1", "i2", "i3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestPagination_MissingEndCursor(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":null}}}}`))
	})
	defer srv.Close()
	_, err := c.FetchCandidateIssues(context.Background())
	assertCode(t, err, tracker.CodeLinearMissingEndCursor)
}

func TestFetchIssuesByStates_EmptyNoCall(t *testing.T) {
	called := false
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`))
	})
	defer srv.Close()
	issues, err := c.FetchIssuesByStates(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if called {
		t.Errorf("should not call API for empty states")
	}
	if len(issues) != 0 {
		t.Errorf("expected empty result")
	}
}

func TestFetchIssueStatesByIDs_UsesIDType(t *testing.T) {
	var gotQuery string
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		w.Write([]byte(`{"data":{"issues":{"nodes":[
			{"id":"i1","identifier":"AB-1","title":"t","state":{"name":"Done"},"labels":{"nodes":[]}}
		],"pageInfo":{"hasNextPage":false}}}}`))
	})
	defer srv.Close()
	issues, err := c.FetchIssueStatesByIDs(context.Background(), []string{"i1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(gotQuery, "[ID!]") {
		t.Errorf("query should type ids as [ID!]: %s", gotQuery)
	}
	if issues[0].State != "Done" {
		t.Errorf("state = %q", issues[0].State)
	}
}

func TestErrorMapping(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		})
		defer srv.Close()
		_, err := c.FetchCandidateIssues(context.Background())
		assertCode(t, err, tracker.CodeLinearAPIStatus)
	})
	t.Run("graphql errors", func(t *testing.T) {
		c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
		})
		defer srv.Close()
		_, err := c.FetchCandidateIssues(context.Background())
		assertCode(t, err, tracker.CodeLinearGraphQLErrors)
	})
	t.Run("malformed", func(t *testing.T) {
		c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`not json`))
		})
		defer srv.Close()
		_, err := c.FetchCandidateIssues(context.Background())
		assertCode(t, err, tracker.CodeLinearUnknownPayload)
	})
}

func TestNew_MissingConfig(t *testing.T) {
	if _, err := New(Options{ProjectSlug: "p"}); err == nil {
		t.Errorf("expected missing api key error")
	}
	if _, err := New(Options{APIKey: "k"}); err == nil {
		t.Errorf("expected missing project slug error")
	}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	te, ok := err.(*tracker.Error)
	if !ok {
		t.Fatalf("expected tracker.Error, got %T: %v", err, err)
	}
	if te.Code != code {
		t.Fatalf("code = %s, want %s", te.Code, code)
	}
}
