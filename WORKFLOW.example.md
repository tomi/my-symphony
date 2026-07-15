---
# Symphony workflow contract (see SPEC.md §5). Copy to WORKFLOW.md and edit.
tracker:
  kind: linear
  # api_key may be a literal token or a $VAR reference resolved from the env.
  api_key: $LINEAR_API_KEY
  project_slug: my-project
  active_states: [Todo, In Progress]
  terminal_states: [Done, Cancelled, Canceled, Closed, Duplicate]
  required_labels: []          # every listed label must be present to dispatch

polling:
  interval_ms: 30000

workspace:
  # Relative paths resolve against this file's directory; ~ and $VAR expand.
  root: ~/symphony_workspaces

hooks:
  timeout_ms: 60000
  # after_create runs once when a workspace is first created. Use it to clone
  # or bootstrap the repository for the issue.
  after_create: |
    echo "created workspace for $(basename "$PWD")"
  # before_run runs before every agent attempt; a failure aborts the attempt.
  before_run: |
    echo "starting attempt in $PWD"

agent:
  max_concurrent_agents: 5
  max_turns: 20
  max_retry_backoff_ms: 300000
  max_concurrent_agents_by_state:
    "In Progress": 3

claude:
  # High-trust default: auto-approve command/file changes for the session.
  # Harden by editing these flags (see SPEC.md §15.5).
  command: claude -p --output-format stream-json --verbose --dangerously-skip-permissions
  # Global defaults for every state; override per-state under `states` below.
  # model appends `--model <model>`; reasoning_effort appends `--effort <level>`.
  model: sonnet                 # optional; omit to use the CLI default
  reasoning_effort: medium      # optional; low | medium | high | xhigh | max
  resume_across_turns: true
  turn_timeout_ms: 3600000
  read_timeout_ms: 5000
  stall_timeout_ms: 300000

# Per-status overrides (SPEC §5.3.7). Keyed by tracker status name; unset fields
# fall back to the global values above. Here an "AI Review" status is picked up
# with a stronger model, higher effort, review-specific instructions, and a
# tighter turn budget.
states:
  "AI Review":
    model: opus
    reasoning_effort: high
    prompt: prompts/review.md   # path relative to this file; replaces the body below
    max_turns: 5

# Optional HTTP dashboard/API extension (SPEC §13.7). Omit to disable, or pass
# --port on the CLI (which overrides this).
server:
  port: 8080
---
You are working on {{ issue.identifier }}: {{ issue.title }}

State: {{ issue.state }}
{% if issue.priority %}Priority: {{ issue.priority }}{% endif %}

{% if issue.description %}{{ issue.description }}{% endif %}

{% if issue.labels %}Labels: {% for label in issue.labels %}{{ label }} {% endfor %}{% endif %}

{% if attempt %}
This is a retry or continuation (attempt {{ attempt }}). Review what has already
been done in this workspace before continuing.
{% else %}
This is the first run for this issue. Read the repository, make the change, run
the tests, and move the ticket to its next handoff state when done.
{% endif %}
