# Deployment Guide

## Prerequisites

- **Go 1.24+**
- **Node.js 22+** and **pnpm** (for building the Angular web frontend)
- **Firebase project** with Realtime Database and Firestore enabled
- **Telegram bot token** (create one via [@BotFather](https://t.me/BotFather))
- **Provider binaries** installed:
  - **Claude Code**: `claude` in `$PATH` (or specify path during setup)
  - **OpenCode**: `opencode` in `$PATH` (or specify path during setup)
  - **Git**: Required for worktree isolation features

## Building from Source

```bash
git clone https://github.com/pufanyi/opencode-manager.git
cd opencode-manager
make build
```

`make build` runs two steps:
1. Builds the Angular frontend (`cd web && pnpm ng build --output-path ../internal/web/dist`)
2. Compiles the Go binary with the frontend embedded (`go build -o ./bin/opencode-manager ./cmd/opencode-manager`)

The resulting binary is at `./bin/opencode-manager`.

### Cross-compilation

Build the frontend first (only needed once), then cross-compile the Go binary:

```bash
# Build frontend
make web

# Linux ARM64 (e.g., Raspberry Pi, ARM servers)
GOOS=linux GOARCH=arm64 go build -o bin/opencode-manager-linux-arm64 ./cmd/opencode-manager

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o bin/opencode-manager-darwin-arm64 ./cmd/opencode-manager
```

## Firebase Project Setup

1. Go to the [Firebase Console](https://console.firebase.google.com/) and create a new project (or use an existing one).

2. Enable **Authentication** and add at least one sign-in provider (Google recommended, email/password also supported).

3. Enable **Realtime Database** (RTDB). Note the database URL (e.g., `https://your-project-default-rtdb.firebaseio.com`).

4. Enable **Cloud Firestore** in the same project.

5. Get your **Web API key** from Project Settings > General.

6. Deploy security rules:

   ```bash
   firebase deploy --only firestore:rules,database --project YOUR_PROJECT_ID
   ```

   The repository includes:
   - `firestore.rules` — Firestore security rules (user-scoped: each user can only read/write their own `users/{uid}/` subtree)
   - `firebase-rules.json` — RTDB security rules (user-scoped data under `users/{uid}/`, plus shared `link_codes/`)

## First-time Setup

### Option A: Interactive login (recommended)

```bash
./bin/opencode-manager login
```

This runs a 4-step interactive wizard:

1. **Sign in to Firebase** — Opens a browser window for Firebase authentication (Google or email/password). On completion, a `refresh_token` is obtained.

2. **Telegram Bot** — Prompts for your bot token (from @BotFather) and allowed Telegram user IDs (comma-separated). Send `/start` to [@userinfobot](https://t.me/userinfobot) on Telegram to find your user ID.

3. **AI Coding Tools** — Auto-detects `claude` and `opencode` binaries in `$PATH` and prompts for confirmation or manual paths.

4. **Save configuration** — Writes `credentials.yaml` locally (with `refresh_token` and auto-generated `client_id`) and pushes all other config (Telegram token, allowed users, binary paths) to Firestore.

The `login` command accepts flags to use a custom Firebase project:

```bash
./bin/opencode-manager login \
  --api-key "YOUR_API_KEY" \
  --database-url "https://your-project-default-rtdb.firebaseio.com" \
  --project-id "your-project" \
  --auth-domain "your-project.firebaseapp.com"
```

If no flags are provided, the default public project (`opencode-manager`) is used.

### Option B: Manual credentials file

```bash
cp credentials.yaml.example credentials.yaml
```

Edit `credentials.yaml`:

```yaml
firebase:
  api_key: "your-firebase-api-key"
  database_url: "https://your-project-default-rtdb.firebaseio.com"
  auth_domain: "your-project.firebaseapp.com"   # optional at runtime, used by login
  project_id: "your-project"                    # optional at runtime, used by login
  refresh_token: ""                             # set by ./bin/opencode-manager login

# Auto-generated on first run if empty.
client_id: ""
```

Then run `./bin/opencode-manager login` to complete the browser authentication and push config to Firestore.

### Refreshing credentials

If your Firebase token expires or becomes invalid, refresh it without re-entering all settings:

```bash
./bin/opencode-manager relogin
```

This opens a browser window, signs in again, and updates only the `refresh_token` in `credentials.yaml`. The server also offers interactive re-login if it detects expired credentials on startup.

## Running

```bash
./bin/opencode-manager
```

On startup, the binary:
1. Reads `credentials.yaml` for Firebase connection info
2. Connects to Firebase (authenticates with the stored `refresh_token`)
3. Pulls configuration from Firestore (Telegram token, binary paths, etc.)
4. Starts the Telegram bot, process manager, Firebase background services, and web dashboard

### Flags

```
Usage: opencode-manager [command] [flags]

Commands:
  login    Browser login + interactive cloud setup
  relogin  Refresh Firebase browser credentials in credentials.yaml
  (none)   Start the manager (default)

Flags:
  -credentials string   path to Firebase credentials file (default "./credentials.yaml")
  -dev                  enable dev mode with Angular dev server (HMR)
```

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
ExecStart=/usr/local/bin/opencode-manager -credentials /home/youruser/.config/opencode-manager/credentials.yaml
Restart=on-failure
RestartSec=5

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/home/youruser/projects /tmp/opencode-manager

[Install]
WantedBy=multi-user.target
```

```bash
sudo cp bin/opencode-manager /usr/local/bin/
sudo systemctl daemon-reload
sudo systemctl enable --now opencode-manager
sudo systemctl status opencode-manager

# View logs
journalctl -u opencode-manager -f
```

### Adjusting ReadWritePaths

The service needs write access to:

- Any project directories where the AI coding tools will write files
- `/tmp/opencode-manager/` (for temporary image downloads, worktree directories)

Update `ReadWritePaths` accordingly.

## Running with tmux / screen

For quick deployments:

```bash
tmux new-session -d -s ocm './bin/opencode-manager -credentials credentials.yaml'

# Reattach to view logs
tmux attach -t ocm
```

## Web Frontend

The Angular web dashboard is **embedded in the binary** at build time. No separate deployment is needed. When `web.enabled` is `true` in the Firestore config (or forced by `-dev` flag), the dashboard is served at the configured address (default `:8080`).

The frontend communicates with the Go server entirely through Firebase (RTDB + Firestore). Both sides are Firebase clients making outbound HTTPS connections -- there is no direct connection between them. This means the Go server needs no public IP, no port forwarding, and no tunnels.

### Development mode

For frontend development with Angular hot module replacement (HMR):

```bash
make dev
```

This starts the Go server with `-dev` flag, which:
1. Creates a stub `index.html` if the embedded dist is empty
2. Starts the Angular dev server (`pnpm ng serve`) as a subprocess
3. Reverse-proxies all non-API requests to the Angular dev server
4. Supports HMR and WebSocket connections for live reload

## Data and State

All persistent data is stored in Firebase:

| Data | Location | Description |
|------|----------|-------------|
| User config | Firestore `users/{uid}/config/user` | Telegram token, allowed users, board interval |
| Client config | Firestore `users/{uid}/config/clients/{clientID}` | Binary paths, port range, per-machine settings |
| Instances | Firestore `users/{uid}/instances/{id}` | Instance metadata (name, directory, provider type) |
| Sessions | Firestore `users/{uid}/instances/{id}/sessions/{sid}` | Session metadata (title, message count, branch) |
| Messages | Firestore `users/{uid}/instances/{id}/sessions/{sid}/messages/{mid}` | Conversation history |
| Runtime/presence | RTDB `users/{uid}/instances/{id}/runtime` | Online status, heartbeats |
| Streams | RTDB `users/{uid}/streams/{sessionId}` | Real-time streaming content |
| Commands | RTDB `users/{uid}/commands/{instanceId}/{cmdId}` | Web-to-server command queue |

There are no local databases. The only local file is `credentials.yaml`.

## Troubleshooting

### Bot doesn't respond

- Verify the token is correct: test with `curl https://api.telegram.org/bot<TOKEN>/getMe`
- Check that your user ID is in `allowed_users` (stored in Firestore config)
- Look at logs: `journalctl -u opencode-manager -f`

### Firebase connection fails

- Verify `credentials.yaml` has correct `api_key` and `database_url`
- If you see "refresh token invalid" errors, run `./bin/opencode-manager relogin` to refresh credentials
- Make sure RTDB and Firestore are enabled in your Firebase project
- Check that security rules are deployed

### Instance won't start

- Verify the provider binary works: `claude --version` or `opencode serve --help`
- Check the project directory exists and is accessible
- Look for port conflicts (OpenCode only): `ss -tlnp | grep 14096`

### Claude Code instance errors

- Check that `claude` is properly authenticated (run `claude` manually first)
- Ensure the project directory has a valid git repo or codebase
- Check stderr output in the manager logs

### Web dashboard not loading

- Ensure `web.enabled` is set in the Firestore user config, or use `-dev` flag
- Check the listen address (default `:8080`) isn't in use: `ss -tlnp | grep 8080`
- The frontend is embedded in the binary at build time -- if you built without `make web` first, the dashboard will be empty
