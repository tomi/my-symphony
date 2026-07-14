# my-symphony

A Go implementation of the [Symphony Service Specification](SPEC.md) — a long-running daemon that
polls Linear for issues, runs the Claude Code CLI in isolated per-issue workspaces, and orchestrates
the work with bounded concurrency, retries, reconciliation, and observability.

- [`SPEC.md`](SPEC.md) — the language-agnostic specification.
- [`docs/GO_DESIGN.md`](docs/GO_DESIGN.md) — the Go implementation design (architecture, packages,
  library choices, and conformance mapping). Covers core conformance plus the HTTP server,
  `linear_graphql` tool, and terminal status extensions. The SSH Worker Extension is out of scope.

## Build

```sh
go build ./...      # or: make build   (outputs ./bin/symphony and the MCP server)
```

Requires Go 1.24+ (the module uses the 1.25 toolchain, fetched automatically by `go` when needed).

Prefer a prebuilt binary? See [`docs/QUICKSTART_LINUX.md`](docs/QUICKSTART_LINUX.md) for
installing and running the `linux/amd64` release from GitHub Releases — no Go toolchain required.

Run `make help` to list common developer commands (`build`, `run`, `test`, `test-race`, `fmt`,
`vet`, `tidy`, `ci`, …). `make ci` runs the same checks as the GitHub Actions workflow.

## Run

```sh
export LINEAR_API_KEY=lin_api_...
cp WORKFLOW.example.md WORKFLOW.md   # then edit it
go run ./cmd/symphony                # uses ./WORKFLOW.md by default
# or: go run ./cmd/symphony path/to/WORKFLOW.md
```

Flags:

- `--port N` — enable the HTTP dashboard/API on `127.0.0.1:N` (overrides `server.port`; `0` picks an
  ephemeral port).
- `--status` — render a periodic terminal status surface to stdout.

The daemon reloads `WORKFLOW.md` on change (polling cadence, concurrency, states, hooks, prompt, and
Claude settings) without a restart. `SIGINT`/`SIGTERM` triggers a clean shutdown.

## Architecture

A single orchestrator goroutine owns all scheduling state and is the sole writer; every input
(poll tick, agent update, worker exit, retry timer, config reload, snapshot request) is an event on
one channel. See `docs/GO_DESIGN.md` §2.

```
internal/
├── workflow/     WORKFLOW.md loader (front matter + body)
├── config/       typed config with defaults, $VAR/~ resolution, validation
├── domain/       normalized Issue, RuntimeState projections, snapshots
├── prompt/       strict Liquid rendering (issue + attempt)
├── tracker/      Client interface + linear/ GraphQL adapter
├── workspace/    per-issue workspaces, lifecycle hooks, path-safety invariants
├── agent/        Agent Runner + claude/ stream-json subprocess client
├── orchestrator/ single-authority event loop: dispatch/reconcile/retry
├── watcher/      fsnotify WORKFLOW.md reload
├── logging/      structured key=value logs
├── status/       [ext] terminal status surface (§13.4)
├── httpserver/   [ext] dashboard + /api/v1/* (§13.7)
└── tools/lineargql/ [ext] linear_graphql client-side tool (§10.5)

cmd/
├── symphony/              daemon entrypoint / composition root
└── symphony-lineargql-mcp/ MCP stdio server exposing linear_graphql
```

### `linear_graphql` tool (optional)

`cmd/symphony-lineargql-mcp` is a minimal MCP stdio server that advertises a single `linear_graphql`
tool to the Claude Code session. Wire it into `claude.command` (e.g. via `--mcp-config`); it reuses
Symphony's configured Linear auth from the process environment (`LINEAR_API_KEY`, optional
`LINEAR_ENDPOINT`) rather than reading tokens from disk. It executes exactly one GraphQL operation
per call and returns structured results the model can inspect.

## Security posture

This implementation targets **trusted environments** with a **high-trust default**: the sample
`claude.command` auto-approves command execution and file changes for the session and fails
user-input-required turns. Filesystem safety is mandatory and enforced — per-issue workspaces stay
under the configured root, directory names are sanitized, and the agent cwd is verified to be the
workspace before every launch. API tokens are never logged. Operators harden by editing
`claude.command` flags (`--permission-mode`, `--allowedTools`, `--disallowedTools`) and by adding
external isolation. See SPEC.md §15 and `docs/GO_DESIGN.md` §14.

## Test

```sh
go test ./...
go test -race ./...
```

Tests are table-driven per package and cover the SPEC §17 conformance matrix, including a fake
Linear GraphQL server, a scriptable fake `claude` CLI (inline bash emitting `stream-json`), and an
end-to-end CLI lifecycle test. The Real Integration Profile (§17.8) against live Linear is not run
in CI (it requires `LINEAR_API_KEY` and network access).
