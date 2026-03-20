# Architecture v2 — Design Document

> Status: DRAFT — needs review before implementation.
> This replaces the previous architecture docs which incorrectly treated Go as a backend server.

## Core Insight

**Go is a CLIENT, not a backend.** Firebase is the backend.

```
User A                              User B
├── Go Client 1 ──┐                ├── Go Client 1 ──┐
├── Go Client 2 ──┤                ├── Go Client 2 ──┤
├── Go Client 3 ──┼──► Firebase ◄──┼── Go Client 3 ──┤
└── Web Dashboard ─┘    (backend)  └── Web Dashboard ─┘
```

- Each **user** has a Firebase Auth account (email/password or Google login).
- Each user may run **many Go clients** (one per machine, or several on the same machine).
- Each user has **one web dashboard** to manage all their Go clients.
- There can be **many users**, each completely isolated from each other.
- Go clients and web dashboards are both **untrusted clients** — Firebase security rules enforce isolation.

## Authentication

Go has no official Firebase Client SDK. The only option is the **Firebase REST API** with user-level authentication.

- **Web frontend**: Firebase JS SDK (standard client auth)
- **Go client**: Firebase REST API with ID token (`?auth=<token>` for RTDB, `Bearer <token>` for Firestore)
- **Auth modes for Go**:
  - Email/password — Go signs in via `identitytoolkit.googleapis.com`
  - Refresh token — from browser-based login flow (user runs `login` command, browser opens, token saved to `credentials.yaml`)
- **Token lifecycle**: ID tokens expire in ~60 minutes, Go refreshes via `securetoken.googleapis.com`

The Firebase Admin SDK (service account) is **NOT appropriate** because:
- Go clients are untrusted — User A's Go client must not access User B's data
- Admin SDK bypasses security rules
- Every Go client would need a copy of the service account key

## Data Ownership

Every piece of data belongs to a specific user, identified by their Firebase Auth UID.

A user's Go clients manage **instances** (AI coding sessions). Each instance belongs to one user. A Go client may manage multiple instances. Multiple Go clients from the same user see the same instances.

```
User (Firebase Auth UID)
└── Instance 1 (running on Go Client A)
│   ├── Session 1
│   │   └── Messages (conversation history)
│   └── Session 2
│       └── Messages
└── Instance 2 (running on Go Client B)
    └── Session 1
        └── Messages
```

## Data Model

### Option A: Firestore Only (recommended)

Eliminate RTDB entirely. Use Firestore for everything — persistent data, commands, streaming, presence. Firestore `onSnapshot` provides real-time listeners.

**Why:**
- One database instead of two — simpler architecture
- Firestore has document-level security rules — easy to scope per user
- RTDB's current data model is broken for multi-user (e.g., `/instances` is global, Go clients overwrite each other)
- Firestore `onSnapshot` handles the real-time needs (commands, streaming, presence)
- At personal-use scale, Firestore pricing for high-frequency updates (streaming at 300ms) is negligible

**Collections:**

```
/users/{uid}
    display_name: string
    telegram_id: number | null          # optional Telegram link
    created_at: timestamp

/users/{uid}/instances/{instanceId}
    id: string
    name: string
    directory: string
    status: "running" | "stopped" | "starting" | "failed"
    provider_type: "claudecode" | "opencode"
    port: number
    password: string
    auto_start: boolean
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/instances/{instanceId}/sessions/{sessionId}
    id: string
    title: string
    worktree_path: string
    branch: string
    message_count: number
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/instances/{instanceId}/sessions/{sessionId}/messages/{messageId}
    id: string
    role: "user" | "assistant"
    content: string
    tool_calls: [{name, status, detail, input, output}]
    created_at: timestamp

/users/{uid}/presence/{instanceId}
    online: boolean
    last_seen: timestamp

/users/{uid}/streams/{sessionId}
    content: string                     # accumulated text
    status: "streaming" | "complete" | "error"
    tool_calls: [{name, status, detail}]
    error: string | null
    updated_at: timestamp

/users/{uid}/commands/{instanceId}/{commandId}
    action: "start" | "stop" | "delete" | "prompt" | "create_session" | "list_sessions"
    payload: map
    status: "pending" | "ack" | "done" | "error"
    result: map | null
    error: string | null
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/config
    telegram_token: string
    telegram_allowed_users: string
    process_claudecode_binary: string
    process_opencode_binary: string
    ...
```

All data is nested under `/users/{uid}/`. This makes security rules trivial.

### Option B: RTDB for Ephemeral + Firestore for Persistent

Keep RTDB for high-frequency ephemeral data (streams, presence), use Firestore for everything else.

```
RTDB:
/users/{uid}/streams/{sessionId}        # 300ms updates
/users/{uid}/presence/{instanceId}      # 30s heartbeats
/users/{uid}/commands/{instanceId}/{cmd} # command queue

Firestore:
/users/{uid}/instances/{instanceId}     # persistent
/users/{uid}/instances/.../sessions/... # persistent
/users/{uid}/instances/.../messages/... # persistent
/users/{uid}/config                     # persistent
```

**Pros:** RTDB is cheaper for bandwidth-heavy ephemeral data.
**Cons:** Two databases, two sets of security rules, more complexity.

## Security Rules

### Firestore (Option A)

```javascript
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    // Users can only access their own data tree.
    match /users/{uid}/{document=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
```

One rule covers everything because all data is nested under `/users/{uid}/`.

### RTDB (Option B, if kept)

```json
{
  "rules": {
    "users": {
      "$uid": {
        ".read": "auth != null && auth.uid === $uid",
        ".write": "auth != null && auth.uid === $uid"
      }
    }
  }
}
```

### Telegram Link Codes (special case)

Link codes need to be writable by one user and readable by any Go client (to verify the code). This is a cross-user operation.

```
/link_codes/{code}
    uid: string
    expires: number
```

Security rules:
- Any authenticated user can write (to create a code)
- Any authenticated user can read (Go client verifies the code)
- This is acceptable because codes are short-lived (10 minutes) and random

## Communication Flows

### 1. Go Client Startup

```
Go Client starts
  → Reads credentials.yaml (Firebase API key + refresh token)
  → Signs in to Firebase Auth (gets ID token)
  → Reads /users/{uid}/config (gets Telegram token, binary paths, etc.)
  → If no config: waits for web frontend to set it (onSnapshot listener)
  → Starts instances from /users/{uid}/instances where status=running or auto_start=true
  → Starts presence heartbeats (/users/{uid}/presence/{instanceId})
  → Starts command listener (/users/{uid}/commands/ via onSnapshot)
  → Starts Telegram bot (if configured)
```

### 2. Web Frontend → Go Client (commands)

```
Web: write /users/{uid}/commands/{instanceId}/{cmdId}
    {action: "prompt", payload: {session_id, content}, status: "pending"}
      │
Go:   onSnapshot detects new "pending" command
      → Update status → "ack"
      → Execute (e.g., send prompt to Claude Code)
      → Update status → "done" (with result) or "error"
      │
Web:  onSnapshot detects status change → update UI
```

### 3. Streaming (Go → Web)

```
Go: Provider.Prompt() → event channel
    → Buffer events every 300ms
    → Write to /users/{uid}/streams/{sessionId}
      {content: "accumulated text", status: "streaming", tool_calls: [...]}
      │
Web:  onSnapshot(/users/{uid}/streams/{sessionId})
      → Re-render content in real-time
      │
Go:   On completion:
      → Update stream status → "complete"
      → Save full message to /users/{uid}/instances/.../sessions/.../messages/
```

### 4. Presence (Go → Web)

```
Go: Every 30 seconds
    → Write /users/{uid}/presence/{instanceId} = {online: true, last_seen: now}
    → On shutdown: mark offline

Web: onSnapshot(/users/{uid}/presence/{instanceId})
    → Show green/red dot per instance
```

### 5. Instance Sync

With Firestore-only (Option A), there is no separate "sync" step. Go clients write instance data directly to Firestore when instances are created/started/stopped. Web frontend reads with `onSnapshot` for real-time updates.

This is different from the current design where Go periodically overwrites `/instances` — that approach is broken for multi-user.

## Go Client Identity

Each Go client authenticates as the user who ran `login`. Multiple Go clients from the same user share the same Firebase UID and see the same data.

**Question:** If two Go clients from the same user both manage instances, how do they avoid conflicts?

- Each Go client manages its own set of instances (different machines, different projects)
- Instance IDs are UUIDs — no collisions
- Instance data is written on create/status change, not periodically overwritten
- Commands are addressed to specific instance IDs — only the Go client running that instance responds
- Presence heartbeats are per-instance, so each Go client beats for its own instances

## Open Questions

1. **Config per-user or per-client?** Currently config is shared per user. If User A has two machines with different binary paths, they'd need per-client config. Should config be `/users/{uid}/config` (shared) or `/users/{uid}/clients/{clientId}/config`?

2. **Telegram bot — one per user or shared?** Each user configures their own Telegram bot token. Two Go clients from the same user would both try to run the same Telegram bot — that would conflict. Should only one Go client per user run the Telegram bot?

3. **Instance ownership — user-level or client-level?** If User A has Go Client on Machine-1 and Machine-2, should Machine-2's web dashboard see Machine-1's instances? Currently yes (same uid). But Machine-2 can't actually control Machine-1's instances unless Machine-1's Go client is running.

4. **RTDB vs Firestore-only?** Option A (Firestore-only) is simpler. Option B (keep RTDB for ephemeral data) is cheaper at scale. For personal use, Option A is recommended.

5. **Telegram user state** — `user_state` maps Telegram user ID → active instance/session. This is per-Telegram-user, not per-Firebase-user. Should it be under `/users/{uid}/user_state/` or a top-level collection with its own rules?

6. **Message sessions** — maps Telegram message ID → session ID (for reply-to-continue). Same scoping question as user_state.
