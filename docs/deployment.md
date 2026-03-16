# Deployment Guide

## Building from Source

```bash
git clone https://github.com/pufanyi/opencode-manager.git
cd opencode-manager
make build
```

The binary is at `./bin/opencode-manager`.

### Cross-compilation

Since the project uses pure-Go SQLite (no CGo), cross-compilation works out of the box:

```bash
# Linux ARM64 (e.g., Raspberry Pi, ARM servers)
GOOS=linux GOARCH=arm64 go build -o bin/opencode-manager-linux-arm64 ./cmd/opencode-manager

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o bin/opencode-manager-darwin-arm64 ./cmd/opencode-manager
```

## Installation

Copy the binary to your server:

```bash
sudo cp bin/opencode-manager /usr/local/bin/
```

Make sure `opencode` is also installed and in `$PATH`, or set the full path in the config.

## First-time Setup

```bash
opencode-manager setup
```

Follow the 6-step wizard. The generated config is written to `opencode-manager.yaml` by default.

## Running as a systemd Service

Create `/etc/systemd/system/opencode-manager.service`:

```ini
[Unit]
Description=OpenCode Manager
After=network.target

[Service]
Type=simple
User=youruser
WorkingDirectory=/home/youruser
ExecStart=/usr/local/bin/opencode-manager -config /home/youruser/.config/opencode-manager/opencode-manager.yaml
Restart=on-failure
RestartSec=5

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/youruser/data /home/youruser/.config/opencode-manager

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now opencode-manager
sudo systemctl status opencode-manager

# View logs
journalctl -u opencode-manager -f
```

### Adjusting ReadWritePaths

The service needs write access to:

- The SQLite database directory (default: `./data/`)
- Any project directories where OpenCode will write files

Update `ReadWritePaths` accordingly.

## Running with Docker

Create a `Dockerfile`:

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /opencode-manager ./cmd/opencode-manager

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /opencode-manager /usr/local/bin/

# Install opencode (adjust for your setup)
# COPY opencode /usr/local/bin/

ENTRYPOINT ["opencode-manager"]
CMD ["-config", "/etc/opencode-manager/opencode-manager.yaml"]
```

```bash
docker build -t opencode-manager .
docker run -d \
  --name opencode-manager \
  -v /path/to/config:/etc/opencode-manager/opencode-manager.yaml:ro \
  -v /path/to/data:/data \
  -v /path/to/projects:/projects \
  opencode-manager
```

Note: The OpenCode binary must also be available inside the container.

## Running with tmux / screen

For quick deployments:

```bash
tmux new-session -d -s ocm 'opencode-manager -config opencode-manager.yaml'

# Reattach to view logs
tmux attach -t ocm
```

## Backup

The only stateful file is the SQLite database. Back it up periodically:

```bash
# Safe backup (uses SQLite's backup API via .backup command isn't available,
# but WAL mode makes file copy safe when no writes are in progress)
cp data/opencode-manager.db data/opencode-manager.db.bak
```

Or use SQLite's `.dump` if you have the CLI:

```bash
sqlite3 data/opencode-manager.db .dump > backup.sql
```

## Troubleshooting

### Bot doesn't respond

- Verify the token is correct: test with `curl https://api.telegram.org/bot<TOKEN>/getMe`
- Check that your user ID is in `allowed_users`
- Look at logs: `journalctl -u opencode-manager -f`

### Instance won't start

- Verify the `opencode` binary works: `opencode serve --help`
- Check the project directory exists and is accessible
- Look for port conflicts: `ss -tlnp | grep 14096`

### SSE connection drops

The SSE subscriber auto-reconnects after 2 seconds. Persistent drops may indicate:

- The OpenCode process crashed (check logs for restart messages)
- Network issues between localhost connections (unlikely but check firewall rules)

### Database locked

The database uses WAL mode which supports concurrent reads. If you see lock errors:

- Ensure only one manager instance is running
- Check for stale lock files: `ls data/opencode-manager.db*`
