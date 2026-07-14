// Package httpserver is the OPTIONAL HTTP observability/control extension
// (SPEC §13.7). It is a pure observer of orchestrator snapshots and must not be
// required for orchestrator correctness.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/logging"
)

// StateProvider is the orchestrator surface the server consumes (SPEC §2.4).
type StateProvider interface {
	Snapshot(timeout time.Duration) (domain.Snapshot, error)
	RequestRefresh() bool
}

// Server hosts the dashboard and JSON API.
type Server struct {
	provider StateProvider
	logger   *logging.Logger
	httpSrv  *http.Server
	addr     string
}

const snapshotTimeout = 2 * time.Second

// New builds a Server bound to loopback on the given port. Port 0 requests an
// ephemeral port (SPEC §13.7).
func New(provider StateProvider, logger *logging.Logger) *Server {
	return &Server{provider: provider, logger: logger}
}

// Start binds the listener and serves in the background (SPEC §13.7).
func (s *Server) Start(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/api/v1/", s.handleAPI)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("http listen: %w", err)
	}
	s.addr = ln.Addr().String()
	s.httpSrv = &http.Server{Handler: mux}

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Warn("http server stopped", "error", err.Error())
		}
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
	}()

	s.logger.Info("http server started", "addr", s.addr)
	return nil
}

// Addr returns the bound address (useful when port 0 was requested).
func (s *Server) Addr() string { return s.addr }

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case rest == "state":
		s.handleState(w, r)
	case rest == "refresh":
		s.handleRefresh(w, r)
	default:
		s.handleIssueDetail(w, r, rest)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	snap, err := s.provider.Snapshot(snapshotTimeout)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	queued := s.provider.RequestRefresh()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":       queued,
		"coalesced":    !queued,
		"requested_at": time.Now().UTC().Format(time.RFC3339),
		"operations":   []string{"poll", "reconcile"},
	})
}

func (s *Server) handleIssueDetail(w http.ResponseWriter, r *http.Request, identifier string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if identifier == "" {
		writeError(w, http.StatusNotFound, "issue_not_found", "no issue identifier")
		return
	}
	snap, err := s.provider.Snapshot(snapshotTimeout)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	detail, ok := buildIssueDetail(snap, identifier)
	if !ok {
		writeError(w, http.StatusNotFound, "issue_not_found",
			fmt.Sprintf("issue %q is not tracked in current state", identifier))
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
