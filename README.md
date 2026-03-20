# OpenCode Manager

A single-binary tool that manages multiple [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and [OpenCode](https://github.com/sst/opencode) instances on one server, controlled via Telegram bot and web dashboard. Run AI coding sessions across different projects from your phone or browser.

## Features

- **Cloud-first config** — Only Firebase credentials stored locally; everything else (Telegram token, instance list, settings) lives in Firebase and syncs automatically
- **Web dashboard** — Real-time streaming visualization of Claude Code sessions via Firebase; login with email/password, no public IP needed
- **Dual provider support** — Run both Claude Code (CLI) and OpenCode (HTTP) instances side by side
- **Telegram interface** — Create, start, stop, switch, and prompt instances from any Telegram client
- **Active Tasks board** — Live status dashboard in Telegram showing all running tasks with tool progress
- **Git worktree isolation** — Each session can run in its own git worktree for parallel, conflict-free work
- **Auto-merge** — Worktree branches are automatically merged back to main after each prompt
- **Reply-to-continue** — Reply to any bot response to continue that session
- **Photo support** — Send images to Claude Code for visual analysis from Telegram
- **Crash recovery** — Auto-restarts crashed instances with exponential backoff
- **Single binary** — Embedded web frontend, no external scripts needed

## Prerequisites

- Go 1.24+
- Node.js 22+ and pnpm (for building the web dashboard)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and/or [OpenCode](https://github.com/sst/opencode) in `$PATH`
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- A [Firebase](https://console.firebase.google.com) project (free tier is sufficient)

## Quick Start

### 1. Build

```bash
# Build frontend + Go binary
make build

# Or just the Go binary (skip frontend build)
go build -o bin/opencode-manager ./cmd/opencode-manager
```

### 2. Set Up Firebase

1. Go to [Firebase Console](https://console.firebase.google.com) → Create project
2. Enable **Authentication** → Sign-in method → Email/Password
3. Enable **Realtime Database** → Create database
4. In Authentication → Users → **Add user** for the Go server:
   - Email: `go-bot@your-project.local` (anything works)
   - Password: choose a password
5. Add another user for yourself (web dashboard login):
   - Email: your email
   - Password: your password
6. In Realtime Database → **Rules**, paste:

```json
{
  "rules": {
    "config": {
      ".read": "auth != null",
      ".write": "auth != null"
    },
    "instances": {
      ".read": "auth != null",
      ".write": "auth != null"
    },
    "streams": {
      "$sessionId": {
        ".read": "auth != null",
        ".write": "auth != null"
      }
    },
    "commands": {
      "$instanceId": {
        "$commandId": {
          ".read": "auth != null",
          ".write": "auth != null"
        }
      }
    },
    "presence": {
      "$instanceId": {
        ".read": "auth != null",
        ".write": "auth != null"
      }
    }
  }
}
```

7. In Project settings → General → Your apps → add a **Web app** → copy the config values

### 3. Create Credentials File

```bash
cp credentials.yaml.example credentials.yaml
```

Edit `credentials.yaml`:

```yaml
firebase:
  api_key: "AIzaSy..."                                             # from web app config
  database_url: "https://your-project-default-rtdb.firebaseio.com"  # from web app config
  auth_domain: "your-project.firebaseapp.com"                       # optional, used by `login`
  project_id: "your-project"                                        # optional, used by `login`
  email: "go-bot@your-project.local"                                # the Go server user from step 4
  password: "your-password"
```

If you use `./bin/opencode-manager login`, it will reuse these Firebase project values from `credentials.yaml`, or you can pass them explicitly with `--api-key`, `--database-url`, `--auth-domain`, and `--project-id`.

### 4. Add Initial Config to Firebase

In Firebase Console → Realtime Database, manually add a `/config` node with these keys:

| Key | Value | Required |
|-----|-------|----------|
| `telegram.token` | Your bot token from @BotFather | Yes |
| `telegram.allowed_users` | Your Telegram user ID (comma-separated for multiple) | Yes |
| `process.claudecode_binary` | Path to `claude` binary (default: `claude`) | No |
| `process.opencode_binary` | Path to `opencode` binary (default: `opencode`) | No |
| `process.port_range_start` | Start of port range for OpenCode (default: `14096`) | No |
| `process.port_range_end` | End of port range (default: `14196`) | No |

### 5. Run

```bash
./bin/opencode-manager
```

The server will:
1. Read `credentials.yaml`
2. Connect to Firebase and authenticate
3. Pull config from Firebase (or wait for it to be set)
4. Start the Telegram bot, process manager, and Firebase sync

If the stored Firebase browser token expires, refresh it with:

```bash
./bin/opencode-manager relogin
```

On an interactive terminal, startup will also offer to re-login automatically when Firebase returns auth-related errors such as `401` or `Permission denied`.

### 6. Deploy Web Frontend

The web dashboard can be deployed to Firebase Hosting:

```bash
# Install Firebase CLI
npm install -g firebase-tools

# Login and initialize
firebase login
firebase init hosting
# → Select your project
# → Public directory: internal/web/dist/browser
# → Single-page app: Yes

# Build and deploy
make web
firebase deploy --only hosting
```

Your dashboard will be available at `https://your-project.web.app`. Log in with the web user you created in step 5.

Alternatively, deploy to any static hosting (Vercel, Cloudflare Pages, etc.) by pointing it to the `internal/web/dist/browser` build output.

### Legacy Mode (no Firebase)

If you prefer to use local SQLite config without Firebase:

```bash
# Interactive setup wizard (stores config in local SQLite)
./bin/opencode-manager setup

# Run in legacy mode
./bin/opencode-manager --legacy
```

## Architecture

```
┌────────────────┐           ┌──────────────────────────┐           ┌────────────────┐
│   Go Server    │──writes──→│        Firebase           │←─listens─│   Web Frontend │
│  (your server) │           │                           │           │ (Firebase Host │
│                │←─listens─│  ┌─────────────────────┐  │──writes──→│  / Vercel /    │
│ ├ Process Mgr  │           │  │ Auth (email/pass)   │  │           │  any static)   │
│ ├ TG Bot       │           │  ├─────────────────────┤  │           │                │
│ ├ Firebase Sync│           │  │ Realtime Database   │  │           │ ├ Login        │
│ └ Cmd Listener │           │  │ ├ /config           │  │           │ ├ Dashboard    │
│                │           │  │ ├ /instances        │  │           │ ├ Instance mgr │
│ No public IP   │           │  │ ├ /streams          │  │           │ └ Session view │
│ No open ports  │           │  │ ├ /commands         │  │           │                │
│                │           │  │ └ /presence         │  │           │ No backend     │
└────────────────┘           │  └─────────────────────┘  │           │ Pure SPA       │
                             └──────────────────────────┘           └────────────────┘
```

**Data flows (all through Firebase, no direct connection):**

| Flow | Path | Direction |
|------|------|-----------|
| Instance list | Go syncs every 2s → `/instances` → Web `onValue()` | Go → Web |
| Streaming tokens | Go buffers 300ms → `/streams/{session}` → Web `onValue()` | Go → Web |
| Commands | Web `push()` → `/commands/{instance}/{id}` → Go SSE listener | Web → Go |
| Presence | Go heartbeat 30s → `/presence/{instance}` → Web `onValue()` | Go → Web |
| Config | Go pulls on startup ← `/config` ← Web/Firebase Console writes | Web → Go |

### Source Layout

```
cmd/opencode-manager/
└── main.go                  Entry point: cloud-first boot or legacy mode

internal/
├── firebase/
│   ├── client.go            Firebase client (Auth + RTDB + Streamer + Presence)
│   ├── auth.go              Email/password auth via REST API (no Admin SDK)
│   ├── rtdb.go              Realtime Database REST client (CRUD + SSE listener)
│   ├── streamer.go          Buffer & flush provider events to RTDB
│   ├── commands.go          Listen for web frontend commands from RTDB
│   ├── presence.go          Heartbeat to RTDB (online/offline status)
│   ├── config_sync.go       Pull/push app config from Firebase
│   └── sync.go              Periodic instance list sync to RTDB
├── app/app.go               Application orchestrator + Firebase command handler
├── config/config.go         Config struct + settings loader + env overrides
├── store/                   Local SQLite (state cache)
├── process/                 Instance lifecycle, port pool, crash recovery
├── provider/                Provider interface + Claude Code + OpenCode
├── bot/                     Telegram bot handlers + streaming board
├── gitops/                  Git worktree merge-back
└── web/                     Embedded web dashboard (serves Angular build)

web/                         Angular 19 frontend source
├── src/
│   ├── environments/        Firebase project config
│   └── app/
│       ├── services/
│       │   └── firebase.service.ts   Firebase Auth + RTDB (replaces old API service)
│       ├── guards/
│       │   └── auth.guard.ts         Route guard (redirect to /login if unauthenticated)
│       └── components/
│           ├── login/                Email/password login page
│           ├── dashboard/            Instance grid + controls
│           ├── instance-card/        Instance status card
│           └── prompt-panel/         Session selector + real-time streaming view
```

## CLI Reference

```
Usage: opencode-manager [command] [flags]

Commands:
  setup    Interactive setup wizard (legacy, local config)
  (none)   Start the manager (default)

Flags:
  -credentials string   Path to Firebase credentials file (default "./credentials.yaml")
  -db string            Path to local database file (optional)
  -dev                  Enable dev mode with Angular dev server (HMR)
  -legacy               Use local SQLite config instead of Firebase
```

## Makefile Targets

```bash
make build       # Build frontend + Go binary → bin/opencode-manager
make web         # Build Angular frontend only
make dev         # Dev mode: Go with Angular HMR
make run         # Build and run (cloud mode)
make run-legacy  # Build and run (legacy SQLite mode)
make lint        # Lint Go + frontend
make clean       # Remove build artifacts
make tidy        # go mod tidy
```

## Telegram Commands

### Instance Management

| Command | Description |
|---------|-------------|
| `/new <name> <path>` | Create & start a new Claude Code instance |
| `/newopencode <name> <path>` | Create & start a new OpenCode instance |
| `/list` | List all instances with status |
| `/switch <name>` | Switch your active instance |
| `/start_inst <name>` | Start a stopped instance |
| `/stop [name]` | Stop an instance |

### Session & Prompting

| Command | Description |
|---------|-------------|
| `/session new` | Create a new session |
| `/session` | Show current session info |
| `/sessions` | List all sessions |
| `/abort` | Abort the running prompt |
| _any text_ | Send as a prompt |
| _photo_ | Send image for visual analysis |
| _reply to bot message_ | Continue that session |

## Environment Variable Overrides

These override values from Firebase config:

| Variable | Overrides |
|----------|-----------|
| `TELEGRAM_TOKEN` | `telegram.token` |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` |
| `OPENCODE_BINARY` | `process.opencode_binary` |
| `CLAUDECODE_BINARY` | `process.claudecode_binary` |
| `STORAGE_DATABASE` | Local SQLite path |

## License

MIT
