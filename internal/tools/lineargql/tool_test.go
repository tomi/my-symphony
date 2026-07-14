package lineargql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCountOperations(t *testing.T) {
	cases := []struct {
		doc  string
		want int
	}{
		{`query { viewer { id } }`, 1},
		{`{ viewer { id } }`, 1},
		{`mutation Foo($x: ID!) { update(id: $x) { ok } }`, 1},
		{`query A { a } query B { b }`, 2},
		{`query A { a }` + "\n" + `fragment F on T { x }`, 1},
		{`# comment with query keyword` + "\n" + `query { a }`, 1},
		{``, 0},
	}
	for _, tc := range cases {
		if got := countOperations(tc.doc); got != tc.want {
			t.Errorf("countOperations(%q) = %d, want %d", tc.doc, got, tc.want)
		}
	}
}

func TestExecute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "tok" {
			t.Errorf("missing auth")
		}
		w.Write([]byte(`{"data":{"viewer":{"id":"u1"}}}`))
	}))
	defer srv.Close()
	tool := New(srv.URL, "tok", srv.Client())
	res := tool.Execute(context.Background(), Input{Query: "query { viewer { id } }"})
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	var data map[string]any
	_ = json.Unmarshal(res.Data, &data)
	if data["viewer"] == nil {
		t.Errorf("data not preserved: %s", res.Data)
	}
}

func TestExecute_GraphQLErrorsPreserveBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":null,"errors":[{"message":"nope"}]}`))
	}))
	defer srv.Close()
	tool := New(srv.URL, "tok", srv.Client())
	res := tool.Execute(context.Background(), Input{Query: "query { x }"})
	if res.Success {
		t.Errorf("expected success=false for graphql errors")
	}
	if len(res.Errors) == 0 {
		t.Errorf("graphql errors body should be preserved")
	}
}

func TestExecute_InvalidInput(t *testing.T) {
	tool := New("", "tok", nil)
	if res := tool.Execute(context.Background(), Input{Query: ""}); res.Success || res.Error.Code != "invalid_input" {
		t.Errorf("empty query should be invalid_input: %+v", res)
	}
	if res := tool.Execute(context.Background(), Input{Query: "query { a } query { b }"}); res.Success || res.Error.Code != "invalid_input" {
		t.Errorf("multi-op should be invalid_input: %+v", res)
	}
}

func TestExecute_MissingAuth(t *testing.T) {
	tool := New("", "", nil)
	res := tool.Execute(context.Background(), Input{Query: "query { a }"})
	if res.Success || res.Error.Code != "missing_auth" {
		t.Errorf("expected missing_auth, got %+v", res)
	}
}

func TestExecute_TransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	tool := New(srv.URL, "tok", srv.Client())
	res := tool.Execute(context.Background(), Input{Query: "query { a }"})
	if res.Success || res.Error.Code != "transport_error" {
		t.Errorf("expected transport_error, got %+v", res)
	}
}

func TestParseInput_Shorthand(t *testing.T) {
	in, err := ParseInput(json.RawMessage(`"query { a }"`))
	if err != nil || in.Query != "query { a }" {
		t.Errorf("shorthand parse failed: %v %+v", err, in)
	}
	in, err = ParseInput(json.RawMessage(`{"query":"query { a }","variables":{"x":1}}`))
	if err != nil || in.Query != "query { a }" || in.Variables["x"] == nil {
		t.Errorf("object parse failed: %v %+v", err, in)
	}
}
