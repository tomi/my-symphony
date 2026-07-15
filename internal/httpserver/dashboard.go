package httpserver

import (
	"html/template"
	"net/http"
	"time"
)

// handleRoot serves the human-readable dashboard rendered from the snapshot
// (SPEC §13.7.1).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not_found", "no such resource")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	snap, err := s.provider.Snapshot(snapshotTimeout)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<h1>Symphony</h1><p>snapshot unavailable: " +
			template.HTMLEscapeString(err.Error()) + "</p>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTmpl.Execute(w, snap)
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"fmtTime": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"fmtTimePtr": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.UTC().Format(time.RFC3339)
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>Symphony Dashboard</title>
<style>
 body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; }
 h1 { font-size: 1.4rem; }
 table { border-collapse: collapse; width: 100%; margin-bottom: 2rem; }
 th, td { border: 1px solid #ddd; padding: 6px 10px; text-align: left; font-size: 0.9rem; }
 th { background: #f4f4f4; }
 .counts { display: flex; gap: 2rem; margin-bottom: 1.5rem; }
 .card { background: #f8f8f8; border: 1px solid #e0e0e0; border-radius: 8px; padding: 1rem 1.5rem; }
 .card .n { font-size: 1.6rem; font-weight: 600; }
 .muted { color: #777; }
 .feed { margin-bottom: 2rem; }
 .feed h3 { font-size: 1rem; margin: 1rem 0 0.5rem; }
 .feed ul { list-style: none; padding: 0; margin: 0; }
 .feed li { padding: 6px 10px; border-left: 3px solid #ddd; margin-bottom: 4px; background: #fafafa; font-size: 0.9rem; }
 .feed li .ts { color: #777; font-size: 0.8rem; margin-right: 0.5rem; }
 .feed .msg { white-space: pre-wrap; word-break: break-word; }
</style>
</head>
<body>
<h1>Symphony Dashboard</h1>
<p class="muted">generated at {{ fmtTime .GeneratedAt }}</p>

<div class="counts">
 <div class="card"><div class="n">{{ .Counts.Running }}</div><div>running</div></div>
 <div class="card"><div class="n">{{ .Counts.Retrying }}</div><div>retrying</div></div>
 <div class="card"><div class="n">{{ .ClaudeTotals.TotalTokens }}</div><div>total tokens</div></div>
 <div class="card"><div class="n">{{ printf "%.1f" .ClaudeTotals.SecondsRunning }}</div><div>seconds running</div></div>
</div>

<h2>Running</h2>
<table>
 <tr><th>Identifier</th><th>State</th><th>Session</th><th>Turns</th><th>Last event</th><th>Started</th><th>Tokens</th></tr>
 {{ range .Running }}
 <tr>
  <td>{{ .IssueIdentifier }}</td>
  <td>{{ .State }}</td>
  <td>{{ .SessionID }}</td>
  <td>{{ .TurnCount }}</td>
  <td>{{ .LastEvent }} <span class="muted">{{ fmtTimePtr .LastEventAt }}</span></td>
  <td>{{ fmtTime .StartedAt }}</td>
  <td>{{ .Tokens.TotalTokens }}</td>
 </tr>
 {{ else }}
 <tr><td colspan="7" class="muted">no running sessions</td></tr>
 {{ end }}
</table>

<h2>Recent output</h2>
<div class="feed">
 {{ $shown := false }}
 {{ range .Running }}
  {{ if .Activity }}
  {{ $shown = true }}
  <h3>{{ .IssueIdentifier }} <span class="muted">turn {{ .TurnCount }}</span></h3>
  <ul>
   {{ range .Activity }}
   <li><span class="ts">{{ fmtTime .Timestamp }}</span><span class="msg">{{ .Message }}</span></li>
   {{ end }}
  </ul>
  {{ end }}
 {{ end }}
 {{ if not $shown }}<p class="muted">no agent output yet</p>{{ end }}
</div>

<h2>Retry queue</h2>
<table>
 <tr><th>Identifier</th><th>Attempt</th><th>Due</th><th>Error</th></tr>
 {{ range .Retrying }}
 <tr>
  <td>{{ .IssueIdentifier }}</td>
  <td>{{ .Attempt }}</td>
  <td>{{ fmtTime .DueAt }}</td>
  <td>{{ if .Error }}{{ .Error }}{{ else }}<span class="muted">—</span>{{ end }}</td>
 </tr>
 {{ else }}
 <tr><td colspan="4" class="muted">empty</td></tr>
 {{ end }}
</table>
</body>
</html>`))
