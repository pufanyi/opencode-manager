# Configuration Reference

## Overview

OpenCode Manager stores all configuration in Firebase Firestore, split into two scopes:

- **User config** -- shared across all Go clients for the same Firebase user (Telegram settings, web dashboard)
- **Client config** -- per-machine settings (binary paths, port ranges)

The only local file is `credentials.yaml`, which contains Firebase connection info and the client's unique ID.

## Setup

Run the interactive login command to configure everything:

```bash
opencode-manager login
```

This performs four steps:

1. **Sign in to Firebase** -- opens a browser for Google/email authentication
2. **Telegram Bot** -- prompts for bot token and allowed user IDs
3. **AI Coding Tools** -- detects and confirms Claude Code and OpenCode binary paths
4. **Save** -- writes `credentials.yaml` locally and pushes config to Firestore

On subsequent machines, run `login` again with the same Firebase account. User-level config (Telegram settings) is shared automatically; client-level config (binary paths) is per-machine.

To refresh an expired browser credential without reconfiguring:

```bash
opencode-manager relogin
```

## credentials.yaml

The only local file. Contains Firebase connection info and a unique client ID. Created by `login` with `0600` permissions.

```yaml
firebase:
  api_key: "AIzaSy..."
  database_url: "https://your-project-default-rtdb.firebaseio.com"
  auth_domain: "your-project.firebaseapp.com"
  project_id: "your-project"
  refresh_token: "AMf-vBx..."    # from browser login
client_id: "a1b2c3d4-..."        # auto-generated UUID
```

| Field | Required | Description |
|-------|----------|-------------|
| `firebase.api_key` | Yes | Firebase web API key |
| `firebase.database_url` | Yes | Firebase Realtime Database URL |
| `firebase.auth_domain` | No | Firebase auth domain (derived from project_id if omitted) |
| `firebase.project_id` | No | Firebase project ID (derived from database_url if omitted) |
| `firebase.refresh_token` | * | Refresh token from browser login |
| `firebase.email` | * | Email for email/password auth mode |
| `firebase.password` | * | Password for email/password auth mode |
| `client_id` | No | Unique ID for this Go process (auto-generated on first run) |

\* Either `refresh_token` or `email`+`password` must be provided.

CLI flags for the `login` command:

```
--credentials <path>    Path to credentials file (default: ./credentials.yaml)
--api-key <key>         Firebase web API key
--database-url <url>    Firebase RTDB URL
--auth-domain <domain>  Firebase auth domain
--project-id <id>       Firebase project ID
```

If flags are omitted, the login command uses the built-in default Firebase project (`opencode-manager`).

## User Config (Firestore)

Stored at `users/{uid}/config/user`. Shared across all Go clients for the same Firebase user.

### Telegram Settings

| Key | Type | Required | Default | Description |
|-----|------|----------|---------|-------------|
| `telegram.token` | string | Yes | -- | Bot token from [@BotFather](https://t.me/BotFather) |
| `telegram.allowed_users` | string | Yes | -- | Comma-separated Telegram user IDs authorized to use the bot |
| `telegram.board_interval` | duration | No | `2s` | Refresh interval for the Active Tasks status board |

Only users in `telegram.allowed_users` can interact with the bot. All other messages are silently ignored.

The `telegram.board_interval` controls how often the Active Tasks board updates in Telegram. Lower values give more responsive tool progress but use more API calls. Values below 1s are not recommended due to Telegram rate limits.

### Web Dashboard Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `web.enabled` | string | `"false"` | Enable the embedded web dashboard (`"true"` or `"false"`) |
| `web.addr` | string | `":8080"` | Listen address for the HTTP server |

When enabled, an Angular-based web dashboard is served with REST API endpoints and SSE streaming.

## Client Config (Firestore)

Stored at `users/{uid}/config/clients/{clientId}`. Each Go client machine has its own settings.

### Process Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `process.opencode_binary` | string | `"opencode"` | Path to the OpenCode binary (or name for `$PATH` lookup) |
| `process.claudecode_binary` | string | `"claude"` | Path to the Claude Code binary (or name for `$PATH` lookup) |
| `process.port_range_start` | int | `14096` | First port in the allocation range |
| `process.port_range_end` | int | `14196` | Last port (exclusive), giving 100 slots by default |
| `process.health_check_interval` | duration | `30s` | How often to ping running instances |
| `process.max_restart_attempts` | int | `3` | Consecutive crash restarts before giving up |

Port range must be within 1024-65535 and `start < end`. Ports are only used by OpenCode instances; Claude Code instances do not require port allocation.

## Environment Variable Overrides

Environment variables take precedence over Firestore config. Applied after config is loaded from Firestore.

| Variable | Config Key | Description |
|----------|-----------|-------------|
| `TELEGRAM_TOKEN` | `telegram.token` | Bot token |
| `TELEGRAM_ALLOWED_USERS` | `telegram.allowed_users` | Comma-separated user IDs |
| `OPENCODE_BINARY` | `process.opencode_binary` | OpenCode binary path |
| `CLAUDECODE_BINARY` | `process.claudecode_binary` | Claude Code binary path |
| `FIREBASE_API_KEY` | `firebase.api_key` | Firebase web API key |
| `FIREBASE_DATABASE_URL` | `firebase.database_url` | Firebase RTDB URL |
| `FIREBASE_PROJECT_ID` | `firebase.project_id` | Firebase project ID |
| `FIREBASE_EMAIL` | `firebase.email` | Email for email/password auth |
| `FIREBASE_PASSWORD` | `firebase.password` | Password for email/password auth |
| `FIREBASE_ENABLED` | `firebase.enabled` | Set to `"true"` to enable Firebase |

Example:

```bash
TELEGRAM_TOKEN="123:ABC" CLAUDECODE_BINARY="/usr/local/bin/claude" opencode-manager
```

## Config Migration

On first boot after upgrading from a pre-Firestore version, the manager automatically detects legacy config stored in RTDB at the flat `/config` path and migrates it to the Firestore user/client config documents. The migrated keys are:

- User-level: `telegram.token`, `telegram.allowed_users`, `telegram.board_interval`, `web.enabled`, `web.addr`
- Client-level: `process.opencode_binary`, `process.claudecode_binary`, `process.port_range_start`, `process.port_range_end`, `process.health_check_interval`, `process.max_restart_attempts`

This migration happens once. After migration, config is read exclusively from Firestore.

## Validation

The following validation rules are enforced at startup:

- `telegram.token` must be non-empty
- `telegram.allowed_users` must contain at least one user ID
- `process.port_range_start` must be less than `process.port_range_end`
- Both port values must be within 1024-65535

If validation fails, the manager exits with an error message.

## Security Notes

- `credentials.yaml` contains your Firebase refresh token. The `login` command writes it with `0600` permissions (owner read/write only).
- Each OpenCode instance gets a unique random password for HTTP Basic Auth. Passwords are stored in Firestore.
- Only users in `telegram.allowed_users` can control the bot.
- All OpenCode instances bind to `127.0.0.1` (localhost only) -- they are not exposed to the network.
- Claude Code instances do not require network ports; they communicate via stdin/stdout.
- Firestore security rules ensure each user can only access data under their own `users/{uid}/` path.
