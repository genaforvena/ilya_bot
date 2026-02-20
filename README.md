# ilya_bot

A production-ready Telegram bot for handling recruiter communication on behalf of Ilya, a senior Go developer. The bot uses the DeepSeek LLM API for natural language understanding, stores availability and bookings in PostgreSQL, and escalates sensitive or uncertain conversations directly to the candidate.

## Architecture

```
cmd/bot/main.go              – Entry point, wiring, graceful shutdown
internal/domain/             – Shared domain types (Update, Intent, Booking, etc.)
internal/application/        – Business logic (HandleMessage, scheduling, escalation)
internal/infrastructure/     – DB (pgx), LLM (DeepSeek), Telegram API clients
internal/transport/          – HTTP webhook handler
migrations/001_initial.sql   – PostgreSQL schema
Dockerfile                   – Multi-stage build (golang:1.22-alpine → alpine:3.19)
.env.example                 – Environment variable reference
```

## Features

- **Webhook mode** – Telegram sends updates via POST /webhook (validated with X-Telegram-Bot-Api-Secret-Token)
- **Two-phase LLM** – Phase 1 classifies intent (strict JSON), Phase 2 generates natural responses
- **Scheduling** – Lists available slots, matches proposed times, books with SELECT FOR UPDATE to prevent races
- **Escalation** – Forwards to candidate on low confidence (< 0.6), salary/relocation topics, or LLM failure
- **Graceful shutdown** – 30-second drain on SIGINT/SIGTERM
- **Deterministic fallbacks** – Templates used when LLM is unavailable

## Environment Variables

| Variable               | Required | Default | Description                        |
|------------------------|----------|---------|------------------------------------|
| TELEGRAM_BOT_TOKEN     | ✅       |         | Telegram Bot API token             |
| TELEGRAM_SECRET        |          | (none)  | Webhook secret token               |
| DATABASE_URL           | ✅       |         | PostgreSQL connection string       |
| DEEPSEA_API_KEY        |          | (none)  | DeepSeek API key                   |
| CANDIDATE_TELEGRAM_ID  | ✅       |         | Telegram ID to escalate to         |
| LLM_ENABLED            |          | true    | Toggle LLM calls                   |
| PORT                   |          | 8080    | HTTP listen port                   |

Copy `.env.example` to `.env` and fill in your values.

## Running Locally

```bash
cp .env.example .env
# edit .env with your values

# Start PostgreSQL (e.g. via Docker)
docker run -d -e POSTGRES_DB=ilya_bot -e POSTGRES_USER=user -e POSTGRES_PASSWORD=pass \
  -p 5432:5432 postgres:16-alpine

# Run the bot
source .env && go run ./cmd/bot
```

## Docker

```bash
docker build -t ilya_bot .
docker run --env-file .env ilya_bot
```

## Database Schema

The schema is applied automatically on startup. See `migrations/001_initial.sql` for the SQL.

## Running Tests

```bash
go test ./...
```

Tests cover intent escalation logic, booking idempotency, and sensitive topic detection — all using in-memory mocks (no database required).
