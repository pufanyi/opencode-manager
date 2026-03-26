# Architecture

## Overview

OpenCode Manager is a single Go binary (`opencode-manager`) that supervises multiple Claude Code and OpenCode instances. It exposes four interfaces:

- **Telegram bot** -- primary mobile interface for sending prompts and managing instances
- **Local dashboard** (`dashboard/`) -- Angular 21 SPA embedded in the binary, talks directly to the Go HTTP API + SSE
- **Remote web frontend** (`web/`) -- Angular 21 SPA hosted on Firebase Hosting, communicates via Firebase RTDB/Firestore relay
- **REST API + SSE** -- direct HTTP endpoints used by the local dashboard and other clients

All persistent data lives in Firebase (Firestore for durable records, RTDB for real-time ephemeral state). The Go process is a **client** to Firebase -- it signs in as a regular Firebase user via the REST API and reads/writes data under its own UID. There is no local database, no Admin SDK, and no service account.

```
                        ┌───────────────────────────────────────────────────────┐
┌─────────────┐         │              opencode-manager (Go binary)             │
│  Telegram   │◄───────►│                                                       │
│  (mobile)   │         │  ┌─────────┐  ┌─────────────┐  ┌─────────────────┐  │
└─────────────┘         │  │   Bot   │  │   Process   │  │ FirestoreStore  │  │
                        │  │         │  │   Manager   │  │  (via REST API) │  │
┌─────────────┐         │  └────┬────┘  └──────┬──────┘  └────────┬────────┘  │
│   Local     │◄─HTTP──►│  ┌────┴────┐  ┌──────┴──────┐           │           │
│  Dashboard  │◄─SSE────│  │  Web    │  │  Provider   │           │           │
│  (embedded) │         │  │ Server  │  │ Abstraction │           │           │
└─────────────┘         │  └─────────┘  └──────┬──────┘           │           │
                        │                      │                  │           │
┌─────────────┐         │           ┌──────────┼──────────┐       │           │
│  Firebase   │         │           │          │          │       │           │
│  Web App    │◄──RTDB──┤     ┌─────▼─────┐ ┌──▼───────┐ │       │           │
│  (remote)   │         │     │claude -p  │ │ opencode │ │       │           │
└─────────────┘         │     │(per prompt)│ │ serve ×N │ │       │           │
                        │     └───────────┘ └──────────┘ │       │           │
                        └───────────────────────────────────────────────────────┘
                                         │                        │
                                         └────────────────────────┘
                                            Firebase (Firestore + RTDB)
```

## Key Design Principles

1. **User-scoped data** -- all data in both RTDB and Firestore lives under `users/{uid}/`. Security rules enforce that each authenticated user can only access their own subtree.

2. **Client identity** -- each Go process has a unique `clientID` (UUID, persisted in `credentials.yaml`). This enables multi-machine deployments where multiple Go clients share the same Firebase user account. Instances record which client owns them.

3. **RTDB for ephemeral real-time data** -- presence heartbeats, streaming content, commands from the web frontend, and Telegram user state. This data is transient and does not need to survive indefinitely.

4. **Firestore for durable records** -- instances, sessions, messages, client registrations, and configuration. This data must persist across restarts and be queryable.

5. **No direct connections** -- the web frontend and Go backend never connect to each other. Both are clients of Firebase. Commands flow through RTDB; durable state flows through Firestore.

## Package Structure

### `cmd/opencode-manager/`

Entry point. Handles three subcommands:

- `(default)` -- read `credentials.yaml`, sign in to Firebase, create FirestoreStore, load config from Firestore (with RTDB migration fallback), validate, start the application
- `login` -- interactive browser-based setup: sign in via local HTTP server, configure Telegram bot and binary paths, push config to Firestore, write `credentials.yaml`
- `relogin` -- refresh an expired browser credential without reconfiguring

| File | Purpose |
|------|---------|
| `main.go` | Entry point, `runServe()`, signal handling |
| `login.go` | Interactive login wizard (4-step browser-based setup) and `relogin` |
| `setup.go` | Firebase config resolution, Firestore adapter, credential recovery, RTDB migration |
| `helpers.go` | CLI utilities (read/write credentials, print, prompt, openBrowser) |

### `internal/app/`

Top-level orchestrator (`App`). Wires together all components on startup:

1. Creates the process manager with port pool
2. Creates TelegramState (RTDB-backed)
3. Initializes the Telegram bot
4. Sets up Firebase streamer and command handler
5. Registers the client in Firestore
6. Restores previously running instances
7. Starts Firebase background services (presence + command listener)
8. Starts the embedded web dashboard (if enabled)
9. Runs the Telegram bot (blocking)

Also dispatches commands from the web frontend: `create`, `start`, `stop`, `delete`, `prompt`, `create_session`, `list_sessions`.

### `internal/bot/`

Telegram bot implementation.

| File | Purpose |
|------|---------|
| `bot.go` | Bot lifecycle, command routing, auth middleware |
| `handlers.go` | Shared types (`pendingPrompt`, `Handlers`), constructor, `getActiveInstance` |
| `commands.go` | Command handlers (`/new`, `/list`, `/switch`, `/stop`, `/session`, etc.) |
| `prompt.go` | Prompt flow (`HandlePrompt`, `HandlePhoto`, worktree choice, main-dir conflict, queue) |
| `callbacks.go` | Inline keyboard callback handlers (worktree choice, session selection) |
| `stream_context.go` | Individual stream lifecycle (consume events, flush, send response, merge-back) |
| `stream_manager.go` | Stream orchestrator (start, remove, stop task, notify) |
| `board.go` | Active Tasks board -- rendering, refresh loop, stop-button keyboard |
| `format.go` | Markdown-to-Telegram HTML conversion with tag balancing |
| `keyboard.go` | Inline keyboard builders |

### `internal/config/`

Configuration types and loading. No file I/O -- config values come from Firestore maps.

- `Config`, `TelegramConfig`, `ProcessConfig`, `WebConfig`, `FirebaseConfig` structs
- `Defaults()` -- sensible defaults
- `LoadFromSettings(userConfig, clientConfig)` -- builds Config from two `map[string]string` maps
- `ApplyEnvOverrides(cfg)` -- environment variable overrides
- `Validate(cfg)` -- checks required fields and value ranges
- `ToUserSettings()` / `ToClientSettings()` -- serialize back to maps for Firestore storage

### `internal/firebase/`

Firebase REST API client layer. The Go process signs in as a regular Firebase user (not Admin SDK).

| File | Purpose | Mechanism |
|------|---------|-----------|
| `auth.go` | Firebase Auth (email/password or refresh token) | `identitytoolkit.googleapis.com`, `securetoken.googleapis.com` |
| `rtdb.go` | RTDB REST client: Get, Set, Update, Delete, Listen (SSE) | HTTPS with `?auth=<idToken>` |
| `firestore.go` | Firestore REST client: GetDoc, SetDoc, UpdateDoc, DeleteDoc, ListDocs | Firestore REST API |
| `client.go` | Ties together Auth, RTDB, Firestore, Streamer, Presence, Commands | Initialization + `StartBackground()` |
| `paths.go` | Path builder functions for all RTDB and Firestore locations | Pure functions |
| `presence.go` | Two-level heartbeats (client + per-instance) to RTDB | Periodic PATCH every 30s |
| `streamer.go` | Buffers provider StreamEvents and flushes to RTDB | Periodic PATCH every 300ms |
| `commands.go` | Watches RTDB for commands from web frontend, dispatches and updates status | SSE listener + PATCH |
| `telegram_state.go` | Telegram user state and message-session mappings in RTDB | Get/Set/Update |

### `internal/process/`

Instance lifecycle management.

| File | Purpose |
|------|---------|
| `manager.go` | Creates, starts, stops, deletes instances. Provider factory. Shutdown. |
| `manager_recovery.go` | Crash recovery with exponential backoff. Health checks. Instance restore on boot. |
| `instance.go` | Wraps a `Provider` with metadata (name, directory, status, provider type, client ID) |
| `portpool.go` | Thread-safe port allocator over a configurable range |

### `internal/provider/`

Provider abstraction with two implementations.

| File | Purpose |
|------|---------|
| `provider.go` | `Provider` interface, `StreamEvent` type, `Type` constants (`claudecode`, `opencode`) |
| `claudecode.go` | Claude Code core provider -- session CRUD, `Prompt()`, `Abort()`, lifecycle |
| `claudecode_worktree.go` | Git worktree management (create, remove, merge, sync, FIFO eviction) |
| `claudecode_maindir.go` | Main-directory exclusive locking (`IsMainDirBusy`, `TryAcquire`, `Release`) |
| `claudecode_parser.go` | Stream-JSON event parsing, Claude event types, tool detail extraction |
| `opencode.go` | OpenCode provider -- manages persistent `opencode serve` child process, HTTP REST + SSE |

### `internal/store/`

Persistence layer.

| File | Purpose |
|------|---------|
| `iface.go` | `Store` interface + domain types (`Instance`, `ClaudeSession`, `ClientInfo`, `Message`, `ToolCall`) |
| `firestore_store.go` | `FirestoreStore` -- Firestore-backed implementation of `Store`, scoped to `users/{uid}/` |
| `firestore_helpers.go` | Serialization helpers (`docToInstance`, `docToSession`, `getString`, `getInt`, `parseTimestamp`) |
| `firestore_adapter.go` | `FirestoreAdapter` -- closure-based bridge from `firebase.Firestore` to `store.FirestoreClient` (avoids import cycles) |

### `internal/web/`

Embedded dashboard server. Serves the local dashboard (Angular build) and exposes the REST API + SSE hub used by both the local dashboard and external clients.

| File | Purpose |
|------|---------|
| `server.go` | HTTP server lifecycle, embedded Angular build via `go:embed`, CORS middleware |
| `api.go` | REST API handlers (instances CRUD, sessions, prompt, abort, settings) |
| `hub.go` | SSE StreamHub for real-time event streaming to browser clients |
| `devproxy.go` | Reverse proxy to Angular dev server for HMR during development |

### `dashboard/` (Angular 21 — Local Dashboard)

Local-only Angular SPA embedded in the Go binary at build time. Communicates directly with the Go HTTP API and SSE hub — no Firebase dependency.

| File | Purpose |
|------|---------|
| `src/app/services/api.service.ts` | HTTP client for REST API + EventSource for SSE streaming |
| `src/app/components/instance-list/` | Instance management (create, start, stop, delete) |
| `src/app/components/prompt-panel/` | Session selector, prompt input, real-time streaming via SSE (`/api/ws`) |
| `src/app/components/settings/` | Bot status, app settings |

**Key difference from `web/`**: No auth, no Firebase JS SDK. All data flows directly via `fetch()` to `/api/*` endpoints and `EventSource` to `/api/ws`.

### `web/` (Angular 21 — Remote Web Frontend)

Firebase-powered Angular SPA for remote access. Communicates entirely through Firebase RTDB and Firestore — no direct HTTP connection to the Go server.

| File | Purpose |
|------|---------|
| `src/app/services/firebase.service.ts` | Firebase Auth + RTDB + Firestore operations |
| `src/app/guards/auth.guard.ts` | Route guard requiring Firebase Auth |
| `src/app/components/login/` | Google / email sign-in |
| `src/app/components/dashboard/` | Instance grid, account linking |
| `src/app/components/instance-card/` | Instance status card with provider badge |
| `src/app/components/prompt-panel/` | Session selector, message history, real-time streaming via RTDB |

**Key difference from `dashboard/`**: Requires Firebase Auth (Google or email). Commands go through RTDB → Go server picks up via SSE. No direct HTTP to Go.

### `internal/gitops/`

| File | Purpose |
|------|---------|
| `merge.go` | Git worktree merge-back logic -- merges session branch to main after prompt completion |

### `internal/opencode/`

HTTP client for the OpenCode REST API.

| File | Purpose |
|------|---------|
| `client.go` | REST client for OpenCode endpoints (sessions, prompts, health) |
| `sse.go` | SSE subscriber with auto-reconnect and heartbeat timeout |
| `types.go` | OpenCode API types |

## Data Model

### Firestore (durable records)

All paths are under `users/{uid}/`.

```
users/{uid}/
├── instances/{id}                           # Instance record
│   ├── id, name, directory, port, password
│   ├── status, auto_start, provider_type
│   ├── client_id                            # Which Go client owns this
│   ├── created_at, updated_at
│   └── sessions/{sid}                       # Session record
│       ├── id, instance_id, title
│       ├── worktree_path, branch
│       ├── message_count, created_at, updated_at
│       └── messages/{mid}                   # Message record
│           ├── id, role, content
│           ├── tool_calls[]                 # {name, status, detail, input, output}
│           └── created_at
├── clients/{clientId}                       # Client registration
│   ├── client_id, hostname
│   ├── started_at, updated_at
├── config/
│   ├── user                                 # User-level config (shared across clients)
│   │   ├── telegram.token
│   │   ├── telegram.allowed_users
│   │   ├── telegram.board_interval
│   │   ├── web.enabled
│   │   └── web.addr
│   └── clients/{clientId}                   # Per-client config
│       ├── process.opencode_binary
│       ├── process.claudecode_binary
│       ├── process.port_range_start
│       ├── process.port_range_end
│       ├── process.health_check_interval
│       └── process.max_restart_attempts
```

### RTDB (real-time ephemeral data)

All paths are under `users/{uid}/`.

```
users/{uid}/
├── clients/{clientId}/presence              # Client heartbeat
│   ├── online: boolean
│   └── last_seen: number (ms)
├── instances/{id}/runtime                   # Instance presence
│   ├── online: boolean
│   ├── client_id: string
│   └── last_seen: number (ms)
├── streams/{sessionId}                      # Live streaming content
│   ├── content: string
│   ├── status: "streaming" | "complete" | "error"
│   ├── tool_calls: [{name, status, detail}]
│   ├── client_id: string
│   ├── error?: string
│   └── updated_at: number (ms)
├── commands/{instanceId}/{cmdId}            # Web frontend commands
│   ├── action: string
│   ├── payload: object
│   ├── status: "pending" | "ack" | "done" | "error"
│   ├── user_id: string
│   ├── acked_by_client_id?: string
│   ├── result?: object
│   ├── error?: string
│   └── updated_at: number (ms)
└── telegram/
    ├── user_state/{telegramUserId}          # Active instance/session per Telegram user
    │   ├── active_instance_id: string
    │   ├── active_session_id: string
    │   └── updated_at: number (ms)
    └── message_sessions/{chatId}_{msgId}    # Telegram message-to-session mapping
        └── session_id: string
```

## Data Flows

### Boot Sequence

```
1. Read credentials.yaml (Firebase connection info + client_id)
2. Auto-generate client_id if missing, persist to file
3. Sign in to Firebase (refresh token or email/password)
4. Extract UID from JWT
5. Create FirestoreStore scoped to users/{uid}/
6. Pull user config from Firestore (users/{uid}/config/user)
7. Pull client config from Firestore (users/{uid}/config/clients/{clientId})
8. If no config found: attempt migration from legacy RTDB /config
9. If still no config: exit with hint to run 'login'
10. Build Config from settings maps, apply env overrides
11. Validate config
12. Create App (process manager, bot, web server, Firebase background services)
13. Register client in Firestore
14. Restore previously running instances
15. Start presence heartbeats + command listener
16. Start web dashboard (if enabled)
17. Start Telegram bot (blocking)
```

### Telegram Prompt Flow

```
1. Telegram message arrives → auth check (allowed_users)
2. If reply to a bot message → look up session from RTDB message_sessions
3. Otherwise → read user state from RTDB → active_instance_id
4. If new session needed and provider supports worktree:
   → Show worktree choice keyboard, wait for selection
5. Create session (with or without worktree), auto-title from first prompt
6. Provider.Prompt(sessionID, content) → returns StreamEvent channel
7. StreamManager (bot layer) reads events, buffers text + tool progress
8. Active Tasks board refreshes on timer (default 2s):
   - Shows all running tasks as blockquote cards
   - Displays tool invocations with progress icons
   - "Stop #N" buttons to cancel individual tasks
9. Streamer (Firebase layer) wraps channel, flushes to RTDB every 300ms
   → Web frontend sees updates via onValue(/streams/{sessionId})
10. On completion:
    - Final response sent as reply to original Telegram message
    - User + assistant messages persisted to Firestore
    - Auto-merge worktree branch back to main (if applicable)
    - Board removes completed task
```

### Local Dashboard Flow (direct HTTP + SSE)

```
1. Dashboard calls REST API directly:
   POST /api/prompt { instance_id, session_id, content }
   → Go handler validates, calls Provider.Prompt()
   → Returns 200 immediately

2. Dashboard opens SSE connection:
   GET /api/ws?session={sessionId}
   → Go StreamHub broadcasts provider events as SSE messages

3. Events flow:
   Provider.Prompt() → event channel → StreamHub.Broadcast()
                                      → SSE to dashboard (immediate)
                                      → Streamer.WrapEvents() → RTDB (for web frontend)

4. Dashboard receives SSE events in real time:
   EventSource.onmessage → update UI (text, tool calls, done/error)
```

### Web Command Flow (via Firebase)

```
1. Web frontend pushes command to RTDB:
   users/{uid}/commands/{instanceId}/{cmdId}
   { action: "prompt", payload: {...}, status: "pending" }
2. Go CommandListener (SSE on /commands) detects new command
3. Go updates status → "ack" with acked_by_client_id
4. Go dispatches command (create/start/stop/delete/prompt/create_session/list_sessions)
5. For prompt commands:
   - Provider.Prompt() → event channel
   - Streamer.WrapEvents() writes to RTDB /streams/{sessionId} every 300ms
   - Web frontend reads /streams/{sessionId} via onValue
6. Go updates command status → "done" (with result) or "error"
```

### Presence Heartbeats

```
Two-level heartbeats, both via RTDB PATCH every 30 seconds:

Client level:  users/{uid}/clients/{clientId}/presence
               { online: true, last_seen: <ms> }

Instance level: users/{uid}/instances/{id}/runtime
                { online: true, client_id: <id>, last_seen: <ms> }

On shutdown: all entries marked online: false
On instance start: immediate heartbeat for new instance
On instance stop: immediate offline mark
```

## Security

### Firestore Rules

```
match /users/{uid}/{document=**} {
  allow read, write: if request.auth != null && request.auth.uid == uid;
}
```

Every user can only read/write documents under their own `users/{uid}/` subtree.

### RTDB Rules

```json
{
  "users": {
    "$uid": {
      ".read": "auth != null && auth.uid === $uid",
      ".write": "auth != null && auth.uid === $uid"
    }
  },
  "link_codes": {
    "$code": {
      ".read": "auth != null",
      ".write": "auth != null"
    }
  }
}
```

Same user-scoping pattern. `link_codes` is accessible to any authenticated user (used for optional Telegram account linking).

### Authentication

The Go process authenticates via the Firebase REST API as a regular user. Two modes:

- **Refresh token** (default, from browser login) -- uses `securetoken.googleapis.com/v1/token` to exchange for ID tokens
- **Email/password** -- uses `identitytoolkit.googleapis.com/v1/accounts:signInWithPassword`

ID tokens are cached and auto-refreshed 5 minutes before expiry (tokens expire after 3600 seconds).

## Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/go-telegram/bot` | v1.19 | Telegram Bot API client |
| `github.com/google/uuid` | v1.6 | UUID generation for instance and client IDs |
| `github.com/yuin/goldmark` | v1.5 | Markdown to HTML conversion |
| `gopkg.in/yaml.v3` | v3.0 | YAML parsing for credentials.yaml |

No CGo required. The binary is fully statically linkable.
