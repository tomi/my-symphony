package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
)

type fakeProvider struct {
	snap        domain.Snapshot
	err         error
	refreshed   bool
	refreshResp bool
}

func (f *fakeProvider) Snapshot(time.Duration) (domain.Snapshot, error) { return f.snap, f.err }
func (f *fakeProvider) RequestRefresh() bool                            { f.refreshed = true; return f.refreshResp }

func newTestServer(p *fakeProvider) *Server {
	return New(p, logging.New())
}

func sampleSnap() domain.Snapshot {
	now := time.Now().UTC()
	return domain.Snapshot{
		GeneratedAt: now,
		Counts:      domain.SnapshotCounts{Running: 1, Retrying: 1},
		Running: []domain.RunningRow{{
			IssueID: "i1", IssueIdentifier: "AB-1", State: "In Progress",
			SessionID: "s1", TurnCount: 2, WorkspacePath: "/ws/AB-1",
			Tokens: domain.TokenCounts{InputTokens: 5, OutputTokens: 3, TotalTokens: 8},
			Activity: []domain.AgentActivity{{
				Timestamp: now, Event: "turn_completed", TurnID: "1",
				Message: "investigating the failing test",
			}, {
				Timestamp: now.Add(time.Second), Event: "notification", TurnID: "1",
				Message: "Read", Detail: "→ Read {\"file_path\":\"/etc/hosts\"}",
				InputTokens: 1500, OutputTokens: 342,
			}},
		}},
		Retrying: []domain.RetryRow{{
			IssueID: "i2", IssueIdentifier: "AB-2", Attempt: 1, DueAt: now,
		}},
		ClaudeTotals: domain.Totals{TotalTokens: 8, SecondsRunning: 12.5},
	}
}

func TestStateEndpoint(t *testing.T) {
	p := &fakeProvider{snap: sampleSnap()}
	srv := newTestServer(p)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.handleAPI(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	counts := body["counts"].(map[string]any)
	if counts["running"].(float64) != 1 {
		t.Errorf("counts wrong: %v", counts)
	}
}

func TestIssueDetail_FoundAndNotFound(t *testing.T) {
	p := &fakeProvider{snap: sampleSnap()}
	srv := newTestServer(p)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/AB-1", nil)
	w := httptest.NewRecorder()
	srv.handleAPI(w, req)
	if w.Code != 200 {
		t.Fatalf("found status = %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "running" {
		t.Errorf("status = %v", body["status"])
	}
	running := body["running"].(map[string]any)
	activity := running["activity"].([]any)
	if len(activity) != 2 {
		t.Fatalf("detail activity len = %d, want 2", len(activity))
	}
	if msg := activity[0].(map[string]any)["message"]; msg != "investigating the failing test" {
		t.Errorf("detail activity message = %v", msg)
	}
	step := activity[1].(map[string]any)
	if step["detail"] == "" || step["detail"] == nil {
		t.Errorf("detail activity should carry tool detail, got %v", step["detail"])
	}
	if step["input_tokens"].(float64) != 1500 || step["output_tokens"].(float64) != 342 {
		t.Errorf("detail activity tokens = in:%v out:%v", step["input_tokens"], step["output_tokens"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/UNKNOWN", nil)
	w = httptest.NewRecorder()
	srv.handleAPI(w, req)
	if w.Code != 404 {
		t.Fatalf("unknown status = %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "issue_not_found" {
		t.Errorf("error code = %v", errObj["code"])
	}
}

func TestRefresh(t *testing.T) {
	p := &fakeProvider{snap: sampleSnap(), refreshResp: true}
	srv := newTestServer(p)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil)
	w := httptest.NewRecorder()
	srv.handleAPI(w, req)
	if w.Code != 202 {
		t.Fatalf("status = %d", w.Code)
	}
	if !p.refreshed {
		t.Errorf("refresh not requested")
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["queued"] != true {
		t.Errorf("queued = %v", body["queued"])
	}
	ops := body["operations"].([]any)
	if len(ops) != 2 {
		t.Errorf("operations = %v", ops)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	p := &fakeProvider{snap: sampleSnap()}
	srv := newTestServer(p)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.handleAPI(w, req)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestDashboardRenders(t *testing.T) {
	p := &fakeProvider{snap: sampleSnap()}
	srv := newTestServer(p)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.handleRoot(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
	if !contains(w.Body.String(), "AB-1") {
		t.Errorf("dashboard should list running issue")
	}
	if !contains(w.Body.String(), "investigating the failing test") {
		t.Errorf("dashboard should render agent output in the recent-output feed")
	}
	if !contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Errorf("dashboard should auto-refresh")
	}
	body := w.Body.String()
	// Steps with detail render as foldable <details> rows keyed for persistence.
	if !contains(body, "<details data-key=") {
		t.Errorf("dashboard should render foldable detail rows")
	}
	if !contains(body, "/etc/hosts") {
		t.Errorf("dashboard should render tool detail inside the fold")
	}
	// Output-token badge on the collapsed summary line (342 -> "342 tok").
	if !contains(body, "342 tok") {
		t.Errorf("dashboard should show per-step output tokens")
	}
	// Input tokens shown in the expanded detail (1500 -> "1.5k in").
	if !contains(body, "1.5k in") {
		t.Errorf("dashboard should show per-step input tokens in detail")
	}
	// Minimal JS persists fold state across the auto-refresh.
	if !contains(body, "localStorage") {
		t.Errorf("dashboard should persist fold state via localStorage")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
