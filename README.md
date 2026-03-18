# OpenCode Manager

A single-binary tool that manages multiple [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and [OpenCode](https://github.com/sst/opencode) instances on one server, controlled entirely via a Telegram bot. Run AI coding sessions across different projects from your phone.

## Features

- **Dual provider support** — Run both Claude Code (CLI) and OpenCode (HTTP) instances side by side
- **Telegram interface** — Create, start, stop, switch, and prompt instances from any Telegram client
- **Active Tasks board** — Live status dashboard in Telegram showing all running tasks with tool progress, elapsed time, and stop buttons
- **Git worktree isolation** — Each session can run in its own git worktree for parallel, conflict-free work
- **Auto-merge** — Worktree branches are automatically merged back to main after each prompt; merge conflicts trigger a "Fix with Claude" button
- **Reply-to-continue** — Reply to any bot response to continue that session, even if you've switched away
- **Photo support** — Send images to Claude Code for visual analysis directly from Telegram
- **Crash recovery** — Auto-restarts crashed instances with exponential backoff and notifies you on permanent failure
- **Persistent state** — SQLite database tracks instances, sessions, and per-user context across restarts
- **Web dashboard** — Optional Angular-based web UI with real-time streaming via SSE
- **Single binary** — Built-in setup wizard, embedded web frontend, no external scripts needed

## Quick Start

```bash
# Build (requires Node.js/pnpm for frontend + Go 1.24+)
make build

# Interactive setup (generates opencode-manager.yaml)
./bin/opencode-manager setup

# Run
./bin/opencode-manager -config opencode-manager.yaml
```

### Prerequisites

- Go 1.24+
- Node.js 22+ and pnpm (for building the web dashboard)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and/or [OpenCode](https://github.com/sst/opencode) installed and available in `$PATH`
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- Your Telegram user ID (send `/start` to [@userinfobot](https://t.me/userinfobot))

## Setup Wizard

The built-in wizard walks you through configuration step by step:

```
$ opencode-manager setup

  ┌──────────────────────────────────────┐
  │     OpenCode Manager Setup Wizard    │
  └──────────────────────────────────────┘

Step 1/7: Telegram Bot Token
Step 2/7: Allowed Telegram User IDs
Step 3/7: OpenCode Binary Path
Step 4/7: Claude Code Binary Path
Step 5/7: Port Range
Step 6/7: Database Path
Step 7/7: Pre-register Projects (optional)
```

It generates `opencode-manager.yaml` with proper permissions (`0600`).

You can also specify the output path:

```bash
opencode-manager setup -output ~/.config/opencode-manager/opencode-manager.yaml
```

## Configuration

The config file can be placed in any of these locations (checked in order):

1. Path passed via `-config` flag
2. `OPENCODE_MANAGER_CONFIG` environment variable
3. `./opencode-manager.yaml`
4. `./configs/opencode-manager.yaml`
5. `~/.config/opencode-manager/opencode-manager.yaml`

See [`configs/opencode-manager.example.yaml`](configs/opencode-manager.example.yaml) for a full example.

### Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `TELEGRAM_TOKEN` | `telegram.token` |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` (comma-separated) |
| `OPENCODE_BINARY` | `process.opencode_binary` |
| `CLAUDECODE_BINARY` | `process.claudecode_binary` |
| `STORAGE_DATABASE` | `storage.database` |

## Telegram Commands

### Instance Management

| Command | Description |
|---------|-------------|
| `/new <name> <path>` | Create & start a new Claude Code instance |
| `/newopencode <name> <path>` | Create & start a new OpenCode instance |
| `/list` | List all instances with status and provider type |
| `/switch <name>` | Switch your active instance |
| `/start_inst <name>` | Start a stopped instance |
| `/stop [name]` | Stop an instance (active if no name given) |

### Session & Prompting

| Command | Description |
|---------|-------------|
| `/session new` | Create a new session (prompts for worktree choice in git repos) |
| `/session` | Show current session info (including branch if worktree) |
| `/sessions` | List all sessions (tap to switch, 🗑 to delete) |
| `/abort` | Abort the running prompt |
| _any text_ | Send as a prompt to the active instance |
| _photo_ | Download image and send to Claude Code for analysis |
| _reply to bot message_ | Continue that session with a new prompt |

### General

| Command | Description |
|---------|-------------|
| `/start` | Welcome message and quick-start guide |
| `/help` | Show all commands |
| `/status` | Active instance, provider, session, and connection details |

## How It Works

```
You (Telegram) ──→ Bot ──→ OpenCode Manager ──→ Claude Code (CLI) or OpenCode (HTTP)
       ↑                                              │
       └──────── streaming events ◄───────────────────┘
```

1. The manager spawns provider processes: `claude -p` per prompt (Claude Code) or `opencode serve` as a persistent server (OpenCode)
2. When you send a text or photo message, it's forwarded as a prompt to your active instance
3. For git repos, you're asked whether to work in a **new worktree** (isolated branch) or the **main directory**
4. The provider streams its response back via JSON streaming (Claude Code) or SSE (OpenCode)
5. An **Active Tasks board** in Telegram shows real-time tool progress for all running prompts
6. When the prompt completes, the final response is sent as a reply to your original message
7. Worktree branches are **auto-merged** back to main; merge conflicts offer a "Fix with Claude" button

### Provider Types

**Claude Code** (default) — Spawns `claude -p` per prompt with JSON streaming output. No persistent server process. Sessions tracked in SQLite with `--resume` support. Supports git worktree isolation for concurrent sessions.

**OpenCode** — Runs `opencode serve` as a persistent HTTP server per instance. Each instance gets a dedicated port and Basic Auth credentials. Communicates via REST API + SSE.

### Git Worktree Isolation

When a Claude Code instance is in a git repository, each new session can optionally run in its own git worktree:

- **Parallel work** — Multiple sessions can edit different files without conflicting
- **Auto-merge** — After each prompt completes, the session branch is merged back to the main branch
- **Cross-sync** — After a merge, other active worktrees are rebased onto the updated main branch
- **Conflict handling** — If auto-merge fails, a notification with a "🔧 Fix with Claude" button creates a new session to resolve the conflict
- **Cleanup** — Deleting a session removes its worktree and branch

### Active Tasks Board

While prompts are running, a live status board appears at the bottom of the Telegram chat:

- Shows each running task with instance name, session title, elapsed time
- Displays tool invocations with status icons (⏳ running, ✅ done, ❌ error) and details
- "Stop #N" buttons to cancel individual tasks
- Refreshes at a configurable interval (default 2s via `telegram.board_interval`)
- Auto-repositions to the bottom when new messages appear
- Disappears when all tasks complete

### Crash Recovery

When an instance crashes unexpectedly:

1. The manager detects the exit via process monitoring
2. Restarts with exponential backoff (1s, 2s, 4s, ...)
3. After 3 failures (configurable), marks as permanently failed
4. Sends a Telegram notification to all authorized users

### Response Delivery

- **Final reply** — Completed responses are sent as a reply to the original user message
- **Message splitting** — Auto-splits at 4096 characters (Telegram's limit)
- **File fallback** — Sends as a `.md` file if the response exceeds ~12,000 characters
- **HTML rendering** — Markdown converted to Telegram HTML with tag balancing and safe truncation
- **Reply-to-continue** — Reply to any bot response to resume that session, even after switching

### Web Dashboard

When enabled (`web.enabled: true`), an Angular-based dashboard is served at the configured address (default `:8080`). It provides:

- Instance listing and management (create, start, stop, delete)
- Session management per instance
- Real-time prompt streaming via SSE (`/api/ws`)

## Architecture

```
cmd/opencode-manager/
└── main.go                  Entry point, subcommand routing, signal handling

internal/
├── setup/setup.go           Interactive 7-step setup wizard
├── config/config.go         YAML config + env overrides + validation
├── store/
│   ├── store.go             SQLite connection (WAL mode, pure Go)
│   ├── migrations.go        Schema: instances, user_state, claude_sessions, message_sessions
│   ├── instance.go          Instance CRUD
│   ├── userstate.go         Per-user active instance/session tracking
│   └── message_session.go   Telegram message → session mapping (reply-to-continue)
├── process/
│   ├── portpool.go          Thread-safe port allocation
│   ├── instance.go          Instance state wrapper
│   └── manager.go           Orchestrator: create, health check, crash recovery
├── provider/
│   ├── provider.go          Provider interface (abstraction layer)
│   ├── claudecode.go        Claude Code CLI + worktree + auto-merge implementation
│   └── opencode.go          OpenCode HTTP+SSE implementation
├── opencode/
│   ├── types.go             API types (Session, Message, SSEEvent, etc.)
│   ├── client.go            HTTP REST client (sessions, prompts, abort)
│   └── sse.go               SSE subscriber with auto-reconnect
├── bot/
│   ├── bot.go               Telegram bot setup, auth middleware
│   ├── handlers.go          Command handlers + prompt/photo forwarding + worktree choice
│   ├── callbacks.go         Inline keyboard callback handlers (incl. merge fix, session delete)
│   ├── keyboard.go          Inline keyboard builders (incl. worktree choice)
│   ├── streaming.go         Active Tasks board + response delivery + merge-back
│   └── format.go            Markdown→Telegram HTML, tag balancing, utilities
├── gitops/
│   └── merge.go             Git worktree merge-back (fast-forward + linked worktree)
├── web/
│   ├── server.go            Web dashboard HTTP server + SSE streaming hub
│   └── dist/                Embedded Angular build artifacts
└── app/app.go               Application orchestrator wiring everything together

web/                         Angular 19.2 frontend source
```

## Building

```bash
# Build frontend + binary
make build

# Build and run
make run

# Dev mode (rebuild and run)
make dev

# Lint (Go + frontend)
make lint

# Clean build artifacts
make clean
```

## License

MIT
