# Web Frontend Design

## Overview

Add a web-based dashboard that communicates with the Go server through Firebase, eliminating the need to expose the Go server to the public internet. Both the Go client and the web frontend are "clients" that connect outward to Firebase.

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
│ Components:    │           │  │ (streaming tokens,  │  │           │ Components:    │
│ - Process Mgr  │           │  │  commands)          │  │           │ - Login page   │
│ - TG Bot       │           │  ├─────────────────────┤  │           │ - Dashboard    │
│ - Firebase SDK │           │  │ Firestore           │  │           │ - Instance mgr │
│                │           │  │ (persistent data,   │  │           │ - Session view │
│                │           │  │  history)           │  │           │ - Prompt panel │
└────────────────┘           │  └─────────────────────┘  │           └────────────────┘
                             └──────────────────────────┘
```

**Key property:** Both sides make only outbound connections. No public IP, no port forwarding, no tunnels.

## Firebase Services Used

| Service             | Purpose                              | Go SDK                          | Web SDK              |
|---------------------|--------------------------------------|---------------------------------|----------------------|
| Auth                | User login/registration              | `firebase.google.com/go/v4`     | `firebase/auth`      |
| Realtime Database   | High-frequency streaming data        | `firebase.google.com/go/v4`     | `firebase/database`  |
| Firestore           | Persistent structured data           | `cloud.google.com/go/firestore` | `firebase/firestore`  |

## Data Model

### Firebase Realtime Database (RTDB)

Used for ephemeral, high-frequency data. Priced by bandwidth, not operations.

```
/streams/{sessionId}
    content: string           // Accumulated text (updated every ~300ms)
    status: "streaming" | "idle" | "complete" | "error"
    tool_calls: [             // Active tool invocations
        {
            name: string,
            status: "running" | "done" | "error",
            detail: string
        }
    ]
    error: string | null
    updated_at: number        // Unix ms

/commands/{instanceId}/{commandId}
    action: "start" | "stop" | "prompt" | "delete" | "create_session"
    payload: object           // Action-specific data
    status: "pending" | "ack" | "done" | "error"
    result: object | null     // Response from Go server
    user_id: string           // Firebase Auth UID
    created_at: number
    updated_at: number

/presence/{instanceId}
    online: boolean           // Go server heartbeat
    last_seen: number         // Unix ms
    version: string           // Go server version
```

### Firestore

Used for persistent, queryable data. Priced by operations (low frequency).

```
/users/{uid}                          // Firebase Auth UID
    display_name: string
    role: "admin" | "user"
    telegram_user_id: number | null   // Link to Telegram identity
    created_at: timestamp
    settings: {
        theme: "dark" | "light"
        notifications: boolean
    }

/instances/{instanceId}
    name: string
    directory: string
    status: "running" | "stopped" | "starting" | "failed"
    provider_type: "claudecode" | "opencode"
    owner_id: string                  // Firebase Auth UID
    port: number
    auto_start: boolean
    created_at: timestamp
    updated_at: timestamp

/sessions/{sessionId}
    instance_id: string
    title: string
    message_count: number
    worktree_path: string
    branch: string
    created_at: timestamp
    updated_at: timestamp

/history/{sessionId}/messages/{messageId}
    role: "user" | "assistant"
    content: string                   // Full text of completed response
    tool_calls: [...]                 // Tool invocations with results
    created_at: timestamp
```

### Firestore Security Rules

```javascript
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    // Users can only read/write their own profile
    match /users/{uid} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }

    // Instances: owner can read/write, admin can read all
    match /instances/{instanceId} {
      allow read: if request.auth != null &&
        (resource.data.owner_id == request.auth.uid ||
         get(/databases/$(database)/documents/users/$(request.auth.uid)).data.role == 'admin');
      allow create: if request.auth != null;
      allow update, delete: if request.auth != null &&
        resource.data.owner_id == request.auth.uid;
    }

    // Sessions: accessible if user owns the parent instance
    match /sessions/{sessionId} {
      allow read, write: if request.auth != null;
      // Fine-grained check done via instance ownership in application logic
    }

    // History: read-only from web, written by Go server (admin SDK)
    match /history/{sessionId}/messages/{messageId} {
      allow read: if request.auth != null;
    }
  }
}
```

### RTDB Security Rules

```json
{
  "rules": {
    "streams": {
      "$sessionId": {
        ".read": "auth != null",
        ".write": false
      }
    },
    "commands": {
      "$instanceId": {
        "$commandId": {
          ".read": "auth != null",
          ".write": "auth != null && newData.child('user_id').val() === auth.uid"
        }
      }
    },
    "presence": {
      "$instanceId": {
        ".read": "auth != null",
        ".write": false
      }
    }
  }
}
```

Note: Go server uses the Admin SDK which bypasses security rules. Rules only apply to web frontend (client SDK).

## Data Flow

### 1. Real-time Streaming (Claude Code → Web)

```
Claude Code produces tokens
        │
        ▼
Go Server buffers tokens (300ms window)
        │
        ▼
RTDB: /streams/{sessionId}.content = accumulated text
        │
        ▼
Web Frontend: onValue() fires, re-renders content
```

Go side (pseudocode):

```go
func (s *FirebaseStreamer) StreamToFirebase(sessionID string, events <-chan provider.StreamEvent) {
    ref := s.rtdb.NewRef("streams/" + sessionID)
    ref.Set(ctx, map[string]interface{}{
        "content":    "",
        "status":     "streaming",
        "tool_calls": []interface{}{},
        "updated_at": time.Now().UnixMilli(),
    })

    var mu sync.Mutex
    var fullContent string
    var toolCalls []map[string]interface{}

    // Flush buffer every 300ms
    ticker := time.NewTicker(300 * time.Millisecond)
    defer ticker.Stop()

    dirty := false

    go func() {
        for range ticker.C {
            mu.Lock()
            if dirty {
                ref.Update(ctx, map[string]interface{}{
                    "content":    fullContent,
                    "tool_calls": toolCalls,
                    "updated_at": time.Now().UnixMilli(),
                })
                dirty = false
            }
            mu.Unlock()
        }
    }()

    for evt := range events {
        mu.Lock()
        switch evt.Type {
        case "text":
            fullContent += evt.Content
            dirty = true
        case "tool_use":
            toolCalls = append(toolCalls, map[string]interface{}{
                "name":   evt.ToolName,
                "status": evt.ToolStatus,
                "detail": evt.Content,
            })
            dirty = true
        case "done":
            ref.Update(ctx, map[string]interface{}{
                "content":    fullContent,
                "status":     "complete",
                "tool_calls": toolCalls,
                "updated_at": time.Now().UnixMilli(),
            })
        case "error":
            ref.Update(ctx, map[string]interface{}{
                "status":     "error",
                "error":      evt.Content,
                "updated_at": time.Now().UnixMilli(),
            })
        }
        mu.Unlock()
    }
}
```

Web side (pseudocode):

```typescript
import { ref, onValue } from "firebase/database";

function useStream(sessionId: string) {
    const [content, setContent] = useState("");
    const [status, setStatus] = useState("idle");
    const [toolCalls, setToolCalls] = useState([]);

    useEffect(() => {
        const streamRef = ref(db, `streams/${sessionId}`);
        const unsub = onValue(streamRef, (snapshot) => {
            const data = snapshot.val();
            if (data) {
                setContent(data.content);
                setStatus(data.status);
                setToolCalls(data.tool_calls || []);
            }
        });
        return unsub;
    }, [sessionId]);

    return { content, status, toolCalls };
}
```

### 2. Commands (Web → Go)

```
User clicks "Start Instance" in web UI
        │
        ▼
Web writes to RTDB: /commands/{instanceId}/{newId}
    { action: "start", status: "pending", user_id: uid }
        │
        ▼
Go Server: Firestore Snapshots listens on /commands/{instanceId}
    Receives new command → executes → updates status to "done"
        │
        ▼
Web: onValue() sees status change → updates UI
```

### 3. Prompt from Web

```
User types prompt in web UI
        │
        ▼
Web writes command:
    /commands/{instanceId}/{cmdId}
    { action: "prompt", payload: { session_id, content }, status: "pending" }
        │
        ▼
Go receives command
    → Calls Provider.Prompt(sessionID, content)
    → Updates command status to "ack"
    → Streams events to /streams/{sessionId} (flow #1 above)
    → On completion: updates command status to "done"
    → Saves to Firestore /history/{sessionId}/messages/
```

### 4. Presence (Go → Web)

```
Go Server: every 30 seconds
    → RTDB: /presence/{instanceId} = { online: true, last_seen: now }

Go Server: on shutdown
    → RTDB: /presence/{instanceId} = { online: false, last_seen: now }

Web: onValue(/presence/{instanceId})
    → Show green/red dot for each instance
    → If last_seen > 60s ago, treat as offline
```

## Auth Flow

### Registration & Login

```
Web Frontend                         Firebase Auth                    Go Server
    │                                     │                              │
    │──signInWithEmailAndPassword()──→     │                              │
    │←──────── ID Token ──────────────    │                              │
    │                                     │                              │
    │  (token stored in browser)          │                              │
    │                                     │                              │
    │──read Firestore with token────→     │──verify & enforce rules──→   │
    │                                     │                              │
    │                                     │    Go uses Admin SDK         │
    │                                     │    (bypasses rules,          │
    │                                     │     verifies tokens          │
    │                                     │     for command auth)        │
```

### Linking Telegram ↔ Web Account

Optional: allow users to link their Telegram identity to their web account.

```
1. Web user goes to Settings → "Link Telegram"
2. Web generates a 6-digit code, writes to Firestore:
   /link_codes/{code} = { uid: "firebase-uid", expires: ... }
3. User sends /link <code> to Telegram bot
4. Go server verifies code, writes telegram_user_id to /users/{uid}
5. Now instances created via Telegram are visible in web dashboard
```

## Go Server Changes

### New Package: `internal/firebase`

```
internal/firebase/
    client.go       // Firebase app initialization
    sync.go         // Bidirectional sync: local SQLite ↔ Firestore
    streamer.go     // Stream provider events to RTDB
    commands.go     // Listen for commands from RTDB, execute locally
    presence.go     // Heartbeat to RTDB
```

### Sync Strategy

The Go server keeps SQLite as the local source of truth and syncs to Firebase:

```
Local SQLite (source of truth for Go server)
        │
        │  On change (create/update/delete instance):
        ▼
Firestore /instances/{id}  (synced copy for web frontend)
```

This avoids rewriting all existing code. The sync layer is additive.

### Config Changes

New settings in the database:

```
firebase.project_id          = "your-project-id"
firebase.credentials_path    = "/path/to/serviceAccountKey.json"
firebase.enabled             = true
```

Or via environment variables:

```
FIREBASE_PROJECT_ID=your-project-id
GOOGLE_APPLICATION_CREDENTIALS=/path/to/serviceAccountKey.json
```

## Web Frontend

### Tech Stack

| Layer      | Choice          | Reason                                     |
|------------|-----------------|---------------------------------------------|
| Framework  | Angular (keep)  | Already exists, team knows it               |
| Hosting    | Vercel or Firebase Hosting | Free static hosting          |
| Auth       | Firebase Auth JS SDK | Official, full-featured               |
| DB         | Firebase JS SDK | Official, Realtime + Firestore              |
| Styling    | Existing (keep) | No need to change                           |

Alternative: switch to React/Next.js if starting fresh. But Angular works fine.

### Pages

```
/login              → Email/password login form
/register           → Registration form
/dashboard          → Instance list with status indicators (green/red dot)
/instance/:id       → Instance detail: sessions, controls (start/stop/delete)
/session/:id        → Real-time streaming view (token-by-token rendering)
/settings           → User preferences, Telegram link
```

### Session Streaming View

The core feature — watching Claude Code work in real-time:

```
┌─────────────────────────────────────────────────┐
│ Session: "Fix authentication bug"    [Running]  │
├─────────────────────────────────────────────────┤
│                                                 │
│ ┌─ Tool: Read file src/auth.ts ───────── ✅ ─┐ │
│ │ (collapsed, click to expand)               │ │
│ └────────────────────────────────────────────┘ │
│                                                 │
│ ┌─ Tool: Edit file src/auth.ts ───────── ⏳ ─┐ │
│ │ Replacing line 42-48...                    │ │
│ └────────────────────────────────────────────┘ │
│                                                 │
│ I found the issue. The token validation was    │
│ checking the wrong field. I've updated the     │
│ auth middleware to use `user.id` instead of     │
│ `user.email` for the lookup. Let me run the   │
│ tests to verify...█                            │
│                                                 │
├─────────────────────────────────────────────────┤
│ [Send prompt...]                    [Stop]      │
└─────────────────────────────────────────────────┘
```

## Implementation Phases

### Phase 1: Firebase Integration (Go Side)

1. Add Firebase Admin SDK dependency
2. Implement `internal/firebase/client.go` — initialization
3. Implement `internal/firebase/sync.go` — SQLite → Firestore sync for instances
4. Implement `internal/firebase/streamer.go` — stream events to RTDB
5. Implement `internal/firebase/commands.go` — listen for commands
6. Implement `internal/firebase/presence.go` — heartbeat
7. Wire into `internal/app/app.go`

### Phase 2: Web Frontend Auth

1. Add Firebase Auth to Angular app
2. Login/register pages
3. Auth guard on routes
4. Token management

### Phase 3: Dashboard

1. Instance list (read from Firestore, real-time status via RTDB presence)
2. Instance controls (start/stop/delete via RTDB commands)
3. Session list per instance

### Phase 4: Real-time Session View

1. Streaming content renderer (read from RTDB /streams/)
2. Tool call visualization
3. Prompt input (write command to RTDB)
4. Abort button

### Phase 5: Polish

1. Telegram ↔ Web account linking
2. History browser (completed sessions from Firestore)
3. Settings page
4. Responsive design for mobile

## Cost Estimate (Personal Use)

| Resource            | Monthly Usage (est.) | Free Tier   | Cost  |
|---------------------|---------------------|-------------|-------|
| Firebase Auth       | <10 users           | Unlimited   | $0    |
| Firestore reads     | ~30k/month          | 1.5M/month  | $0    |
| Firestore writes    | ~5k/month           | 600k/month  | $0    |
| Firestore storage   | ~10 MB              | 1 GB        | $0    |
| RTDB bandwidth      | ~5 GB/month         | 10 GB/month | $0    |
| RTDB storage        | ~5 MB (ephemeral)   | 1 GB        | $0    |
| Web hosting         | Static files        | Free (Vercel/Firebase) | $0 |
| **Total**           |                     |             | **$0** |
