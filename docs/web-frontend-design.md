# Web Frontend Architecture

## Overview

The web frontend communicates with the Go server entirely through **Firebase** (Realtime Database + Firestore). Both sides are Firebase clients making outbound HTTPS connections -- there is no direct connection between them.

**Key property:** No public IP, no port forwarding, no tunnels. The Go server only makes outbound HTTPS requests to Firebase.

All data paths are **user-scoped** under `users/{uid}/`. Each authenticated user can only access their own data.

## Architecture

```
+----------------+           +--------------------------+           +----------------+
|   Go Server    |--writes-->|        Firebase           |<--listens-|  Web Frontend  |
|  (internal     |           |                           |           |  (embedded in  |
|   network)     |<--listens-|  +---------------------+  |--writes-->|   Go binary)   |
|                |           |  | Auth                |  |           |                |
| No public IP   |           |  | (Google / email)    |  |           | Angular SPA    |
| No open ports  |           |  +---------------------+  |           |                |
|                |           |  | Realtime Database   |  |           | Components:    |
| Components:    |           |  | (runtime, streams,  |  |           | - Login        |
| - Process Mgr  |           |  |  commands, presence)|  |           | - Dashboard    |
| - TG Bot       |           |  +---------------------+  |           | - Instance Card|
| - Firebase REST|           |  | Firestore           |  |           | - Prompt Panel |
|                |           |  | (instances, sessions,|  |           |                |
|                |           |  |  messages, config)  |  |           |                |
+----------------+           +--------------------------+           +----------------+
```

## Firebase Services Used

| Service | Purpose | Go Side | Web Side |
|---------|---------|---------|----------|
| Auth | User login/registration | REST API (`identitytoolkit.googleapis.com`) | `firebase/auth` JS SDK |
| Realtime Database | Runtime state, streaming, commands, presence | REST API (`{db}.firebaseio.com`) | `firebase/database` JS SDK |
| Cloud Firestore | Persistent data (instances, sessions, messages, config) | REST API (`firestore.googleapis.com`) | `firebase/firestore` JS SDK |

Go authenticates as a regular Firebase user via REST API, not via the Admin SDK or a service account.

## Authentication

Go signs in as a regular Firebase user using a stored `refresh_token`:

```
Go Server                                Firebase Auth REST API
    |                                          |
    +-- POST securetoken.googleapis.com ------>|  (refresh_token)
    |   /v1/token                              |
    |<-------- new idToken --------------------|
    |                                          |
    |  (idToken used as ?auth=<idToken> on     |
    |   all RTDB/Firestore requests)           |
```

Token refresh happens automatically when the token is within 5 minutes of expiry (tokens last ~60 minutes).

The web frontend supports two sign-in methods:
- **Google** -- via popup (recommended)
- **Email/Password** -- for manual accounts

## Data Model

### Firestore (persistent data, user-scoped)

```
users/{uid}/
  config/
    user                                  <-- User-level settings
      telegram.token: string                  Telegram bot token
      telegram.allowed_users: string          Comma-separated user IDs
      telegram.board_interval: string         Active tasks board refresh interval
      web.enabled: string                     Enable web dashboard
      web.addr: string                        Web dashboard listen address
    clients/{clientID}                    <-- Per-machine settings
      process.claudecode_binary: string       Path to claude binary
      process.opencode_binary: string         Path to opencode binary
      process.port_range_start: string        OpenCode port range start
      process.port_range_end: string          OpenCode port range end
  clients/{clientID}                      <-- Client registration
    hostname: string
    started_at: timestamp
  instances/{instanceID}                  <-- Instance metadata
    id: string
    name: string
    directory: string
    status: "running" | "stopped" | "starting" | "failed"
    provider_type: "claudecode" | "opencode"
    client_id: string
    sessions/{sessionID}                  <-- Session metadata
      id: string
      title: string
      message_count: number
      branch: string                        (worktree branch name, if any)
      worktree_path: string                 (worktree directory, if any)
      updated_at: timestamp
      messages/{messageID}                <-- Conversation history
        id: string
        role: "user" | "assistant"
        content: string
        tool_calls: [{ name, status, detail, input?, output? }]
        created_at: timestamp
```

### RTDB (ephemeral/real-time data, user-scoped)

```
users/{uid}/
  clients/{clientID}/presence             <-- Client heartbeat (every 30s)
    online: boolean
    last_seen: number                       Unix ms

  instances/{instanceID}/runtime          <-- Instance runtime status
    online: boolean
    client_id: string
    last_seen: number                       Unix ms

  streams/{sessionID}                     <-- Real-time streaming content
    content: string                         Accumulated response text
    status: "streaming" | "complete" | "error"
    tool_calls: [{ name, status, detail }]
    error: string | null
    client_id: string
    updated_at: number                      Unix ms

  commands/{instanceID}/{commandID}       <-- Web-to-server command queue
    action: "start" | "stop" | "delete" | "prompt" | "create_session" | "list_sessions" | "create"
    payload: object
    status: "pending" | "ack" | "done" | "error"
    user_id: string                         Firebase Auth UID
    created_at: number
    updated_at: number
    result: object | null                   Set by Go on success
    error: string | null                    Set by Go on failure

  telegram/
    user_state/{telegramUserID}           <-- Telegram bot active context
      active_instance_id: string
      active_session_id: string
    message_sessions/{chatID}_{messageID} <-- Reply-to session mapping
      string (sessionID)

  telegram_id: number                     <-- Linked Telegram user ID

link_codes/{code}                         <-- Account linking (NOT user-scoped)
  uid: string                               6-digit code, 10-minute expiry
  expires: number                           Unix ms
```

## Data Flow

### 1. Instance Sync (Go --> Firestore)

Go server writes instance metadata to Firestore when instances are created, started, stopped, or deleted:

```
Firestore: users/{uid}/instances/{instanceID}
  { id, name, directory, status, provider_type, client_id }
```

Web frontend polls Firestore every 5 seconds to refresh the instance list:

```typescript
// firebase.service.ts -- getInstances()
const instancesRef = collection(this.firestore, "users", uid, "instances");
const snapshot = await getDocs(instancesRef);
```

### 2. Instance Runtime/Presence (Go --> RTDB)

Every 30 seconds, Go PATCHes per-instance runtime status:

```
RTDB: users/{uid}/instances/{instanceID}/runtime
  { online: true, client_id: "...", last_seen: 1711234567890 }
```

On shutdown, marks all instances offline. Web subscribes via `onValue` for real-time online/offline indicators:

```typescript
// firebase.service.ts -- onInstanceRuntime()
const dbRef = ref(this.db, `users/${uid}/instances/${instanceId}/runtime`);
onValue(dbRef, (snapshot) => callback(snapshot.val()));
```

### 3. Commands (Web --> Go)

Web pushes a new command node to RTDB; Go listens via SSE:

```
Web: push(users/{uid}/commands/{instanceId})
  --> { action: "prompt", payload: {session_id, content}, status: "pending" }

Go (SSE on users/{uid}/commands):
  --> Detects new "pending" command
  --> PATCH status --> "ack"
  --> Execute handler
  --> PATCH status --> "done" (with result) or "error"

Web: onValue sees status change --> resolves promise
```

`sendCommandAndWait()` subscribes via `onValue` with a 30-second timeout.

Supported command actions: `create`, `start`, `stop`, `delete`, `prompt`, `create_session`, `list_sessions`.

### 4. Real-time Streaming (Go --> Web)

`Streamer.WrapEvents()` wraps a provider event channel, buffering to RTDB:

```
Provider.Prompt() --> event channel
    --> Streamer intercepts events
    --> Buffers text + tool_calls in memory
    --> Flushes to users/{uid}/streams/{sessionId} every 300ms (PATCH)
    --> On done/error: final PATCH with terminal status

Web: onValue(users/{uid}/streams/{sessionId})
    --> Re-renders content on each update
```

### 5. Conversation History (Go --> Firestore)

After each prompt completes, Go persists the user message and assistant response to Firestore:

```
Firestore: users/{uid}/instances/{instanceId}/sessions/{sessionId}/messages/{messageId}
  { id, role, content, tool_calls, created_at }
```

Web frontend retrieves history when selecting a session:

```typescript
// firebase.service.ts -- getSessionHistory()
const messagesRef = collection(
  this.firestore, "users", uid, "instances", instanceId,
  "sessions", sessionId, "messages"
);
const q = query(messagesRef, orderBy("created_at", "asc"));
```

### 6. Account Linking (Web + Telegram Bot)

Telegram account linking uses a 6-digit code flow:

1. Web dashboard generates a code and writes it to RTDB `link_codes/{code}` with the user's UID and a 10-minute expiry
2. User sends `/link <code>` to the Telegram bot
3. Bot reads `link_codes/{code}`, validates UID and expiry
4. Bot writes the Telegram user ID to `users/{uid}/telegram_id`
5. Bot deletes the link code
6. Web dashboard listens on `users/{uid}/telegram_id` to detect link status

## Go Implementation

### Package: `internal/firebase`

| File | Responsibility |
|------|---------------|
| `client.go` | Client initialization, ties together all components |
| `auth.go` | Firebase Auth REST API: SignIn, SignInWithRefreshToken, Token (with auto-refresh) |
| `rtdb.go` | RTDB REST client: Get, Set, Update, Delete, Listen (SSE with auto-reconnect) |
| `firestore.go` | Firestore REST client: GetDoc, SetDoc, UpdateDoc, DeleteDoc, ListDocs |
| `paths.go` | Path builders for all RTDB and Firestore paths (user-scoped) |
| `presence.go` | Client + instance heartbeats to RTDB (every 30s), offline marking on shutdown |
| `streamer.go` | Wraps provider event channels, buffers + flushes to RTDB streams (every 300ms) |
| `commands.go` | SSE listener on `users/{uid}/commands`, dispatches to handler, updates status |
| `telegram_state.go` | Telegram user state management (active instance/session, message-session mapping) |

### Package: `internal/store`

`FirestoreStore` implements the `Store` interface using Firestore REST API for all persistent data:

- Instance CRUD: `SaveInstance`, `GetInstance`, `ListInstances`, `DeleteInstance`
- Session CRUD: `SaveClaudeSession`, `GetClaudeSession`, `ListClaudeSessions`, `DeleteClaudeSession`
- Messages: `SaveMessage`, `GetSessionHistory`
- Config: `GetUserConfig`, `SetUserConfig`, `GetClientConfig`, `SetClientConfig`
- Client registration: `RegisterClient`

## Web Frontend

### Tech Stack

| Layer | Choice | Reason |
|-------|--------|--------|
| Framework | Angular (standalone components) | Already exists |
| Hosting | Embedded in Go binary | Single binary deployment |
| Auth | Firebase Auth JS SDK | Google + email/password |
| Persistent data | Firebase Firestore JS SDK | Instance list, session history |
| Real-time data | Firebase RTDB JS SDK | Streams, presence, commands |

### Key Service: `FirebaseService`

`web/src/app/services/firebase.service.ts`

All Firebase interaction is centralized in `FirebaseService`:

- **Auth**: `login`, `register`, `loginWithGoogle`, `logout`
- **Account linking**: `onUserLinkStatus(uid)`, `generateLinkCode(uid)`
- **Instance list**: `getInstances(uid)` -- Firestore query on `users/{uid}/instances`
- **Instance runtime**: `onInstanceRuntime(uid, instanceId)` -- RTDB listener on `users/{uid}/instances/{id}/runtime`
- **Streams**: `onStream(uid, sessionId)` -- RTDB listener on `users/{uid}/streams/{sessionId}`
- **Commands**: `sendCommand(uid, instanceId, action, payload)`, `sendCommandAndWait(...)` -- RTDB push to `users/{uid}/commands/{instanceId}`
- **History**: `getSessionHistory(uid, instanceId, sessionId)` -- Firestore query on `users/{uid}/instances/{id}/sessions/{sid}/messages`

### Interfaces

```typescript
interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  client_id?: string;
}

interface InstanceRuntime {
  online: boolean;
  client_id: string;
  last_seen: number;
}

interface StreamData {
  content: string;
  status: string;
  tool_calls: { name: string; status: string; detail: string }[];
  error?: string;
  updated_at: number;
}

interface Command {
  action: string;
  payload: unknown;
  status: string;
  user_id: string;
  created_at: number;
  updated_at: number;
  result?: unknown;
  error?: string;
}

interface HistoryMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  tool_calls: { name: string; status: string; detail: string; input?: string; output?: string }[];
  created_at: string;
}
```

### Components

| Component | File | Purpose |
|-----------|------|---------|
| `LoginComponent` | `web/src/app/components/login/login.component.ts` | Sign in via Google or email/password. Redirects to dashboard on success. |
| `DashboardComponent` | `web/src/app/components/dashboard/dashboard.component.ts` | Main view. Shows account linking status, instance list (polled from Firestore every 5s), create/start/stop/delete controls. |
| `InstanceCardComponent` | `web/src/app/components/instance-card/instance-card.component.ts` | Displays a single instance with status color, provider badge (CC/OC), and action buttons. |
| `PromptPanelComponent` | `web/src/app/components/prompt-panel/prompt-panel.component.ts` | Session selector, message history (from Firestore), prompt input, real-time streaming display (from RTDB). |

### Routes

```
/login    --> LoginComponent
/         --> DashboardComponent (requires auth via authGuard)
/**       --> redirects to /
```

### Dashboard Flow

1. User signs in (Google or email/password)
2. `authGuard` checks Firebase auth state; redirects to `/login` if unauthenticated
3. Dashboard checks `users/{uid}/telegram_id` via RTDB listener
4. If not linked: generates a 6-digit link code and displays it with instructions to send `/link <code>` to the Telegram bot
5. If linked: starts polling `getInstances(uid)` from Firestore every 5 seconds
6. User can create, start, stop, delete instances via commands pushed to RTDB
7. Selecting an instance opens the prompt panel with session list, history, and streaming view

## Security Rules

### Firestore (`firestore.rules`)

```
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid}/{document=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
```

### RTDB (`firebase-rules.json`)

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

Both Go server and web frontend sign in as regular Firebase users. Security rules enforce that each user can only access their own data under `users/{uid}/`. The `link_codes` path is accessible to any authenticated user since the web frontend writes the code and a different Firebase user (the Go server) reads it.

## Cost Estimate (Personal Use)

| Resource | Monthly Usage (est.) | Free Tier | Cost |
|----------|---------------------|-----------|------|
| Firebase Auth | <10 users | Unlimited | $0 |
| RTDB bandwidth | ~5 GB/month | 10 GB/month | $0 |
| RTDB storage | ~5 MB (ephemeral) | 1 GB | $0 |
| Firestore reads | ~100K/month | 50K/day | $0 |
| Firestore writes | ~10K/month | 20K/day | $0 |
| Firestore storage | ~50 MB | 1 GB | $0 |
| Web hosting | Embedded in binary | N/A | $0 |
| **Total** | | | **$0** |
