package httpserver

import (
	"fmt"
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
	// fmtTimeNano yields a stable per-step key (nanosecond precision) used to
	// persist a row's expanded state across the page's auto-refresh.
	"fmtTimeNano": func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) },
	// fmtTokens renders a token count compactly (e.g. 342, 15.2k).
	"fmtTokens": fmtTokens,
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
 .feed details { border-left: 3px solid #cdd6e0; margin-bottom: 4px; background: #fafafa; font-size: 0.9rem; }
 .feed details[open] { border-left-color: #6b8cae; background: #f4f7fb; }
 .feed summary { padding: 6px 10px; cursor: pointer; list-style-position: inside; }
 .feed summary::-webkit-details-marker { color: #999; }
 .feed summary .msg { white-space: normal; }
 .feed .tok { color: #3a6ea5; font-size: 0.8rem; margin-left: 0.4rem; white-space: nowrap; }
 .feed .detail { margin: 0; padding: 6px 12px 10px 26px; white-space: pre-wrap; word-break: break-word;
   font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.82rem; color: #333; }
 .feed .usage { color: #777; font-size: 0.78rem; padding: 0 12px 8px 26px; }
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
  {{ $id := .IssueIdentifier }}
  <h3>{{ $id }} <span class="muted">turn {{ .TurnCount }}</span></h3>
  <ul>
   {{ range .Activity }}
   {{ if .Detail }}
   <li><details data-key="{{ $id }}|{{ fmtTimeNano .Timestamp }}">
    <summary><span class="ts">{{ fmtTime .Timestamp }}</span><span class="msg">{{ .Message }}</span>{{ if .OutputTokens }}<span class="tok">· {{ fmtTokens .OutputTokens }} tok</span>{{ end }}</summary>
    <pre class="detail">{{ .Detail }}</pre>
    {{ if or .InputTokens .OutputTokens }}<div class="usage">{{ fmtTokens .InputTokens }} in / {{ fmtTokens .OutputTokens }} out</div>{{ end }}
   </details></li>
   {{ else }}
   <li><span class="ts">{{ fmtTime .Timestamp }}</span><span class="msg">{{ .Message }}</span>{{ if .OutputTokens }}<span class="tok">· {{ fmtTokens .OutputTokens }} tok</span>{{ end }}</li>
   {{ end }}
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
<script>
// Persist which activity rows are expanded across the page's auto-refresh. The
// dashboard reloads fully every few seconds; localStorage survives the reload so
// an expanded step stays expanded. Keyed per step by issue identifier + timestamp.
(function () {
  var KEY = "symphony.openActivity";
  function load() {
    try { return new Set(JSON.parse(localStorage.getItem(KEY)) || []); }
    catch (e) { return new Set(); }
  }
  function save(set) {
    try { localStorage.setItem(KEY, JSON.stringify(Array.from(set))); } catch (e) {}
  }
  var open = load();
  document.querySelectorAll("details[data-key]").forEach(function (d) {
    var k = d.getAttribute("data-key");
    if (open.has(k)) { d.open = true; }
    d.addEventListener("toggle", function () {
      var cur = load();
      if (d.open) { cur.add(k); } else { cur.delete(k); }
      save(cur);
    });
  });
})();
</script>
</body>
</html>`))

// fmtTokens renders a token count compactly: exact below 1000, otherwise a
// one-decimal thousands form (e.g. 342, 1.5k, 15.2k).
func fmtTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
