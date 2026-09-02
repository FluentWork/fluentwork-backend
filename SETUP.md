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

### 1. Recommended: Start Full Stack with Docker Compose

```bash
./scripts/dev-stack.zsh
```

This is the preferred entrypoint for a full local environment. It will:
- Start MySQL on `127.0.0.1:3306` via Docker Compose
- Start Redis on `127.0.0.1:6379` via Docker Compose
- Apply all `migrations/*.sql`
- Start `app-server` and `voice-gateway`

Stop compose services later with:

```bash
./scripts/dev-down.sh
```

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
- Load configuration from `.env.dev`
- Start app-server on `http://127.0.0.1:8080`
- Start voice-gateway on `ws://127.0.0.1:8081/v1/voice`
- Run smoke tests

Press `Ctrl-C` to stop.

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

### Development (.env.dev)

- **MySQL:** `fw:fw@tcp(127.0.0.1:3306)/fluentwork`
- **Redis:** `127.0.0.1:6379` (no password)
- **JWT Secret:** Development default
- **Internal Token:** Development default

### Production (.env.prod)

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

1. Copy `.env.prod` to your production server
2. Update all `CHANGE_*` placeholders with secure values
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
