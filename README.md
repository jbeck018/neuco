# Neuco

Neuco is an AI-native product intelligence platform that turns product signals into specs, code, and GitHub pull requests.

## Features

Neuco combines ingestion, synthesis, and delivery into one workflow:

- **Signal ingestion** from support tickets, calls/transcripts, Slack, Linear, Jira, webhooks, CSV, and more
- **Signal synthesis** into prioritized feature candidates
- **Spec generation** from candidate context and product memory
- **Code generation** with repository-aware implementation context
- **GitHub PR creation** to propose implementation changes automatically

## Tech Stack

- **Backend:** Go 1.25
- **Frontend:** SvelteKit 5
- **Database:** PostgreSQL 16 + `pgvector`
- **Job system:** River (Postgres-backed queues)

## Prerequisites

Install before starting:

- Go **1.25+**
- Node.js **20+**
- Docker + Docker Compose
- `pnpm`

You may also want these for local DX:

- `air` (used by `make run-api` / `make run-worker`)
- `migrate` CLI (used by `make migrate-up` / `make migrate-down`)

## Quick Start

1. **Start local infra**

   ```bash
   docker compose up -d postgres redis nango-server mailhog
   ```

2. **Configure environment**

   Create your local env file(s) as needed (for API/worker/frontend), including at least:

   - `DATABASE_URL`
   - `JWT_SECRET`
   - `INTERNAL_API_TOKEN`

   A common local DB URL with docker compose:

   ```bash
   export DATABASE_URL='postgres://neuco:neuco@localhost:5432/neuco?sslmode=disable'
   ```

3. **Run DB migrations**

   ```bash
   make migrate-up
   ```

4. **Run API**

   ```bash
   make run-api
   ```

5. **Run worker**

   ```bash
   make run-worker
   ```

6. **Run frontend (new terminal)**

   ```bash
   cd neuco-web
   pnpm install
   pnpm dev
   ```

API defaults to `http://localhost:8080` and frontend dev usually runs on `http://localhost:5173`.

## Development Commands

From the project root:

```bash
# Run API / worker with Air
make run-api
make run-worker

# Run API / worker without Air
make run-api-no-air
make run-worker-no-air

# Migrations
make migrate-up
make migrate-down

# Generate code/types
make gen

# Test / build / lint
make test
make build
make lint

# Local helper (starts Postgres via compose)
make dev

# Docker image builds
make docker-build-api
make docker-build-worker

# Cleanup
make clean
```

## Architecture

For system design, service boundaries, queue topology, route map, and data model details, see **[ARCH.md](./ARCH.md)**.

## Deployment

For production and preview deployment details, see **[DEPLOY.md](./DEPLOY.md)**.

## License

TBD.
