# Quickstart — Linux

Get the `symphony` daemon running on Linux from a prebuilt release binary. No Go
toolchain required — we publish static `linux/amd64` builds to
[GitHub Releases](https://github.com/tomi/my-symphony/releases).

If you'd rather build from source, see the [README](../README.md#build) instead.

## Prerequisites

- A Linux `x86-64` machine (the release binaries are built for `linux/amd64`,
  `CGO_ENABLED=0`, so they run on any modern distro with no shared-library
  dependencies).
- A [Linear](https://linear.app) API key (`lin_api_...`) — create one under
  **Settings → Security & access → API → Personal API keys**.
- The [Claude Code CLI](https://docs.claude.com/en/docs/claude-code) on your
  `PATH`. Symphony launches `claude` as a subprocess for each issue; verify with
  `claude --version`.

## 1. Download the release

Grab the latest `linux/amd64` archive and its checksum from the
[Releases page](https://github.com/tomi/my-symphony/releases/latest), or from the
command line. Set `VERSION` to the tag you want (e.g. `v1.2.3`):

```sh
VERSION=v1.2.3
BASE="https://github.com/tomi/my-symphony/releases/download/${VERSION}"
ARCHIVE="symphony_${VERSION}_linux_amd64.tar.gz"

curl -fL -O "${BASE}/${ARCHIVE}"
curl -fL -O "${BASE}/${ARCHIVE}.sha256"
```

## 2. Verify the checksum

```sh
sha256sum -c "${ARCHIVE}.sha256"
# symphony_v1.2.3_linux_amd64.tar.gz: OK
```

If this prints anything other than `OK`, stop — do not run the binary.

## 3. Extract and install

The archive expands into a `symphony_<version>_linux_amd64/` directory
containing two binaries (`symphony`, `symphony-lineargql-mcp`) and a copy of the
README. Install them somewhere on your `PATH`:

```sh
tar -xzf "${ARCHIVE}"
cd "symphony_${VERSION}_linux_amd64"

# Install to ~/.local/bin (or /usr/local/bin with sudo).
mkdir -p ~/.local/bin
install -m 0755 symphony symphony-lineargql-mcp ~/.local/bin/

symphony --help    # confirm it runs
```

Make sure `~/.local/bin` is on your `PATH` (add `export PATH="$HOME/.local/bin:$PATH"`
to your shell profile if not).

> `symphony-lineargql-mcp` is optional — it's a small MCP server that exposes a
> `linear_graphql` tool to the Claude session. Install it only if you plan to
> wire it into `claude.command`. See the [README](../README.md#linear_graphql-tool-optional).

## 4. Configure

Symphony is driven by a `WORKFLOW.md` file (YAML front matter + a Liquid prompt
body). Start from the example bundled in the repo:

```sh
# From a checkout of the repo, or download WORKFLOW.example.md from GitHub:
curl -fL -o WORKFLOW.md \
  https://raw.githubusercontent.com/tomi/my-symphony/main/WORKFLOW.example.md
```

Then edit `WORKFLOW.md` and, at minimum, set:

- `tracker.project_slug` — the Linear project to poll.
- `tracker.active_states` / `tracker.terminal_states` — the states that gate
  dispatch and completion.
- `workspace.root` — where per-issue workspaces are created (default
  `~/symphony_workspaces`).
- `hooks.after_create` — how to clone/bootstrap the repo for each issue.

Provide your Linear API key via the environment (the example references
`$LINEAR_API_KEY`):

```sh
export LINEAR_API_KEY=lin_api_...
```

## 5. Run

```sh
symphony ./WORKFLOW.md
```

Useful flags:

- `--port N` — serve the HTTP dashboard/API on `127.0.0.1:N` (`0` picks a free
  port).
- `--status` — render a periodic status surface to stdout.

The daemon polls Linear on the configured interval, dispatches Claude Code in
isolated per-issue workspaces, and hot-reloads `WORKFLOW.md` when you edit it.
Press `Ctrl-C` (`SIGINT`) or send `SIGTERM` for a clean shutdown.

## Optional — run as a systemd service

To keep Symphony running in the background, create a user service. Adjust the
paths and put your secrets in an environment file that only your user can read:

```sh
mkdir -p ~/.config/symphony
printf 'LINEAR_API_KEY=lin_api_...\n' > ~/.config/symphony/env
chmod 600 ~/.config/symphony/env

mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/symphony.service <<'EOF'
[Unit]
Description=Symphony daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=%h/.config/symphony/env
ExecStart=%h/.local/bin/symphony %h/.config/symphony/WORKFLOW.md
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

# Place your edited WORKFLOW.md where the unit expects it, then:
systemctl --user daemon-reload
systemctl --user enable --now symphony.service
journalctl --user -u symphony -f      # follow the logs
```

(To keep the service running after you log out, enable lingering once:
`loginctl enable-linger "$USER"`.)

## Upgrading

Repeat steps 1–3 with the new `VERSION` and re-install over the old binaries. If
you're running the systemd service, restart it afterward:

```sh
systemctl --user restart symphony.service
```

## Troubleshooting

- **`claude: command not found` in the logs** — the Claude Code CLI isn't on the
  `PATH` of the process running Symphony. For the systemd service, set an
  explicit `PATH` or point `claude.command` at an absolute path.
- **No issues are picked up** — check that `tracker.project_slug`,
  `active_states`, and `required_labels` match your Linear setup, and that
  `LINEAR_API_KEY` is exported in the daemon's environment.
- **Permission posture** — the sample `claude.command` uses
  `--dangerously-skip-permissions` for a high-trust setup. Review the
  [security posture](../README.md#security-posture) and harden the flags before
  running against anything sensitive.
