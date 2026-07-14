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

## 1. Install and configure

Set `VERSION` to the release tag you want (see the
[Releases page](https://github.com/tomi/my-symphony/releases/latest)), then
download, verify, install, and configure:

```sh
VERSION=v1.2.3
BASE="https://github.com/tomi/my-symphony/releases/download/${VERSION}"
ARCHIVE="symphony_${VERSION}_linux_amd64.tar.gz"

# Download the linux/amd64 archive and its checksum.
curl -fL -O "${BASE}/${ARCHIVE}"
curl -fL -O "${BASE}/${ARCHIVE}.sha256"

# Verify — stop if this prints anything other than "OK".
sha256sum -c "${ARCHIVE}.sha256"

# Extract (yields symphony_<version>_linux_amd64/) and install onto PATH.
# ~/.local/bin must be on your PATH; use /usr/local/bin with sudo instead.
tar -xzf "${ARCHIVE}"
mkdir -p ~/.local/bin
install -m 0755 "symphony_${VERSION}_linux_amd64/symphony" \
  "symphony_${VERSION}_linux_amd64/symphony-lineargql-mcp" ~/.local/bin/
symphony --help   # confirm it runs

# Configure: grab the example workflow and export your Linear API key.
curl -fL -o WORKFLOW.md \
  https://raw.githubusercontent.com/tomi/my-symphony/main/WORKFLOW.example.md
export LINEAR_API_KEY=lin_api_...
```

Now edit `WORKFLOW.md` and, at minimum, set `tracker.project_slug`,
`tracker.active_states` / `tracker.terminal_states`, `workspace.root`, and the
`hooks.after_create` hook that clones/bootstraps the repo for each issue.

> `symphony-lineargql-mcp` is optional — a small MCP server exposing a
> `linear_graphql` tool to the Claude session. Skip it unless you wire it into
> `claude.command`. See the [README](../README.md#linear_graphql-tool-optional).

## 2. Run

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
