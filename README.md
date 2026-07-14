# my-symphony

A Go implementation of the [Symphony Service Specification](SPEC.md) — a long-running daemon that
polls Linear for issues, runs the Claude Code CLI in isolated per-issue workspaces, and orchestrates
the work with bounded concurrency, retries, reconciliation, and observability.

- [`SPEC.md`](SPEC.md) — the language-agnostic specification.
- [`docs/GO_DESIGN.md`](docs/GO_DESIGN.md) — the Go implementation design (architecture, packages,
  library choices, and conformance mapping). Covers core conformance plus the HTTP server,
  `linear_graphql` tool, and terminal status extensions. The SSH Worker Extension is out of scope.
