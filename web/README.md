# Remote Web Frontend

Angular 21 single-page application for managing OpenCode Manager instances remotely. Communicates entirely through Firebase (RTDB + Firestore) -- no direct connection to the Go server.

## Architecture

Unlike the local dashboard (`dashboard/`), the remote web frontend uses Firebase as a relay:

- **Firebase Auth** -- Google or email/password sign-in
- **Firebase RTDB** -- real-time streaming, commands, presence
- **Firebase Firestore** -- instance list, session history, messages

The Go server and web frontend are both Firebase clients. No public IP, port forwarding, or tunnels are required.

## Development

```bash
cd web
pnpm install
pnpm start        # ng serve on http://localhost:4200
```

Requires Firebase project credentials configured in `src/environments/`.

## Building

```bash
make web          # Builds for Firebase Hosting deployment
```

## Deployment

The web frontend is deployed to Firebase Hosting via GitHub Actions:

- **On PR**: Preview deployment (`firebase-hosting-pull-request.yml`)
- **On merge to main**: Production deployment (`firebase-hosting-merge.yml`)

## Project Structure

```
src/app/
├── services/
│   └── firebase.service.ts         Firebase Auth + RTDB + Firestore operations
├── guards/
│   └── auth.guard.ts               Route guard requiring Firebase Auth
├── components/
│   ├── login/                      Google / email sign-in
│   ├── dashboard/                  Instance grid, account linking
│   ├── instance-card/              Instance status card with provider badge
│   └── prompt-panel/               Session selector, history, RTDB streaming
├── app.component.ts                Root component
├── app.config.ts                   App + Firebase configuration
└── app.routes.ts                   Route definitions
```

## Tech Stack

| Layer | Choice |
|-------|--------|
| Framework | Angular 21 (standalone components) |
| TypeScript | 5.9 |
| Bundler | Angular CLI (esbuild) |
| Linter | Biome |
| Auth | Firebase Auth JS SDK |
| Persistent data | Firebase Firestore JS SDK |
| Real-time data | Firebase RTDB JS SDK |
