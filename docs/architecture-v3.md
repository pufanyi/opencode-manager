# Architecture v3 - Design Document

> Status: DRAFT
> Purpose: Replace both `architecture.md` and `architecture-v2.md` with a design that matches the real platform constraints.

## Executive Summary

This system has three hard constraints:

1. Go runs on end-user machines and must be treated as an untrusted client.
2. Go cannot rely on Firebase's official Go SDKs for user-scoped access enforced by Firebase Security Rules.
3. Real-time behavior is required for commands, presence, and streaming output.

Given those constraints, the correct architecture is:

- Firebase is the backend.
- The Go application and the web dashboard are both clients.
- Go uses Firebase Authentication REST APIs to obtain a user ID token.
- Go uses Realtime Database REST streaming for real-time state and command delivery.
- Go uses Firestore REST for persistent records and queryable history.

This design avoids the two incorrect extremes:

- Treating Go as a privileged backend with Admin SDK credentials.
- Treating Firestore REST as a full real-time transport for Go and falling back to polling.

The recommended v3 model is therefore:

- Realtime Database: control plane and ephemeral state
- Firestore: durable state and history

## Why v2 Is Not Sufficient

The v2 document fixed one major conceptual mistake: Go is not a backend. That part was correct.

However, v2 still assumed that Firestore could serve as the main real-time substrate for Go. In practice, that assumption is weak for this project:

- Go has no Firebase client SDK equivalent to the Web SDK.
- Official Go Firebase libraries are server-oriented and assume privileged credentials or IAM-based access patterns.
- Firestore REST supports user ID tokens, but the real-time listener model used by Firestore SDKs is not available as a simple REST pattern we should depend on here.
- If Go talks to Firestore over plain REST without a reliable listener mechanism, the fallback becomes polling.
- Polling is a poor fit for commands, presence, and token-by-token assistant streaming.

So the issue is not "REST is too slow." The issue is:

- REST plus polling is operationally weak for control-plane traffic.
- RTDB REST streaming is a better match for real-time client coordination.

## Design Principles

### 1. Firebase is the only backend

There is no custom server in the core architecture.

- Web dashboard is a client.
- Go runtime is a client.
- Telegram integration is executed by a Go client, not by a central backend.

### 2. Go is untrusted

Any Go process may be running on a user-controlled machine. It must not hold privileged credentials that bypass per-user authorization.

This means:

- No service account on end-user machines
- No Admin SDK for normal application data access
- No IAM-based privileged data plane on the client

### 3. User identity is the security boundary

All user-owned data must be scoped under the authenticated Firebase UID.

This keeps rules simple and makes multi-user isolation enforceable by Firebase itself.

### 4. Realtime and durable data are different concerns

The architecture should not force one storage system to do both jobs equally well.

- Realtime traffic has different latency and write-frequency needs.
- Durable records need structured history, predictable querying, and cleaner schemas.

### 5. Polling is a fallback, not the primary transport

If the design only works by polling every few seconds, it is the wrong design for this product.

Polling may still exist for recovery or watchdog scenarios, but not as the default real-time path.

## Platform Constraints

## Authentication Constraint

For Go, the supported and safe client-side model is:

- Sign in with Firebase Auth REST APIs
- Obtain Firebase ID token and refresh token
- Use the ID token for database requests
- Refresh the token periodically with Secure Token REST APIs

This preserves the end-user identity and lets Firebase Security Rules evaluate access correctly.

## SDK Constraint

The official Go Firebase libraries are not equivalent to the Web SDK client model.

In this project, that matters because we need:

- user-scoped access
- Security Rules enforcement
- no privileged backend credential on the client
- real-time coordination

Therefore:

- We do not design around Firebase Admin SDK for Go.
- We do not design around Google Cloud IAM credentials on user machines.
- We assume Go talks to Firebase using explicit HTTP clients against Firebase REST APIs.

## Realtime Constraint

The system needs low-latency updates for:

- command delivery
- command acknowledgements
- presence
- partial assistant output
- Telegram-side session state updates

Those paths should use a real-time transport. In this design, that transport is RTDB REST streaming.

## High-Level Architecture

```text
                           +----------------------+
                           |      Firebase        |
                           |                      |
                           |  Auth                |
                           |  Realtime Database   |
                           |  Firestore           |
                           +----------+-----------+
                                      ^
                                      |
                 +--------------------+--------------------+
                 |                                         |
        +--------+--------+                       +--------+--------+
        |   Web Dashboard |                       |    Go Client    |
        |   Firebase JS   |                       |   REST Clients  |
        +-----------------+                       +-----------------+
                 |                                         |
                 |                                         |
                 v                                         v
          User-driven config,                      Local instances,
          control, and viewing                     provider execution,
          of state                                 Telegram bot
```

## System Responsibilities

### Web Dashboard

The web dashboard is responsible for:

- user authentication through the Firebase Web SDK
- rendering instance/session/message state
- writing commands into the control plane
- displaying streaming output and presence
- editing user configuration
- initiating account-link flows such as Telegram link setup

### Go Client

Each Go client is responsible for:

- authenticating as the signed-in user
- loading user and client configuration
- starting and managing local instances
- listening for commands addressed to its instances
- publishing presence for the instances it owns
- publishing streaming response chunks
- writing final durable results after execution completes
- optionally running the Telegram bot

### Firebase Authentication

Firebase Authentication is responsible for:

- issuing the UID identity boundary
- producing user-scoped ID tokens
- enabling Security Rules enforcement

### Realtime Database

RTDB is the control plane. It carries:

- commands
- command status updates
- instance presence
- active stream state
- client liveness
- optional short-lived UI state that benefits from push updates

### Firestore

Firestore is the durable record. It stores:

- user profile metadata
- user and client configuration
- instance metadata
- session metadata
- persisted messages
- audit-style execution summaries if needed later

## Data Ownership Model

All durable application data belongs to a Firebase Auth user.

Within one user:

- there may be multiple Go clients
- each Go client may manage multiple instances
- each instance may contain multiple sessions
- each session may contain multiple persisted messages

The basic ownership graph is:

```text
User UID
├── Client A
│   ├── Instance A1
│   └── Instance A2
└── Client B
    └── Instance B1
```

Visibility model:

- A user can see all of their clients and instances from the dashboard.
- A client only actively manages the instances assigned to that client.
- A client must ignore commands for instances it does not own.

This client ownership field is important. It avoids the ambiguity present in v2 where multiple clients under the same user could see the same data but ownership was not explicit enough.

## Identity and Authentication Flow

## Login Options

The Go client may support two login entry paths:

### Option A: Browser-assisted login

1. User runs `login`.
2. Browser opens the web app.
3. User signs in through Firebase Auth in the browser.
4. Web app exchanges or returns a refresh token to the local Go client through a local callback or copy-paste flow.
5. Go stores the refresh token and API key in a local credentials file.

### Option B: Direct email/password login

1. User enters email and password in CLI or local TUI.
2. Go calls Firebase Auth REST endpoints.
3. Go stores the refresh token and API key locally.

Browser-assisted login is preferable if Google login must be supported.

## Token Lifecycle

Go stores:

- Firebase Web API key
- refresh token
- cached ID token
- token expiry timestamp

On each outbound request or stream setup:

1. If ID token is still valid, use it.
2. If expired or close to expiry, refresh it.
3. Retry once on authentication failure if token refresh succeeds.

## Security Model

The security model is intentionally simple:

- all user-owned data is scoped under `/users/{uid}`
- access is allowed only when `request.auth.uid == uid`

There should be as few exceptions as possible.

## Recommended Persistent Schema (Firestore)

The following schema is the recommended durable data model.

```text
/users/{uid}
    display_name: string
    telegram_id: number | null
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/clients/{clientId}
    id: string
    name: string
    hostname: string
    platform: string
    app_version: string
    status: "online" | "offline" | "degraded"
    telegram_role: "disabled" | "primary" | "standby"
    created_at: timestamp
    updated_at: timestamp
    last_seen: timestamp

/users/{uid}/clients/{clientId}/config/default
    process_claudecode_binary: string
    process_opencode_binary: string
    workspace_root: string
    telegram_enabled: boolean
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/config/default
    preferred_client_id: string | null
    telegram_token: string | null
    telegram_allowed_users: string | null
    default_provider_type: "claudecode" | "opencode"
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/instances/{instanceId}
    id: string
    client_id: string
    name: string
    directory: string
    provider_type: "claudecode" | "opencode"
    status: "running" | "stopped" | "starting" | "failed"
    auto_start: boolean
    port: number | null
    password: string | null
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/instances/{instanceId}/sessions/{sessionId}
    id: string
    title: string
    branch: string
    worktree_path: string
    message_count: number
    last_message_at: timestamp | null
    created_at: timestamp
    updated_at: timestamp

/users/{uid}/instances/{instanceId}/sessions/{sessionId}/messages/{messageId}
    id: string
    role: "user" | "assistant" | "system"
    content: string
    tool_calls: array
    provider_message_id: string | null
    created_at: timestamp
```

### Why client config is separate from user config

This solves a real problem left open in v2:

- binary paths differ by machine
- workspace roots differ by machine
- one client may run Telegram, another may not

Therefore:

- `/users/{uid}/config/default` holds user-level preferences
- `/users/{uid}/clients/{clientId}/config/default` holds machine-specific settings

This is cleaner than forcing all configuration into a single shared document.

## Recommended Realtime Schema (RTDB)

The following schema is the recommended ephemeral and control-plane model.

```text
/users/{uid}/clients/{clientId}/presence
    online: true
    status: "online" | "degraded"
    last_seen: number

/users/{uid}/instances/{instanceId}/runtime
    client_id: string
    online: boolean
    state: "idle" | "busy" | "streaming" | "error"
    last_seen: number
    current_session_id: string | null
    updated_at: number

/users/{uid}/commands/{instanceId}/{commandId}
    action: "start" | "stop" | "delete" | "prompt" | "create_session" | "list_sessions"
    payload: object
    status: "pending" | "ack" | "running" | "done" | "error" | "expired"
    issued_by: "web" | "telegram" | "client"
    created_at: number
    updated_at: number
    acked_by_client_id: string | null
    result: object | null
    error: string | null

/users/{uid}/streams/{sessionId}
    instance_id: string
    client_id: string
    status: "idle" | "streaming" | "complete" | "error"
    content: string
    tool_calls: object
    sequence: number
    updated_at: number
    error: string | null

/users/{uid}/telegram/runtime
    primary_client_id: string | null
    status: "idle" | "running" | "offline"
    updated_at: number

/users/{uid}/telegram/user_state/{telegramUserId}
    active_instance_id: string | null
    active_session_id: string | null
    updated_at: number

/users/{uid}/telegram/message_sessions/{telegramMessageId}
    instance_id: string
    session_id: string
    updated_at: number
```

## Special-Case Shared Paths

Some paths may need to exist outside `/users/{uid}`.

The main example is short-lived Telegram link codes:

```text
/link_codes/{code}
    uid: string
    created_at: number
    expires_at: number
```

This should remain a narrowly scoped exception with strict TTL behavior.

## Why RTDB Owns the Control Plane

RTDB is the recommended real-time transport because the system needs push-like coordination semantics between the web UI and one or more Go clients.

That fits RTDB well:

- commands are small
- presence updates are frequent
- stream updates are ephemeral
- ordering can be coarse but predictable
- the web UI needs quick fan-out

Most importantly, Go can use REST streaming for RTDB rather than pretending Firestore REST will act like a full listener SDK.

## Why Firestore Owns the Durable Plane

Firestore is still useful, but for a different job:

- persistent session history
- instance metadata
- queryable collections
- structured documents with clearer evolution over time

This keeps the final persisted model clean and reduces noisy ephemeral writes in Firestore.

## Command Delivery Model

Each command is written under a specific instance:

```text
/users/{uid}/commands/{instanceId}/{commandId}
```

The owning client listens only for commands for the instances it manages.

### Command lifecycle

1. Web writes command with `status = pending`.
2. Owning Go client receives the event from RTDB stream.
3. Go validates ownership and idempotency.
4. Go updates status to `ack`.
5. Go begins execution and updates status to `running`.
6. Go finishes with `done` or `error`.
7. If the action produces durable artifacts, Go writes those to Firestore.

### Why explicit status transitions matter

This gives the UI and recovery logic a stable protocol:

- `pending` means not yet claimed
- `ack` means delivery succeeded
- `running` means execution started
- `done` means completed successfully
- `error` means terminal failure
- `expired` means nobody claimed it in time

This is better than treating command documents as fire-and-forget messages.

## Streaming Output Model

Assistant output should not be written directly to Firestore on every token or chunk.

Instead:

1. Go executes a prompt locally.
2. Partial output is buffered in memory.
3. Every 200 to 500 ms, Go updates the RTDB stream node for the session.
4. Web consumes that stream node in real time.
5. On completion, Go writes the final assistant message to Firestore.
6. RTDB stream node is marked `complete` and may be garbage-collected later.

### Why this matters

This avoids:

- excessive Firestore writes
- storing noisy partial fragments in durable history
- needing polling for token-level UI updates

## Presence Model

Presence exists at two levels:

### Client presence

Tracks whether a machine-level Go client is online.

```text
/users/{uid}/clients/{clientId}/presence
```

### Instance runtime presence

Tracks whether a particular instance is alive and what it is currently doing.

```text
/users/{uid}/instances/{instanceId}/runtime
```

This distinction helps the dashboard answer two different questions:

- Is this machine alive?
- Is this instance healthy and available?

## Client Registration Model

Every Go client must have a stable `clientId`.

Recommended behavior:

1. On first login, client generates a UUID and stores it locally.
2. Client registers itself in Firestore under `/users/{uid}/clients/{clientId}`.
3. Client updates runtime presence in RTDB.
4. Instances created by that client record `client_id`.

This explicit client identity resolves several ambiguous ownership problems from v2.

## Telegram Model

Telegram creates an extra coordination problem because a single bot token should generally not be actively polled by multiple clients at the same time.

The recommended v3 approach is:

- Telegram token is user-level configuration.
- Exactly one client per user is the active Telegram owner.
- That client is marked as `telegram_role = primary`.
- Other clients are `standby` and do not run the bot.

### Telegram failover

Possible failover approach:

1. Primary client publishes heartbeat in RTDB.
2. If heartbeat is stale for a threshold window, another eligible client may claim leadership.
3. Leadership claim is written to RTDB first, then reflected to Firestore.

This is optional for the first implementation. A simpler first version is manual primary-client selection in the dashboard.

## Recommended Security Rules

## Firestore

All normal application documents should live under `/users/{uid}`.

```javascript
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid}/{document=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
```

## RTDB

```json
{
  "rules": {
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
}
```

### Rule philosophy

Do not encode too much application logic into database rules initially.

At first, use rules primarily for:

- user isolation
- gross path protection

Application-level ownership checks such as "only the owning client should ack a command" should still be enforced in Go and in the web app protocol.

## Operational Semantics

## Startup Flow

1. Go loads local credentials and `clientId`.
2. Go refreshes or obtains a Firebase ID token.
3. Go registers or updates the client document in Firestore.
4. Go opens RTDB streaming connections for:
   - client-scoped runtime
   - owned instance commands
   - optional Telegram runtime coordination
5. Go loads user config and client config from Firestore.
6. Go restores instances assigned to this client with `auto_start = true`.
7. Go starts periodic presence updates.
8. Go marks itself online in RTDB.

## Command Execution Flow

1. Web writes command to RTDB.
2. Go receives command via stream.
3. Go checks:
   - instance exists locally
   - `client_id` matches this client
   - command not already handled
4. Go updates command status.
5. Go runs local provider operation.
6. Go streams partial output to RTDB if applicable.
7. Go writes final durable records to Firestore.
8. Go marks command terminal state in RTDB.

## Recovery Flow

If a Go client disconnects unexpectedly:

- its RTDB heartbeats stop
- client presence becomes stale
- instance runtime presence becomes stale
- pending commands remain visible to the UI

The dashboard can then show:

- client offline
- instance unavailable
- command stuck or expired

Optional cleanup workers can be implemented later, but the design should not require a central cleanup service on day one.

## Failure and Consistency Model

This system is eventually consistent across RTDB and Firestore.

That is acceptable if responsibilities are clearly separated:

- RTDB answers "what is happening now?"
- Firestore answers "what happened and what exists?"

### Expected consistency behavior

- A stream may reach `complete` in RTDB milliseconds before the final message appears in Firestore.
- Presence may go stale before instance metadata is updated.
- A command may be `done` in RTDB even if a Firestore write must be retried.

The UI should be built with this in mind instead of assuming cross-database atomicity.

### Idempotency requirements

Go command handling should be idempotent where possible.

Examples:

- repeated `stop` should be safe
- repeated `create_session` should not create duplicates if a client crash happens after partial success
- final message writes should use deterministic IDs or duplicate detection where practical

## Performance Expectations

REST is not inherently the bottleneck here.

The bigger performance distinction is:

- REST with streaming: acceptable for this product
- REST with polling: poor for this product

Expected behavior under the proposed design:

- command latency should feel near-real-time
- presence should update within seconds
- stream UI should feel live with 200 to 500 ms batching
- durable Firestore writes should happen on human-scale events, not on every token

## Polling Policy

Polling is allowed only for:

- startup reconciliation
- watchdog health checks
- rare recovery scans for stale commands or stale presence

Polling is not the default mechanism for:

- command delivery
- live stream output
- presence

This should be treated as a hard design rule.

## Migration Guidance from Current Design

## Step 1: Stop treating Go as a backend

Remove any design assumptions that:

- Go owns global state for all users
- Go can safely hold admin credentials
- web talks to Go as if Go were the authority

## Step 2: Introduce explicit client identity

Add `clientId` and assign instance ownership to a client.

This is a prerequisite for multi-machine correctness.

## Step 3: Move real-time traffic to RTDB

Migrate these paths first:

- commands
- presence
- stream state
- Telegram runtime state

## Step 4: Keep Firestore for durable records

Persist:

- instance metadata
- sessions
- messages
- user config
- client config

## Step 5: Remove periodic full-state overwrite

Do not let clients periodically overwrite a global `/instances` tree.

All writes should be:

- scoped by user
- scoped by resource
- incremental
- ownership-aware

## Open Questions

The following questions remain, but they no longer block the main architecture:

1. Should the web dashboard allow reassigning an instance from one client to another, or should instance ownership be immutable after creation?
2. Should Telegram primary-client failover be automatic or manual in v1?
3. How long should RTDB stream nodes be retained after completion?
4. Should durable command history also be copied into Firestore for auditing or debugging?
5. Should client config support multiple named profiles instead of a single `default` document?

## Final Recommendation

The recommended v3 architecture is:

- Firebase Auth for identity
- Go authenticates with Auth REST APIs
- RTDB REST streaming for control-plane realtime traffic
- Firestore REST for persistent records
- all durable user-owned data under `/users/{uid}`
- explicit `clientId` ownership for all machine-managed resources

In short:

- v1 was wrong because it treated Go as a backend
- v2 was incomplete because it leaned too hard on Firestore for realtime
- v3 should split realtime and persistence cleanly, based on the actual constraints of Go and Firebase
