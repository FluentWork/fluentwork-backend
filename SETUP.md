# FluentWork Backend - Local Development Setup

## Prerequisites

- Go 1.26+
- MySQL 8.0+
- Redis 7.0+

Install via Homebrew:
```bash
brew install mysql redis
```

## Quick Start

### 1. Full stack with Docker Compose (when you need MySQL + Redis)

```bash
./scripts/dev-stack.zsh
```

Use this path when the work has to exercise the **real storage layer**. It will:
- Start MySQL on `127.0.0.1:3306` via Docker Compose
- Start Redis on `127.0.0.1:6379` via Docker Compose
- Apply all `migrations/*.sql`
- Start `app-server` and `voice-gateway`

Stop compose services later with:

```bash
./scripts/dev-down.sh
```

> **Which entrypoint?** All three scripts work; they differ in what they bring up, not in
> correctness. README's `dev-up.sh` is the fastest and needs no Docker (in-memory stores — what
> the iOS client uses day to day). `dev-stack.zsh` (this page) also starts MySQL + Redis. Neither
> is "the" entrypoint; pick by whether you need the database.
>
> **Whatever you pick, a physical device needs `--host`** — see step 5.

### 2. Low-level path: Start Local Services via Homebrew

```bash
./scripts/local-services-start.sh
```

### 3. Initialize Database (First Time Only)

```bash
./scripts/local-db-init.sh
```

This will:
- Create `fluentwork` database
- Create user `fw` with password `fw`
- Apply all migrations from `migrations/*.sql`

### 4. Start FluentWork Backend

```bash
./scripts/dev-local-start.sh
```

This will:
- Load `.env.dev` **if present**, then `.env.volc.local` on top. Both are gitignored
  (`.gitignore` excludes `.env.*`), so a fresh clone has neither and the script falls back to its
  built-in local defaults. Templates live in `configs/*.env.example`.
- Start app-server on `http://127.0.0.1:8080`
- Start voice-gateway on `ws://127.0.0.1:8081/v1/voice`
- Run smoke tests

Press `Ctrl-C` to stop.

### 5. Run against a physical device (iPhone / Android)

**All three start scripts take `--host <your LAN IP>`.** Leaving it out is the single most common
"the app can't connect" report, because the default is `127.0.0.1` — a phone handed
`ws://127.0.0.1:8081/v1/voice` dials **itself**.

```bash
./scripts/dev-up.sh          --host 192.168.2.185
./scripts/dev-stack.zsh      --host 192.168.2.185
./scripts/dev-local-start.sh --host 192.168.2.185
```

It is not just a log line. `--host` becomes `VOICE_GATEWAY_WSS_URL`
(`dev-up.sh:134`, `dev-local-start.sh:199`; `dev-stack.zsh` passes `--host` through to `dev-up.sh`),
which `config.Load` reads (`internal/config/config.go:129`) and app-server copies into the
`wss_url` field of `POST /api/v1/sessions` (`internal/session/service.go:191`). That field is the
address the client actually dials. So it has to be the address **the phone** can reach — your
Mac's LAN IP, not the Mac's own loopback. The built-in default, with no `--host` at all, is
literally `ws://127.0.0.1:8081/v1/voice` (`config.go:22`).

The phone and the Mac have to be on the same network, and macOS may ask whether to allow incoming
connections the first time — accept it.

## Management Commands

### Check Services Status

```bash
./scripts/local-services-status.sh
```

### Stop Local Services

```bash
./scripts/local-services-stop.sh
```

### Start Backend Without Voice Gateway

```bash
./scripts/dev-local-start.sh --no-gateway
```

### Use Custom Port

```bash
PORT=9000 ./scripts/dev-local-start.sh
```

## Environment Configuration

### Development (`.env.dev`, optional)

**This file is not in the repository** — `.gitignore` excludes `.env.*`. With no file present the
scripts use the defaults below; copy `configs/*.env.example` if you need to change them.

- **MySQL:** `fw:fw@tcp(127.0.0.1:3306)/fluentwork`
- **Redis:** `127.0.0.1:6379` (no password)
- **JWT Secret:** Development default
- **Internal Token:** Development default

### Production (`.env.prod`, you create it)

Not in the repository either. Start from `configs/app-server.env.example` /
`configs/voice-gateway.env.example`.

⚠️ **MUST CHANGE THESE IN PRODUCTION:**
- MySQL password
- Redis password
- JWT secret (minimum 32 characters)
- Internal API token
- Voice Gateway WSS URL (use `wss://` for production)

## Architecture

```
┌─────────────────┐
│   iOS Client    │
└────────┬────────┘
         │ HTTPS
         ▼
┌─────────────────┐     ┌──────────────┐
│   app-server    │────▶│    MySQL     │
│   (port 8080)   │     │  (port 3306) │
└────────┬────────┘     └──────────────┘
         │                      
         │ Internal HTTP        ┌──────────────┐
         ▼                      │    Redis     │
┌─────────────────┐            │  (port 6379) │
│ voice-gateway   │───────────▶└──────────────┘
│   (port 8081)   │
└────────┬────────┘
         │ WebSocket
         ▼
┌─────────────────┐
│   iOS Client    │
│  (SpeakingRoom) │
└─────────────────┘
```

## API Endpoints

### App Server (8080)

- `GET /healthz` - Health check
- `GET /readyz` - Readiness check
- `POST /api/v1/auth/guest` - Guest login
- `POST /api/v1/sessions` - Create voice session
- `POST /internal/v1/tickets/consume` - Internal ticket validation

### Voice Gateway (8081)

- `GET /healthz` - Health check
- `WS /v1/voice` - WebSocket voice connection

## Troubleshooting

### MySQL Connection Failed

```bash
# Check if MySQL is running
./scripts/local-services-status.sh

# Restart MySQL
brew services restart mysql
```

### Redis Connection Failed

```bash
# Check if Redis is running
./scripts/local-services-status.sh

# Restart Redis
brew services restart redis
```

### The phone connects but nothing reaches the backend (or the session dies immediately)

Read the `wss_url` that `POST /api/v1/sessions` returned. If it says `127.0.0.1`, the server was
started **without `--host`** and the phone is dialling itself. Restart with `--host <LAN IP>`
(step 5) and **create a new session** — the URL is baked in at creation time, so an existing
session keeps handing out the old value.

```bash
# what the client is being told to dial
curl -sS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"scene_type":"standup"}' http://<ip>:8080/api/v1/sessions | grep -o '"wss_url":"[^"]*"'
```

### Port Already in Use

```bash
# Use different ports
PORT=9000 GATEWAY_PORT=9001 ./scripts/dev-local-start.sh
```

### Database Not Found

```bash
# Re-initialize database
./scripts/local-db-init.sh
```

## Testing

### Test Guest Authentication

```bash
curl -X POST http://127.0.0.1:8080/api/v1/auth/guest \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"test-device"}'
```

### Test Voice Session Creation

```bash
# First, get access token from guest auth
ACCESS_TOKEN="<token_from_guest_auth>"

curl -X POST http://127.0.0.1:8080/api/v1/sessions \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}'
```

## Development Workflow

1. **Start services once per dev session:**
   ```bash
   ./scripts/local-services-start.sh
   ```

2. **Run backend (restart as needed):**
   ```bash
   ./scripts/dev-local-start.sh
   ```

3. **When done for the day:**
   ```bash
   ./scripts/local-services-stop.sh
   ```

## Migration to Production

1. Copy `configs/app-server.env.example` / `configs/voice-gateway.env.example` to `.env.prod`
   on your production server
2. Replace every placeholder in it with a secure value
3. Configure MySQL with strong passwords
4. Configure Redis with authentication
5. Use HTTPS/WSS in production
6. Set up proper firewall rules
7. Consider using managed database services

## Notes

- Development uses weak passwords for convenience - **NEVER use in production**
- MySQL and Redis run as macOS services (survive reboots)
- Data persists between backend restarts
- Voice gateway connects to app-server via internal HTTP
