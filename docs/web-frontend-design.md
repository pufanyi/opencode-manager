# Web Frontend Communication Architecture

## Overview

The web frontend communicates with the Go server entirely through **Firebase Realtime Database (RTDB)**. Both sides are "clients" that make outbound HTTPS connections to Firebase — there is no direct connection between them.

**Key property:** No public IP, no port forwarding, no tunnels. The Go server only makes outbound HTTPS requests to Firebase.

## Architecture

```
┌────────────────┐           ┌──────────────────────────┐           ┌────────────────┐
│   Go Server    │──writes──→│        Firebase           │←─listens─│   Web Frontend │
│  (internal     │           │                           │           │   (Vercel /    │
│   network)     │←─listens─│  ┌─────────────────────┐  │──writes──→│   static host) │
│                │           │  │ Auth                │  │           │                │
│ No public IP   │           │  │ (login/register)    │  │           │ No backend     │
│ No open ports  │           │  ├─────────────────────┤  │           │ Pure SPA       │
│                │           │  │ Realtime Database   │  │           │                │
│ Components:    │           │  │ (ALL data:          │  │           │ Components:    │
│ - Process Mgr  │           │  │  instances, streams │  │           │ - Login page   │
│ - TG Bot       │           │  │  commands, presence │  │           │ - Dashboard    │
│ - Firebase REST│           │  │  config)            │  │           │ - Instance mgr │
│                │           │  └─────────────────────┘  │           │ - Session view │
│                │           │                           │           │ - Prompt panel │
└────────────────┘           └──────────────────────────┘           └────────────────┘
```

## Firebase Services Used

**Only two services are used in the actual implementation:**

| Service           | Purpose                        | Go Side                            | Web Side             |
|-------------------|--------------------------------|------------------------------------|----------------------|
| Auth              | User login/registration        | REST API (`identitytoolkit.googleapis.com`) | `firebase/auth` JS SDK |
| Realtime Database | All data exchange              | REST API (`{db}.firebaseio.com`)   | `firebase/database` JS SDK |

**Note:** The original design planned to use Firestore for persistent data (instances, sessions, history). This was **not implemented** — all data goes through RTDB only. Go authenticates as a regular Firebase user via REST API, not via the Admin SDK or a service account.

## Authentication

Go signs in as a regular Firebase user, not an admin:

```
Go Server                                Firebase Auth REST API
    │                                          │
    ├── POST identitytoolkit.googleapis.com ──►│  (email/password)
    │   /v1/accounts:signInWithPassword        │
    │◄──────── idToken + refreshToken ─────────┤
    │                                          │
    │  (token used as ?auth=<idToken> on       │
    │   all RTDB requests)                     │
    │                                          │
    ├── POST securetoken.googleapis.com ──────►│  (token refresh)
    │   /v1/token                              │
    │◄──────── new idToken ────────────────────┤
```

Token refresh happens automatically when the token is within 5 minutes of expiry (tokens last ~60 minutes).

Two auth modes are supported:
- **Email/Password** — for dedicated service accounts
- **Refresh Token** — from browser-based login (Google, etc.)

## RTDB Data Model

All data lives in RTDB. There is no Firestore.

```
/instances/{instanceId}              ← Written by Go (sync.go every 2s)
    id: string                          Read by web (onValue listener)
    name: string
    directory: string
    status: "running" | "stopped" | "starting" | "failed"
    provider_type: "claudecode" | "opencode"

/presence/{instanceId}               ← Written by Go (presence.go every 30s)
    online: boolean                     Read by web (onValue listener)
    last_seen: number                   // Unix ms

/streams/{sessionId}                 ← Written by Go (streamer.go every 300ms)
    content: string                     Read by web (onValue listener)
    status: "streaming" | "complete" | "error"
    tool_calls: [
        { name: string, status: string, detail: string }
    ]
    error: string | null
    updated_at: number                  // Unix ms

/commands/{instanceId}/{commandId}   ← Written by web (push + set)
    action: "start" | "stop" | "delete" | "prompt" | "create_session" | "list_sessions"
    payload: object
    status: "pending" | "ack" | "done" | "error"
    user_id: string                     // Firebase Auth UID
    created_at: number
    updated_at: number
    result: object | null               // Set by Go on success
    error: string | null                // Set by Go on failure

/config                              ← Read/written by both
    telegram_token, binary paths, etc.
    Go can wait for config via SSE (WaitForConfig)
```

### Optional: Telegram Account Linking

Only used when Telegram bot integration is enabled:

```
/link_codes/{code}                   ← Written by web, read+deleted by Go
    uid: string                         6-digit code, 10-minute expiry
    expires: number

/users/{uid}/telegram_id             ← Written by Go, read by web
    number                              Telegram user ID
```

## Data Flow

### 1. Instance Sync (Go → Web)

Go server polls its local process manager every 2 seconds and PUTs the full instance list to `/instances`:

```go
// sync.go — syncInstances()
instances := lister()
data := make(map[string]interface{}, len(instances))
for _, inst := range instances {
    data[inst.ID] = map[string]interface{}{...}
}
c.RTDB.Set(ctx, "instances", data)  // PUT — replaces entire /instances
```

Web frontend reads with `onValue`:

```typescript
// firebase.service.ts — onInstances()
const dbRef = ref(this.db, "instances");
onValue(dbRef, (snapshot) => {
    const instances = Object.values(snapshot.val() || {});
    callback(instances);
});
```

### 2. Presence Heartbeat (Go → Web)

Every 30 seconds, Go PATCHes `/presence/{instanceId}`:

```go
// presence.go — heartbeat()
rtdb.Update(ctx, "presence/"+id, map[string]interface{}{
    "online": true, "last_seen": time.Now().UnixMilli(),
})
```

On shutdown, marks all instances offline. Web shows green/red status indicators.

### 3. Commands (Web → Go)

Web pushes a new command node; Go listens via SSE:

```
Web: push(/commands/{instanceId})
  → { action: "prompt", payload: {session_id, content}, status: "pending" }

Go (SSE on /commands):
  → Detects new "pending" command
  → PATCH status → "ack"
  → Execute handler
  → PATCH status → "done" (with result) or "error"

Web: onValue sees status change → resolves promise
```

`sendCommandAndWait()` polls via `onValue` with a 30-second timeout.

### 4. Real-time Streaming (Go → Web)

`Streamer.WrapEvents()` wraps a provider event channel, buffering to RTDB:

```
Provider.Prompt() → event channel
    → Streamer intercepts events
    → Buffers text + tool_calls in memory
    → Flushes to /streams/{sessionId} every 300ms (PATCH)
    → On done/error: final PATCH with terminal status

Web: onValue(/streams/{sessionId})
    → Re-renders content on each update
```

### 5. Remote Config (Bidirectional)

Go can boot with only Firebase credentials and pull remaining config (Telegram token, binary paths) from `/config`. If no config exists, `WaitForConfig()` blocks on SSE until the web frontend sets it.

## Go Implementation

### Package: `internal/firebase`

| File | Responsibility |
|------|---------------|
| `client.go` | Client initialization, ties together all components |
| `auth.go` | Firebase Auth REST API: SignIn, SignInWithRefreshToken, Token (with auto-refresh) |
| `rtdb.go` | RTDB REST client: Get, Set, Update, Delete, Listen (SSE with auto-reconnect) |
| `sync.go` | Periodic instance list sync to `/instances` (every 2s) |
| `presence.go` | Periodic heartbeat to `/presence` (every 30s), offline marking on shutdown |
| `streamer.go` | Wraps provider event channels, buffers + flushes to `/streams` (every 300ms) |
| `commands.go` | SSE listener on `/commands`, dispatches to handler, updates status |
| `config_sync.go` | Pull/Push/WaitForConfig on `/config` |

### Sync Strategy

SQLite remains the local source of truth. Firebase is a synced view for the web frontend:

```
Local SQLite (source of truth for Go server)
        │
        │  Periodic sync (every 2s):
        ▼
RTDB /instances  (read-only copy for web frontend)
```

## Web Frontend

### Tech Stack

| Layer      | Choice                         | Reason                    |
|------------|--------------------------------|---------------------------|
| Framework  | Angular                        | Already exists            |
| Hosting    | Vercel or Firebase Hosting     | Free static hosting       |
| Auth       | Firebase Auth JS SDK           | Official, full-featured   |
| Data       | Firebase RTDB JS SDK           | Real-time listeners       |
| Styling    | Existing                       | No need to change         |

### Key Service: `firebase.service.ts`

All Firebase interaction is centralized in `FirebaseService`:

- **Auth**: login, register, loginWithGoogle, logout
- **Listeners**: onInstances, onPresence, onStream, onCommandResult (all via `onValue`)
- **Commands**: sendCommand (fire-and-forget), sendCommandAndWait (with 30s timeout)
- **Optional**: generateLinkCode, onUserLinkStatus (for Telegram linking)

### Pages

```
/login              → Email/password + Google login
/dashboard          → Instance list with online/offline indicators
/instance/:id       → Instance detail: sessions, controls (start/stop/delete)
/session/:id        → Real-time streaming view (token-by-token rendering)
```

## RTDB Security Rules

```json
{
  "rules": {
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
    },
    "config": {
      ".read": "auth != null",
      ".write": "auth != null"
    },
    "link_codes": {
      ".read": "auth != null",
      ".write": "auth != null"
    },
    "users": {
      ".read": "auth != null",
      ".write": "auth != null"
    }
  }
}
```

Note: Go signs in as a regular user, so security rules apply to both sides equally.

## Cost Estimate (Personal Use)

| Resource            | Monthly Usage (est.) | Free Tier   | Cost  |
|---------------------|---------------------|-------------|-------|
| Firebase Auth       | <10 users           | Unlimited   | $0    |
| RTDB bandwidth      | ~5 GB/month         | 10 GB/month | $0    |
| RTDB storage        | ~5 MB (ephemeral)   | 1 GB        | $0    |
| Web hosting         | Static files        | Free (Vercel/Firebase) | $0 |
| **Total**           |                     |             | **$0** |
