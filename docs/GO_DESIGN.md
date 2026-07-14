# Symphony — Go Implementation Design

Status: Design v1
Target spec: `SPEC.md` (Symphony Service Specification, Draft v1)
Scope: All REQUIRED core conformance (SPEC §18.1) plus three OPTIONAL extensions —
HTTP server + dashboard (§13.7), `linear_graphql` agent tool (§10.5), and terminal status
surface (§13.4). **The SSH Worker Extension (Appendix A) is explicitly out of scope.**

This document describes *how* to build a Go implementation of Symphony. It is a design, not code:
it maps each spec requirement onto concrete Go packages, types, concurrency structures, and
third-party libraries, and it cites the governing spec section for every design decision so the
mapping can be audited.

Section references of the form "§N" point at `SPEC.md`.

---

## 1. Architecture Overview & Package Layout

Symphony is a single long-running daemon. It polls Linear, dispatches per-issue workers with
bounded concurrency, runs the Claude Code CLI once per turn inside an isolated workspace, and
exposes observability. The spec's abstraction layers (§3.2) map cleanly to Go packages.

Proposed module: `github.com/tomi/my-symphony`.

```
my-symphony/
├── go.mod                          module github.com/tomi/my-symphony
├── SPEC.md                         (existing) language-agnostic specification
├── WORKFLOW.md                     (operator-provided at runtime; repo-owned policy §5)
├── cmd/
│   └── symphony/
│       └── main.go                 CLI entrypoint & host lifecycle (§17.7)
└── internal/
    ├── workflow/                   WORKFLOW.md loader: front-matter/body split (§5.1–5.2)
    ├── config/                     Typed config view: defaults, $VAR, path norm, validation (§6)
    ├── domain/                     Issue, Workspace, RunAttempt, LiveSession, RetryEntry, State (§4)
    ├── prompt/                     Strict Liquid rendering of the prompt template (§5.4, §12)
    ├── tracker/                    Client interface (§11.1)
    │   └── linear/                 Linear GraphQL adapter (§11.2–11.4)
    ├── workspace/                  Sanitize, create/reuse, hooks, safety invariants, cleanup (§9)
    ├── agent/                      Agent Runner (§10.7)
    │   └── claude/                 stream-json subprocess client (§10.1–10.6)
    ├── orchestrator/               Single-authority event loop: dispatch/reconcile/retry (§7,§8,§16)
    ├── watcher/                    fsnotify WORKFLOW.md watch → reload/re-apply (§6.2)
    ├── logging/                    Structured key=value logs w/ required context fields (§13.1–13.2)
    ├── status/          [ext]      Terminal status surface (§13.4)
    ├── httpserver/      [ext]      Dashboard + /api/v1/* (§13.7)
    └── tools/
        └── lineargql/  [ext]      linear_graphql client-side tool (§10.5)
```

Layer mapping (§3.2):

| Spec layer (§3.2)     | Go packages                                             |
|-----------------------|--------------------------------------------------------|
| Policy (repo-defined) | `WORKFLOW.md` (not code)                                |
| Configuration         | `workflow`, `config`                                   |
| Coordination          | `orchestrator`, `watcher`                              |
| Execution             | `workspace`, `agent`, `agent/claude`, `prompt`         |
| Integration           | `tracker`, `tracker/linear`                            |
| Observability         | `logging`, `status` [ext], `httpserver` [ext]          |

Dependency direction is strictly inward: `orchestrator` depends on the `tracker.Client` and
`agent.Runner` *interfaces*, never on their concrete adapters. `cmd/symphony` is the only place
that wires concrete implementations together (composition root). This keeps the coordination layer
testable with fakes and keeps the Linear/Claude specifics swappable, as the spec intends
(§3.2 "easiest to port when kept in these layers", §11.2 "keep query construction isolated").

---

## 2. Concurrency Model — the central design decision

§7 is unambiguous: "The orchestrator is the only component that mutates scheduling state" and "The
orchestrator serializes state mutations through one authority to avoid duplicate dispatch" (§7.4).
The reference algorithms in §16 are written in an actor style — workers `send(orchestrator_channel,
{...})` and the orchestrator reacts.

**Chosen realization: a single orchestrator goroutine owning `RuntimeState`, driven by one events
channel.** Everything that changes scheduling state is expressed as an event delivered on that
channel; the orchestrator's `select` loop is the sole writer. This is the idiomatic Go translation
of §16 ("Do not communicate by sharing memory; share memory by communicating"), and it structurally
guarantees the §7.4 idempotency invariant: because only one goroutine ever reads-modifies-writes
`running`/`claimed`/`retry_attempts`, duplicate dispatch is impossible without any mutex.

```
                      ┌────────────────────────────────────────────┐
   fsnotify ───────▶  │                                            │
   (watcher)          │            Orchestrator goroutine          │
                      │              (sole state writer)           │
   tick timer ─────▶  │   for {                                    │
                      │     select {                               │
   worker goroutines  │       case <-tick:        onTick()         │
   (AgentUpdate, ────▶│       case e := <-events: handle(e)        │
    WorkerExit)       │       case <-ctx.Done():  drain & return   │
                      │     }                                      │
   retry timers ────▶ │   }                                        │
   (AfterFunc)        │                                            │
                      │   owns: RuntimeState (§4.1.8)              │
   HTTP / status ───▶ │                                            │
   (SnapshotRequest)  └────────────────────────────────────────────┘
```

### 2.1 Event types

```go
package orchestrator

type Event interface{ isEvent() }

type TickEvent        struct{}                         // poll cadence fired (§8.1)
type AgentUpdate      struct{ IssueID string; Msg agent.Event }   // §16.4, §10.4
type WorkerExit       struct{ IssueID string; Reason ExitReason } // normal|abnormal (§16.6)
type RetryTimerFired  struct{ IssueID string }         // §16.7
type ReloadConfig     struct{ Cfg *config.Config; Tmpl string }   // §6.2
type SnapshotRequest  struct{ Reply chan<- domain.Snapshot }      // §13.3
type RefreshRequest   struct{}                         // POST /api/v1/refresh (§13.7.2)
```

Each event handler runs to completion on the loop goroutine, so state transitions in §7.1–§7.3
happen without interleaving.

### 2.2 Workers

Each dispatched issue runs `run_agent_attempt` (§16.5) in its own goroutine. A worker never touches
`RuntimeState`; it communicates outcomes exclusively by posting events:

- streaming agent events → `AgentUpdate` (updates live session fields, tokens, rate limits, §7.3)
- turn/worker completion → `WorkerExit{Reason: Normal|Abnormal}` (§16.6)

The `dispatch_issue` step (§16.4) records the worker's cancel handle in the `running` entry so
reconciliation/stall handling can terminate it.

### 2.3 Retry timers

`schedule_retry` (§8.4) uses `time.AfterFunc(delay, func(){ post(RetryTimerFired{id}) })`. The
returned `*time.Timer` is the `timer_handle` stored in `RetryEntry` (§4.1.7); scheduling a new retry
for an issue first calls `Stop()` on the existing timer ("Cancel any existing retry timer for the
same issue", §8.4).

### 2.4 Snapshots (feeds both HTTP API and terminal status)

External observers never read `RuntimeState` directly. They post a `SnapshotRequest` with a reply
channel; the loop builds an immutable `domain.Snapshot` copy and sends it back. The caller does:

```go
reply := make(chan domain.Snapshot, 1)
select {
case orch.Events() <- SnapshotRequest{Reply: reply}:
    select {
    case snap := <-reply:      // ok
    case <-time.After(d):      // §13.3 "timeout"
    }
default:                       // loop not accepting → §13.3 "unavailable"
}
```

This gives the §13.3 `timeout`/`unavailable` error modes for free and keeps the single-writer
invariant intact (no locking of live state from other goroutines).

### 2.5 Effective config after reload

The `watcher` goroutine only *reads and validates* `WORKFLOW.md`; it never mutates live state. On a
successful reload it posts `ReloadConfig`, and the loop swaps the effective config between events
(§6.2: "re-apply … without restart", applies to "future dispatch, retry scheduling, reconciliation
decisions, hook execution, and agent launches"). In-flight sessions are not restarted (§6.2:
"not REQUIRED to restart in-flight agent sessions").

---

## 3. Workflow Loader & Config Layer (§5, §6)

### 3.1 Loader — `internal/workflow`

```go
type Definition struct {
    Config         map[string]any // front-matter root object, NOT nested under "config" (§5.2)
    PromptTemplate string         // trimmed Markdown body (§5.2)
}

func Load(path string) (*Definition, error)
```

Algorithm (§5.2):

1. Read file; on failure return `Error{Code: "missing_workflow_file"}` (§5.5).
2. If the content begins with a `---` line, scan to the next `---`; decode that block as YAML with
   `gopkg.in/yaml.v3` into `map[string]any`. The remainder is the prompt body.
3. If there is no front matter, `Config` is an empty map and the whole file is the body (§5.2).
4. YAML that does not decode to a map → `Error{Code: "workflow_front_matter_not_a_map"}`.
   YAML syntax errors → `Error{Code: "workflow_parse_error"}`.
5. Trim the body before returning (§5.2).

Path precedence (§5.1): explicit CLI argument first, else `./WORKFLOW.md` in the process CWD —
resolved in `cmd/symphony` and passed to `Load`.

### 3.2 Typed config — `internal/config`

`config.Config` provides typed getters over the raw map with every default from the §6.4 cheat
sheet baked in. Construction resolves the pipeline in §6.1 order:

1. select workflow path (done by caller)
2. parse front matter (done by `workflow.Load`)
3. apply built-in defaults for missing optional fields
4. resolve `$VAR_NAME` **only** where a value literally contains `$NAME` (§6.1: env vars do not
   globally override YAML)
5. coerce and validate typed values

```go
type Config struct {
    Tracker  TrackerConfig
    Polling  PollingConfig
    Workspace WorkspaceConfig
    Hooks    HooksConfig
    Agent    AgentConfig
    Claude   ClaudeConfig
    Server   ServerConfig   // [ext] §13.7
    // workflowDir retained for relative workspace.root resolution (§6.1)
}
```

Field-by-field defaults follow the §6.4 table exactly (e.g. `Polling.IntervalMs` default 30000;
`Agent.MaxConcurrent` default 10; `Agent.MaxTurns` default 20; `Claude.Command` default
`claude -p --output-format stream-json --verbose`; `Claude.TurnTimeoutMs` 3600000; etc.).

Value coercion (§6.1):

- **Path/command expansion** applies `~` and `$VAR` **only to filesystem-path values** (e.g.
  `workspace.root`), never to URIs or arbitrary shell command strings (`claude.command`,
  `tracker.endpoint` are left verbatim). Helper `expandPath(string) string`.
- **Relative `workspace.root`** resolves against the directory containing the selected `WORKFLOW.md`,
  then normalizes to an absolute path (§6.1, §5.3.3). Default `<os.TempDir()>/symphony_workspaces`.
- **`tracker.api_key`**: `$VAR` resolves via `os.Getenv`; empty result is treated as *missing*
  (§5.3.1). Canonical env is `LINEAR_API_KEY`.
- **`max_concurrent_agents_by_state`**: keys lowercased for lookup; non-positive/non-numeric entries
  ignored (§5.3.5).

### 3.3 Error taxonomy

A single typed error carries the spec's error codes so callers can branch and operators can read
them (§5.5, §11.4):

```go
type Error struct { Code string; Msg string; Wrapped error }
func (e *Error) Error() string { ... }   // "code: msg"
func (e *Error) Unwrap() error { return e.Wrapped }
```

Codes: `missing_workflow_file`, `workflow_parse_error`, `workflow_front_matter_not_a_map`,
`template_parse_error`, `template_render_error` (§5.5); plus tracker codes in §5 below.

### 3.4 Dispatch preflight validation (§6.3)

```go
func (c *Config) ValidateDispatch() error
```

Checks (§6.3):

- workflow file loads & parses (already true if we have a `Config`, re-checked defensively)
- `tracker.kind` present and supported (`linear`)
- `tracker.api_key` present after `$` resolution
- `tracker.project_slug` present when `tracker.kind == linear`
- `claude.command` present and non-empty

Called at **startup** (failure → fail startup with operator-visible error, §6.3/§16.1) and **per
tick** before dispatch (failure → skip dispatch, keep reconciliation, emit operator-visible error,
§8.1). Reconciliation always runs first regardless (§8.1).

### 3.5 Dynamic reload — `internal/watcher` (§6.2)

`fsnotify.Watcher` watches the `WORKFLOW.md` file **and its parent directory** (editors frequently
replace-on-save via rename, which fires on the directory, not the file). On any relevant event:

1. `workflow.Load` + `config.New` + `ValidateDispatch`.
2. On success → post `ReloadConfig{Cfg, Tmpl}`; the loop swaps effective config atomically
   between events and re-applies poll cadence, concurrency, active/terminal states, claude settings,
   workspace paths/hooks, and future-run prompt content (§6.2).
3. On failure → keep last-known-good effective config and emit an operator-visible error; **never
   crash** (§6.2: "Invalid reloads MUST NOT crash the service").

Defensive reload: the orchestrator also re-validates before dispatch (§3.4) so a missed filesystem
event still gets corrected on the next tick (§6.2: "re-validate/reload defensively … in case
filesystem watch events are missed").

Note: the HTTP listener port is *not* hot-rebound; a `server.port` change is restart-required, which
§13.7 explicitly allows.

---

## 4. Domain Model — `internal/domain` (§4)

Plain data structs, no behavior beyond normalization helpers. All fields mirror §4.1.

```go
type BlockerRef struct { ID, Identifier, State *string }        // §4.1.1

type Issue struct {                                             // §4.1.1
    ID          string
    Identifier  string
    Title       string
    Description *string
    Priority    *int          // lower = higher priority (§4.1.1)
    State       string
    BranchName  *string
    URL         *string
    Labels      []string      // normalized lowercase (§4.1.1, §11.3)
    BlockedBy   []BlockerRef
    CreatedAt   *time.Time
    UpdatedAt   *time.Time
    Assignee    *string       // for routing (§8.2)
}

type Workspace struct { Path, Key string; CreatedNow bool }     // §4.1.4

type LiveSession struct {                                       // §4.1.6
    SessionID, ThreadID, TurnID string
    AgentPID *string
    LastAgentEvent *string
    LastAgentTimestamp *time.Time
    LastAgentMessage string
    ClaudeInputTokens, ClaudeOutputTokens, ClaudeTotalTokens int
    LastReportedInputTokens, LastReportedOutputTokens, LastReportedTotalTokens int
    TurnCount int
}

type RetryEntry struct {                                        // §4.1.7
    IssueID, Identifier string
    Attempt int              // 1-based for retry queue
    DueAtMs int64            // monotonic
    Timer   *time.Timer      // timer_handle
    Error   *string
}

type RuntimeState struct {                                      // §4.1.8
    PollIntervalMs     int
    MaxConcurrentAgents int
    Running       map[string]*RunningEntry   // issue_id -> entry
    Claimed       map[string]struct{}        // reserved/running/retrying
    RetryAttempts map[string]*RetryEntry
    Completed     map[string]struct{}        // bookkeeping only, not dispatch gating
    ClaudeTotals  Totals                      // tokens + seconds_running
    ClaudeRateLimits any                      // latest snapshot
}
```

Normalization helpers (§4.2):

```go
func WorkspaceKey(identifier string) string  // replace [^A-Za-z0-9._-] with "_"
func NormalizeState(state string) string      // strings.ToLower for comparisons
```

`RunningEntry` embeds the worker's cancel func, the current `Issue` snapshot, `StartedAt`, and a
`LiveSession` (mutated only by the loop from `AgentUpdate`).

`Snapshot` is a read-only projection built inside the loop for observers (§2.4), shaped to the
§13.3 / §13.7 JSON (running rows with `turn_count`, retry rows, `claude_totals` incl. live
`seconds_running`, `rate_limits`).

---

## 5. Tracker Integration (§11)

### 5.1 Interface — `internal/tracker`

The orchestrator depends only on this interface (§11.1):

```go
type Client interface {
    FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error)      // §11.1(1)
    FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) // §11.1(2)
    FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error)  // §11.1(3)
}
```

### 5.2 Linear adapter — `internal/tracker/linear` (§11.2)

Plain `net/http` POST to the GraphQL endpoint (default `https://api.linear.app/graphql`) — no heavy
GraphQL client library is warranted, keeping the dependency surface minimal and the exact query
fields under our control (§11.2 "Keep query construction isolated and test the exact query
fields/types").

- Auth token in the `Authorization` header (§11.2). Never logged (§15.3).
- Per-request network timeout 30000 ms via `context.WithTimeout` (§11.2).
- Candidate query filters project with `project: { slugId: { eq: $projectSlug } }` and includes
  labels + inverse `blocks` relations (§11.2). `project_slug` maps to Linear `slugId`.
- **Pagination** required for candidates: cursor-based, page size default 50; on a page that claims
  `hasNextPage` but omits `endCursor`, fail with `linear_missing_end_cursor` (§11.2, §11.4).
  Pagination preserves order across pages (§17.3).
- `FetchIssuesByStates(nil/empty)` short-circuits to `([], nil)` with **no** API call (§17.3).
- State-refresh query takes GraphQL issue IDs typed as `[ID!]` and returns minimal normalized issues
  including labels (§11.2: refresh can observe label removal to stop/release work).

### 5.3 Normalization (§11.3)

- `labels` → each name trimmed then lowercased.
- `blocked_by` → derived from inverse relations whose type is `blocks`.
- `priority` → integer only; non-integers become `nil`.
- `created_at`/`updated_at` → parse ISO-8601 to `time.Time` (`nil` on absence/parse failure).

Required-label / assignee routing (§8.2) is applied by the orchestrator **after** normalization, so
that label removal observed on refresh can stop or release existing work (§11.2). A blank configured
label matches no issue; matching ignores case and surrounding whitespace (§5.3.1).

### 5.4 Error mapping (§11.4)

Transport failure → `linear_api_request`; non-200 → `linear_api_status`; top-level GraphQL `errors`
→ `linear_graphql_errors`; unrecognized body → `linear_unknown_payload`; pagination integrity →
`linear_missing_end_cursor`; plus config-level `unsupported_tracker_kind`, `missing_tracker_api_key`,
`missing_tracker_project_slug`. Orchestrator reaction (§11.4): candidate fetch failure → log & skip
dispatch this tick; running-state refresh failure → log & keep workers; startup terminal cleanup
failure → warn & continue.

### 5.5 Tracker writes

None. Symphony is a reader/scheduler; ticket mutations are the agent's job via its own tools
(§11.5). Success frequently means "reached a handoff state" (e.g. `Human Review`), not tracker
`Done` (§1, §11.5).

---

## 6. Workspace Manager & Safety — `internal/workspace` (§9)

```go
type Manager struct { /* holds effective workspace root + hooks + hook timeout */ }

func (m *Manager) CreateForIssue(ctx, identifier string) (domain.Workspace, error) // §9.2
func (m *Manager) RunHook(ctx, name, path string) error                             // §9.4
func (m *Manager) Cleanup(ctx, identifier string) error                             // §9.1/§9.4
```

### 6.1 Creation & reuse (§9.2)

1. `key := domain.WorkspaceKey(identifier)` (§9.5 invariant 3).
2. `path := filepath.Join(root, key)`.
3. **Enforce safety invariant 2 before any FS write** (§9.5): normalize `path` and `root` to
   absolute; require `root` to be a proper path prefix directory of `path`; reject escapes. Because
   the key is already sanitized, `..` cannot appear, but the containment check is enforced anyway as
   defense in depth.
4. Ensure the directory exists. `CreatedNow` is true only if this call created it (stat-first, or
   attempt `os.Mkdir` and treat `ErrExist` as reuse) — this gates `after_create` (§9.2 step 4).
   An existing non-directory at the path is handled per policy (fail with a clear error, §17.2).
5. If `CreatedNow`, run the `after_create` hook; failure/timeout is **fatal** to creation and the
   partially-created dir may be removed (§9.3, §9.4).

Workspaces are reused across runs and **not** auto-deleted after success (§9.1). Any
population/sync beyond `MkdirAll` is left to hooks (§9.2/§9.3); reused workspaces are not
destructively reset on population failure (§9.3).

### 6.2 Hooks (§9.4)

Executed via `bash -lc <script>` (the spec's conforming POSIX default, §9.4) with `cwd` = workspace
directory and a `context` deadline of `hooks.timeout_ms` (default 60000). Start, failures, and
timeouts are logged; hook stdout/stderr is truncated in logs (§15.4). Failure semantics:

| Hook            | On failure/timeout                          | Spec |
|-----------------|---------------------------------------------|------|
| `after_create`  | fatal to workspace creation                 | §9.4 |
| `before_run`    | fatal to the current run attempt            | §9.4 |
| `after_run`     | logged and ignored                          | §9.4 |
| `before_remove` | logged and ignored (cleanup still proceeds) | §9.4 |

### 6.3 Safety invariants (§9.5, §15.2) — enforced before every agent launch

1. Coding-agent `cwd` **==** the per-issue workspace path (checked in the agent runner right before
   spawn, §9.5 invariant 1).
2. `workspace_path` stays under `workspace_root` (absolute + prefix, §9.5 invariant 2).
3. Workspace directory names use only `[A-Za-z0-9._-]` (§9.5 invariant 3).

`Cleanup` runs `before_remove` (best-effort) then `os.RemoveAll`, used by startup terminal sweep and
terminal-transition reconciliation (§8.6, §8.5).

---

## 7. Agent Runner & Claude stream-json Client (§10)

### 7.1 Runner — `internal/agent` (§10.7, §16.5)

```go
type Runner interface {
    Run(ctx context.Context, issue domain.Issue, attempt *int, emit func(Event)) error
}
```

`Run` implements §16.5 exactly:

1. `workspace.CreateForIssue(issue.Identifier)`; on failure fail the attempt.
2. `before_run` hook; on failure fail the attempt.
3. Start the Claude session (workspace as cwd); on failure run `after_run` best-effort and fail.
4. Turn loop, `turn_number := 1`, up to `agent.max_turns` (§5.3.5):
   - build the turn prompt (§8 below); on failure stop session, `after_run`, fail.
   - run the turn, forwarding streamed events via `emit` → orchestrator `AgentUpdate` (§16.4).
   - on turn failure: stop session, `after_run`, fail (orchestrator will retry, §16.6).
   - **re-fetch tracker state** for this issue (§7.1, §16.5); on failure stop/after_run/fail.
   - if the issue is no longer active → break; if `turn_number >= max_turns` → break;
     else `turn_number++` and continue on the **same** session (§7.1).
5. Stop session, `after_run` best-effort, exit normally (§16.5).

First turn uses the full rendered task prompt; continuation turns send only continuation guidance,
not the original prompt already in thread history (§7.1, §10.2).

### 7.2 Claude client — `internal/agent/claude` (§10.1–§10.6)

**Execution model: one subprocess per turn** (§10, "There is no persistent server process";
continuity is `--resume`, not a long-lived process).

Launch (§10.1):

```go
cmd := exec.CommandContext(ctx, "bash", "-lc", claudeCommand)
cmd.Dir = workspacePath                 // §10.1 working dir = workspace
stdin, _ := cmd.StdinPipe()             // rendered prompt then close (§10.1)
stdout, _ := cmd.StdoutPipe()
stderr, _ := cmd.StderrPipe()
```

- On continuation turns, when `claude.resume_across_turns` (default true) and a `session_id` is
  known, append `--resume <session_id>` to the command (§5.3.6, §10.2).
- Stdout parsed with `bufio.Scanner` whose buffer max is **10 MB** to tolerate large `stream-json`
  lines (§10.1). One JSON object per line; dispatch by `type`: `system`(subtype `init`),
  `assistant`, `user`, `result` (§10.3).
- **Session id**: captured from the `system`/`init` event (fallback: terminal `result`). Session is
  `pending` until it arrives (§10.2). The same id is emitted as both `session_id` and `thread_id`
  and reused for all continuation turns of the worker; `turn_id` is a per-turn counter (§4.2, §10.2).
- **Malformed lines**: a run of `MALFORMED_LINE_LIMIT` consecutive unparseable lines is treated as
  stream corruption and fails the turn (§10.3).
- **stderr** is drained into a bounded ring buffer used only for failure diagnostics, kept separate
  from the stdout `stream-json` stream (§10.3).
- **Timeouts** (§10.6): `read_timeout_ms` for startup/sync reads; `turn_timeout_ms` bounds the whole
  turn stream (context cancellation kills the subprocess); stall is enforced by the orchestrator via
  event inactivity (§8.5), not here.

Completion mapping (§10.3): terminal `result` with `is_error==false` → success; `is_error==true` →
failure; stream ends with no `result` → failure; turn timeout → failure; subprocess exit before
`result` → failure.

### 7.3 Emitted runtime events (§10.4)

A normalized event enum is forwarded upstream: `session_started`, `startup_failed`, `turn_completed`,
`turn_failed`, `turn_cancelled`, `turn_ended_with_error`, `turn_input_required`,
`approval_auto_approved`, `unsupported_tool_call`, `notification`, `other_message`, `malformed`.
Each carries `event`, UTC `timestamp`, `agent_pid` when available, optional `usage`, and payload
fields (§10.4).

### 7.4 Token accounting (§13.5)

Per-turn usage is read from the terminal `result` event's `usage` map only; mid-stream `assistant`
deltas are ignored (§13.5). Extraction is lenient across `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`. Turn usage is **accumulated across the
worker run** (never treated as an absolute thread total) and aggregated into `RuntimeState`
totals by the loop when it handles the `AgentUpdate`/`WorkerExit`.

### 7.5 Approval / sandbox posture (§10.5)

Symphony documents a **high-trust default** matching the §10.5 example, expressed purely as Claude
Code CLI flags inside `claude.command` (there is no separate approval/sandbox config surface,
§5.3.6): command-execution and file-change approvals auto-approved for the session (e.g. via
`--permission-mode` / `--dangerously-skip-permissions`), user-input-required turns treated as **hard
failure** (§10.5 example, §10.5 "A run MUST NOT stall indefinitely"). Unsupported dynamic tool calls
return a tool failure result and the session continues (§10.5). Operators harden by editing
`claude.command` (§5.3.6, §15.5). This posture is restated in the Security section (§14).

### 7.6 Error mapping (§10.6)

Normalized categories: `claude_not_found`, `invalid_workspace_cwd`, `response_timeout`,
`turn_timeout`, `port_exit`, `response_error`, `turn_failed`, `turn_cancelled`,
`turn_input_required`.

---

## 8. Prompt Rendering — `internal/prompt` (§5.4, §12)

```go
func Render(template string, issue domain.Issue, attempt *int) (string, error)
```

Rendering uses a Liquid engine (`github.com/osteele/liquid`) — Liquid-compatible semantics satisfy
§5.4. **Strict requirements:** unknown variables MUST fail rendering, and unknown filters MUST fail
rendering (§5.4). This is the one place the chosen library needs care: osteele/liquid renders
undefined variables as empty by default. The design enforces strictness by a **pre-render reference
check** — walk the parsed template's variable and filter references and fail with
`template_render_error` if any variable is outside the known context (`issue.*`, `attempt`) or any
filter is not registered — before (or instead of) relying on the engine's lenient render. If the
library cannot be made strict enough, the fallback is a small custom strict renderer over the same
`{{ }}`/`{% %}` surface the workflows use. Parse failures map to `template_parse_error`, render
failures to `template_render_error` (§5.5).

Inputs (§12.1): `issue` (all normalized fields, keys stringified for template compatibility, nested
labels/blockers preserved so templates can iterate, §12.2) and `attempt` (`nil`/absent on first
run; integer on retry or continuation, §5.4, §12.3). The workflow prompt can branch on `attempt` to
differ between first run, continuation, and retry (§12.3).

Fallbacks (§5.4): an empty prompt body MAY use the minimal default
`You are working on an issue from Linear.`. Workflow read/parse failures are configuration errors
and do **not** fall back to a prompt (§5.4). A render failure fails only the current attempt and is
handled by the orchestrator like any worker failure (§12.4, §5.5 "Template errors fail only the
affected run attempt").

---

## 9. Orchestration: Poll, Dispatch, Reconcile, Retry — `internal/orchestrator` (§7,§8,§16)

The §16 reference algorithms translate directly into event handlers on the single loop (§2).

### 9.1 Startup (§16.1)

`configure logging → start observability outputs (status/http ext) → start workflow watch →
init RuntimeState → ValidateDispatch (fatal on failure, §6.3) → startup terminal workspace cleanup →
schedule immediate tick → run loop`.

### 9.2 Tick (§16.2, §8.1)

`reconcile → ValidateDispatch (skip dispatch on failure, keep reconcile) → FetchCandidateIssues
(on error log & skip) → sort → dispatch while slots remain → notify observers → reschedule at
effective poll interval`.

Sort order (§8.2): `priority` ascending (1..4 preferred; nil/unknown last) → `created_at` oldest
first → `identifier` lexicographic tie-break. Implemented with `sort.SliceStable`.

### 9.3 Eligibility (§8.2)

An issue dispatches only if: it has `id`, `identifier`, `title`, `state`; state ∈ active_states and
∉ terminal_states; routed to this worker by configured assignee **and** contains every
`required_labels` entry (post-normalization, §5.3.1); not in `running`; not in `claimed`; global
slot available; per-state slot available; and — if state is `Todo` — no blocker is non-terminal
(§8.2 blocker rule).

### 9.4 Concurrency (§8.3)

Global available slots `= max(max_concurrent_agents - len(running), 0)`. Per-state limit uses
`max_concurrent_agents_by_state[normalize(state)]` when present, else the global limit; issues are
counted by their current tracked state in `running`.

### 9.5 Dispatch one issue (§16.4)

Spawn the worker goroutine; if spawn fails schedule a retry with `next_attempt`. On success record
the `running` entry (identifier, issue snapshot, nil session fields, token counters at 0,
`retry_attempt = normalize(attempt)`, `started_at = now`), add to `claimed`, and remove any
`retry_attempts` entry (§16.4).

### 9.6 Reconciliation (§16.3, §8.5)

Runs first on every tick.

- **Part A — stall detection**: for each running issue compute `elapsed` since
  `last_agent_timestamp` (or `started_at` if no event yet); if `elapsed > stall_timeout_ms`
  terminate the worker and queue a retry; if `stall_timeout_ms <= 0` skip stall detection entirely
  (§8.5, §10.6).
- **Part B — tracker state refresh**: `FetchIssueStatesByIDs(running ids)`; on failure keep workers
  and retry next tick (§8.5). Per issue: terminal → terminate worker + clean workspace; active →
  update the in-memory issue snapshot; neither active nor terminal → terminate worker **without**
  workspace cleanup (§16.3, §8.5).

### 9.7 Worker exit & retry (§16.6, §8.4)

`on_worker_exit`: remove the running entry, add its run duration to cumulative runtime seconds
(§13.5). Normal exit → add to `completed` (bookkeeping only) and schedule a **continuation** retry
(attempt 1) with a short fixed 1000 ms delay so the orchestrator re-checks whether the issue is
still active and needs another worker session (§7.1, §8.4). Abnormal exit → schedule an
exponential-backoff retry `delay = min(10000 * 2^(attempt-1), max_retry_backoff_ms)` (§8.4).

`on_retry_timer` (§16.7): pop the retry entry; `FetchCandidateIssues`; on fetch failure requeue with
`retry poll failed`; find the issue by id — if absent release the claim; if no slot available
requeue with error `no available orchestrator slots`; otherwise dispatch with the entry's attempt.
An issue found but no longer active releases the claim (§8.4).

### 9.8 Startup terminal workspace cleanup (§8.6)

`FetchIssuesByStates(terminal_states)` → remove each returned issue's workspace; on fetch failure
log a warning and continue startup (§8.6, §11.4). Prevents stale terminal workspaces accumulating
across restarts.

### 9.9 Restart recovery (§14.3)

Scheduler state is in-memory only. After restart there are no restored timers or sessions; recovery
is startup terminal cleanup + fresh polling + re-dispatch of eligible work (§14.3), aided by
preserved workspaces (§9.1).

---

## 10. Observability — `internal/logging` (§13)

- **Structured logging** built on `log/slog`, emitting stable `key=value` phrasing with a fixed key
  order. Required context: `issue_id` and `issue_identifier` on issue-related logs; `session_id` on
  coding-agent lifecycle logs (§13.1). Each message carries an action outcome
  (`completed`/`failed`/`retrying`/…) and a concise failure reason; large raw payloads are avoided
  or truncated (§13.1, §15.4).
- **Secrets** (API tokens, `$VAR` secret values) are never logged; presence is validated without
  printing (§15.3).
- **Sinks** are not prescribed; the design supports one or more sinks and a failing sink does not
  crash the service — it degrades to remaining sinks with an operator-visible warning (§13.2).
- **Snapshot** for dashboards/monitoring is produced inside the loop via `SnapshotRequest` (§2.4,
  §13.3) and feeds both the terminal status surface and the HTTP API. Runtime seconds are a live
  aggregate: cumulative ended-session seconds plus active-session elapsed derived from `started_at`
  at snapshot time (§13.5); no continuous background ticking of totals is required (§13.5). The
  latest rate-limit payload seen in any agent update is retained (§13.5).

---

## 11. Extensions (all three selected)

### 11.1 HTTP server + dashboard — `internal/httpserver` (§13.7)

- **Enablement**: started when a CLI `--port` is provided, or when `server.port` is present in front
  matter; CLI `--port` overrides `server.port` (§13.7). `port == 0` requests an ephemeral port
  (dev/tests). Binds loopback (`127.0.0.1`) by default (§13.7).
- **Routes** on a `net/http` mux, all sourced from the loop snapshot (§2.4):
  - `GET /api/v1/state` — summary view (counts, running rows incl. `turn_count`, retry rows,
    `claude_totals` incl. `seconds_running`, `rate_limits`) matching the §13.7.2 shape.
  - `GET /api/v1/<issue_identifier>` — issue-specific runtime/debug detail; `404` with
    `{"error":{"code":"issue_not_found","message":"..."}}` when unknown (§13.7.2).
  - `POST /api/v1/refresh` — posts a `RefreshRequest` to the loop (coalesced), returns `202` with the
    `{queued, coalesced, requested_at, operations}` body (§13.7.2).
  - `GET /` — HTML dashboard rendered server-side from the snapshot (§13.7.1).
  - Wrong method on a defined route → `405`; all API errors use the `{"error":{code,message}}`
    envelope (§13.7.2).
- Port changes are restart-required (conformant, §13.7). The server is observability/control only and
  **must not** be required for orchestrator correctness (§13.7).

### 11.2 `linear_graphql` agent tool — `internal/tools/lineargql` (§10.5)

- Advertised to the Claude session using Claude Code's tool mechanism — a small MCP server (or
  `--allowedTools`) wired into `claude.command` (§10.5, §10.2).
- Executes exactly one GraphQL operation per call against the **configured** Linear endpoint/auth
  from the active workflow (no token-from-disk, §10.5). Input: non-empty `query` containing exactly
  one operation, optional object `variables`; a raw query string MAY be accepted as shorthand;
  multiple operations → invalid input (§10.5).
- Result semantics (§10.5): transport success with no top-level `errors` → `success=true`; top-level
  `errors` → `success=false` but preserve the GraphQL body; invalid input / missing auth / transport
  failure → `success=false` with an error payload. Unsupported tool names still fail without stalling
  the session (§10.5). Only meaningful when `tracker.kind == linear` with valid auth.
- Hardening: scoped to the intended project rather than workspace-wide tracker access (§15.5).

### 11.3 Terminal status surface — `internal/status` (§13.4)

A periodic terminal render driven from the loop snapshot: running rows (with `turn_count`), retry
queue with due delays, token and runtime totals, and latest rate limits (§13.3, §13.4). Read-only,
drawn from orchestrator state/metrics only, and **not** required for correctness (§13.4).

---

## 12. Third-Party Library Choices (with rationale)

| Concern            | Choice                          | Rationale |
|--------------------|---------------------------------|-----------|
| YAML front matter  | `gopkg.in/yaml.v3`              | De-facto standard; decodes to `map[string]any` for §5.2. |
| Prompt templating  | `github.com/osteele/liquid`     | Liquid-compatible semantics satisfy §5.4; strict-mode gap handled per §8 (pre-render reference check, custom fallback). |
| File watching      | `github.com/fsnotify/fsnotify`  | Cross-platform FS events for §6.2 reload. |
| GraphQL transport  | stdlib `net/http` + `encoding/json` | §11.2 wants isolated, exact query control; no heavy client needed. |
| Subprocess / stream| stdlib `os/exec` + `bufio`      | §10 one-subprocess-per-turn; 10 MB scanner buffer. |
| HTTP server        | stdlib `net/http`               | §13.7 baseline routes; no framework needed. |
| Logging            | stdlib `log/slog`               | Structured `key=value` per §13.1 with minimal deps. |
| Timers/concurrency | stdlib `time`, channels         | §2 actor loop, §8.4 retry timers. |

Design bias: minimize dependencies for a portable daemon (§3.2), keeping the coordination and
execution layers on the standard library.

---

## 13. Conformance Mapping & Test Strategy (§17, §18)

### 13.1 §18.1 REQUIRED-for-conformance → design section

| §18.1 item | Design |
|---|---|
| Workflow path selection (explicit + cwd default) | §3.1, §15 (CLI) |
| `WORKFLOW.md` loader (front matter + body) | §3.1 |
| Typed config w/ defaults and `$` resolution | §3.2 |
| Dynamic watch/reload/re-apply | §3.5 |
| Polling orchestrator, single-authority state | §2, §9 |
| Tracker client (candidate + refresh + terminal) | §5 |
| Workspace manager, sanitized per-issue workspaces | §6 |
| Workspace lifecycle hooks | §6.2 |
| Hook timeout config (`hooks.timeout_ms`) | §6.2 |
| Claude subprocess-per-turn `stream-json` client | §7.2 |
| Claude launch command config | §7.2, §3.2 |
| Strict prompt rendering (`issue`,`attempt`) | §8 |
| Exponential retry + continuation retries | §9.7 |
| Configurable backoff cap | §9.7 |
| Reconciliation stops on terminal/non-active | §9.6 |
| Workspace cleanup (startup sweep + transition) | §6.3, §9.6, §9.8 |
| Structured logs w/ issue/session context | §10 |
| Operator-visible observability | §10, §11 |

### 13.2 §17 test bullets → package

Each §17.1–§17.7 bullet maps to a package test: config/workflow parsing (§17.1 → `workflow`,
`config`, `prompt`), workspace & safety (§17.2 → `workspace`), tracker client (§17.3 →
`tracker/linear`), orchestrator dispatch/reconcile/retry (§17.4 → `orchestrator`), Claude
`stream-json` client (§17.5 → `agent/claude`, `tools/lineargql`), observability (§17.6 → `logging`,
`status`, `httpserver`), CLI/host lifecycle (§17.7 → `cmd/symphony`).

### 13.3 Test approach

- **Table-driven unit tests** per package (Go idiom), covering every deterministic §17 bullet.
- **Fake `tracker.Client`** returning scripted issue sets to drive deterministic orchestrator tests
  (sort order, blocker rule, slot exhaustion, reconciliation transitions, retry/backoff, §17.4).
- **Scriptable fake `claude` binary**: a tiny test program that emits canned newline-delimited
  `stream-json` (init/assistant/result, error results, malformed runs, missing-result) so the client
  and runner are tested without the real CLI (§17.5). Launch is still `bash -lc`, cwd asserted equal
  to the workspace (§9.5, §17.5).
- **`fsnotify` reload test** writing a changed `WORKFLOW.md` and asserting re-apply + last-good on
  invalid reload (§17.1).
- **Extension-conformance tests** for the three shipped extensions (§17.4 snapshot API, §17.5
  `linear_graphql` success/error/invalid-arg/unsupported-name, §17.6 status surface driven from
  state) — required because those extensions are shipped (§17 intro, §18.2).
- **Real Integration Profile (§17.8)** is opt-in and skippable: a real-tracker smoke test gated on
  `LINEAR_API_KEY`; a skipped run reports *skipped*, not passed.

### 13.4 Explicitly out of scope

The **SSH Worker Extension (Appendix A)** and the §18.2 TODOs (persist retry queue/session metadata
across restarts; configurable observability front matter; first-class tracker write APIs; pluggable
non-Linear adapters) are **not implemented** in this design — noted here so they are consciously
deferred rather than silently dropped.

---

## 14. Security & Operational Safety Posture (§15)

Per §15.1 Symphony must state its posture explicitly. This implementation targets **trusted
environments** with a **high-trust default**:

- **Trust boundary**: intended for trusted operators; the default `claude.command` auto-approves
  command execution and file changes for the session and fails user-input-required turns (§10.5
  example, §7.5). Operators tighten this by editing `claude.command` flags (`--permission-mode`,
  `--allowedTools`, `--disallowedTools`) (§5.3.6, §15.5).
- **Filesystem safety** (mandatory, §15.2): workspace path stays under the configured root; the
  coding-agent cwd is the per-issue workspace for the run; directory names use sanitized identifiers
  (§6.1, §6.3, §9.5).
- **Secret handling** (§15.3): `$VAR` indirection supported; API tokens and secret env values are
  never logged; presence validated without printing.
- **Hook safety** (§15.4): hooks are fully trusted config, run inside the workspace, are
  timeout-bounded (required, §15.4), and their output is truncated in logs.
- **Harness hardening** (§15.5): `linear_graphql` is project-scoped rather than workspace-wide; the
  set of client-side tools, credentials, and network destinations is kept to the workflow minimum;
  the design documents these as part of the core safety model, and operators MAY add external
  isolation (OS/container/VM sandbox, network limits) around the daemon.

---

## 15. CLI & Host Lifecycle — `cmd/symphony` (§17.7)

- Accepts a positional `path-to-WORKFLOW.md`; when absent uses `./WORKFLOW.md` (§5.1, §17.7).
- Errors cleanly on a nonexistent explicit path or a missing default `./WORKFLOW.md` (§17.7).
- Optional `--port` flag enables/overrides the HTTP server extension (§13.7).
- Surfaces startup failure cleanly and exits nonzero on startup failure or abnormal host exit; exits
  zero on normal startup + shutdown (§17.7). Handles `SIGINT`/`SIGTERM` via a root `context` that
  cancels the orchestrator loop and drains workers.

---

## Appendix — Traceability summary

Every design section above cites the SPEC sections it satisfies. The two conformance profiles are
covered as follows: **Core Conformance** (§18.1) by §§3–10 and §15; **Extension Conformance** for
the three shipped extensions by §11 (HTTP §13.7, `linear_graphql` §10.5, terminal status §13.4). The
SSH Worker Extension (Appendix A) and the §18.2 persistence/write-API/pluggable-adapter TODOs are
deferred (§13.4). The **Real Integration Profile** (§17.8) is provided as opt-in, skippable tests.
