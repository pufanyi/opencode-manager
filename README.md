# OpenCode Manager

A single-binary tool that manages multiple [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and [OpenCode](https://github.com/sst/opencode) instances on one server, controlled via Telegram bot and web dashboard. Run AI coding sessions across different projects from your phone or browser.

## Features

- **Cloud-first** — Only Firebase credentials stored locally; config, instances, sessions, and messages all live in Firebase
- **User-scoped data** — All data under `users/{uid}/` with Firebase security rules enforcing per-user isolation
- **Multi-client** — Each Go process has a unique `client_id` for instance ownership; run multiple servers safely
- **Web dashboard** — Real-time streaming visualization via Firebase; login with Google or email, no public IP needed
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
- A [Firebase](https://console.firebase.google.com) project with Realtime Database + Firestore enabled

## Quick Start

### 1. Build

```bash
make build
```

### 2. Set Up Firebase

1. Go to [Firebase Console](https://console.firebase.google.com) → Create project
2. Enable **Authentication** → Sign-in method → Google (and/or Email/Password)
3. Enable **Realtime Database** → Create database
4. Enable **Cloud Firestore** → Create database
5. In Project settings → General → Your apps → add a **Web app** → copy the `apiKey` and `databaseURL`

### 3. Login & Configure

```bash
cp credentials.yaml.example credentials.yaml
# Fill in api_key and database_url, then:
./bin/opencode-manager login
```

This opens a browser for Firebase sign-in, then prompts for:
- Telegram bot token
- Allowed Telegram user IDs
- Claude Code / OpenCode binary paths

Config is saved to Firestore. A `client_id` is auto-generated in `credentials.yaml`.

### 4. Deploy Security Rules

```bash
firebase deploy --only firestore:rules,database --project YOUR_PROJECT
```

### 5. Run

```bash
./bin/opencode-manager
```

The server will:
1. Read `credentials.yaml` → sign in to Firebase → extract UID from JWT
2. Pull config from Firestore (auto-migrates from legacy RTDB if needed)
3. Register this client, restore owned instances
4. Start Telegram bot, presence heartbeats, command listener, and web dashboard

If the stored refresh token expires:

```bash
./bin/opencode-manager relogin
```

## Architecture

```
┌────────────────┐           ┌──────────────────────────┐           ┌────────────────┐
│   Go Server    │──writes──→│        Firebase           │←─listens─│   Web Frontend │
│  (your server) │           │                           │           │ (embedded SPA) │
│                │←─listens─│  ┌─────────────────────┐  │──writes──→│                │
│ ├ Process Mgr  │           │  │ Auth (Google/email) │  │           │ ├ Login        │
│ ├ TG Bot       │           │  ├─────────────────────┤  │           │ ├ Dashboard    │
│ ├ Cmd Listener │           │  │ Firestore (durable) │  │           │ ├ Instance mgr │
│ └ Presence     │           │  │ └ users/{uid}/...   │  │           │ └ Prompt panel │
│                │           │  ├─────────────────────┤  │           │                │
│ No public IP   │           │  │ RTDB (realtime)     │  │           │ No backend     │
│ No open ports  │           │  │ └ users/{uid}/...   │  │           │ Pure SPA       │
└────────────────┘           │  └─────────────────────┘  │           └────────────────┘
                             └──────────────────────────┘
```

**Data storage split:**

| Store | Purpose | Data |
|-------|---------|------|
| **Firestore** | Durable records | Instances, sessions, messages, config, client registration |
| **RTDB** | Real-time ephemeral | Presence, streams, commands, Telegram user state |

**All paths are user-scoped** under `users/{uid}/` — one Firebase project safely serves multiple users.

### Source Layout

```
cmd/opencode-manager/
└── main.go                  Entry point, login wizard, config migration

internal/
├── firebase/
│   ├── auth.go              Firebase Auth (email/password + refresh token, UID extraction)
│   ├── client.go            Firebase client (Auth + RTDB + Firestore + Streamer + Presence)
│   ├── rtdb.go              Realtime Database REST client (CRUD + SSE listener)
│   ├── firestore.go         Firestore REST client
│   ├── paths.go             Centralized user-scoped path builders (RTDB + Firestore)
│   ├── streamer.go          Buffer & flush provider events to RTDB
│   ├── commands.go          Listen for web frontend commands from RTDB
│   ├── presence.go          Two-level heartbeat (client + per-instance)
│   └── telegram_state.go    Telegram user state + message-session mappings (RTDB)
├── app/app.go               Application orchestrator, client registration, command handler
├── config/config.go         Config struct, LoadFromSettings(userConfig, clientConfig)
├── store/                   Store interface + FirestoreStore (user-scoped)
├── process/                 Instance lifecycle, port pool, crash recovery, client ownership
├── provider/                Provider interface + Claude Code + OpenCode
├── bot/                     Telegram bot handlers, callbacks, streaming board
├── gitops/                  Git worktree merge-back
├── opencode/                OpenCode HTTP client
└── web/                     Embedded web dashboard (serves Angular build)

dashboard/                   Angular 19 — Local dashboard (direct HTTP API + SSE)
└── src/app/
    ├── services/api.service.ts                 Direct HTTP + SSE to Go server
    └── components/
        ├── instance-list/                      Instance management (create/start/stop/delete)
        ├── prompt-panel/                       Prompt + streaming via SSE (/api/ws)
        └── settings/                           Bot status, app settings

web/                         Angular 19 — Remote frontend (Firebase relay)
└── src/app/
    ├── services/firebase.service.ts            Firebase Auth + RTDB + Firestore
    ├── guards/auth.guard.ts                    Route guard
    └── components/
        ├── login/                              Google / email login
        ├── dashboard/                          Instance grid + account linking
        ├── instance-card/                      Instance status card
        └── prompt-panel/                       Session selector + streaming view
```

## CLI Reference

```
Usage: opencode-manager [command] [flags]

Commands:
  login    Browser login + interactive setup (pushes config to Firestore)
  relogin  Refresh Firebase browser credentials
  (none)   Start the manager (default)

Flags:
  -credentials string   Path to credentials file (default "./credentials.yaml")
  -dev                  Enable dev mode with Angular dev server (HMR)
```

## Makefile Targets

```bash
make build       # Build frontend + Go binary → bin/opencode-manager
make web         # Build Angular frontend only
make dev         # Dev mode: Go with Angular HMR
make run         # Build and run
make lint        # golangci-lint + Biome
make clean       # Remove build artifacts
make tidy        # go mod tidy
```

## Telegram Commands

| Command | Description |
|---------|-------------|
| `/new <name> <path>` | Create & start a Claude Code instance |
| `/newopencode <name> <path>` | Create & start an OpenCode instance |
| `/list` | List all instances with status |
| `/switch <name>` | Switch active instance |
| `/start_inst <name>` | Start a stopped instance |
| `/stop [name]` | Stop an instance |
| `/session new` | Create a new session |
| `/session` | Show current session info |
| `/sessions` | List & manage all sessions |
| `/abort` | Abort the running prompt |
| `/link <code>` | Link Telegram to web dashboard account |
| `/help` | Show help |
| _any text_ | Send as prompt to active instance |
| _photo_ | Send image for visual analysis |
| _reply to bot message_ | Continue that session |

## Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `TELEGRAM_TOKEN` | `telegram.token` |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` |
| `OPENCODE_BINARY` | `process.opencode_binary` |
| `CLAUDECODE_BINARY` | `process.claudecode_binary` |
| `FIREBASE_API_KEY` | Firebase API key |
| `FIREBASE_DATABASE_URL` | Firebase RTDB URL |
| `FIREBASE_PROJECT_ID` | Firebase project ID |

## License

MIT
