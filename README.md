# LuxmedWatcher

LuxmedWatcher is a private Telegram bot that checks appointment availability in the LuxMed patient portal. Each user connects their own account, sets up a watch and receives a summary when matching slots appear. Book appointments in LuxMed.

## Run

1. Create a Telegram bot with BotFather and run `cp .env.example .env`.
2. Set `TELEGRAM_BOT_TOKEN` and `ADMIN_TELEGRAM_IDS` (comma-separated numeric Telegram user IDs). Use `openssl rand -base64 32` for `APP_MASTER_KEY`. Keep the same key across restarts and backups.
3. Run `docker compose up --build -d`. The Compose volume keeps the SQLite database across container rebuilds.

To run without Docker, install Go 1.26.11 or newer and load the env file into your shell:

```sh
set -a
. ./.env
set +a
go run ./cmd/luxmed-watcher
```

Use one running watcher per database and Telegram token. The readiness probe is `GET /healthz` at `HEALTH_ADDR` (default `:8080`). It checks the database connection.

### PostgreSQL

Set `DATABASE_URL` to a PostgreSQL URL to select PostgreSQL. If the URL is empty, `DATABASE_PATH` selects the SQLite file. Use an existing database with a dedicated role that can create and update tables in its schema. For a remote database, use TLS with server verification, for example `sslmode=verify-full`. The app creates and updates its schema on startup.

Selecting another database starts from the data already in that database. Keep the SQLite volume if it contains accounts or watches you want to retain. Back up the database and keep `APP_MASTER_KEY` separately.

Set `RECONNECT_JOURNAL_DIR` to an application-owned, writable directory on a persistent volume. It holds confirmed Telegram refusals and queued unblock events across a restart. The app first uses `<DATABASE_PATH>.reconnect` by default. If that path is unavailable with PostgreSQL, it uses a database-specific directory under `$XDG_STATE_HOME/luxmed-watcher` or `~/.local/state/luxmed-watcher`. In containers, mount a persistent volume at the configured directory to preserve it across replacement.

PostgreSQL can still start if both default paths are unavailable, or with `RECONNECT_JOURNAL_DIR=-` to disable the local journal. During a database outage the process keeps confirmed refusals and unblock events in memory and retries them while it runs. If it crashes before recording the outcome durably, an outstanding delivery claim stays paused after restart because Telegram may already have accepted it.

## Telegram workflow

- Open the bot in a private chat and use `/start`. Admins can issue a limited-use invite with `/invite [max_uses]`.
- Connect your LuxMed account, then use `/new` to choose a city, service, doctor or clinic filters and a check interval.
- Use `/watches` to review, change, pause or manually check a watch. `/history` opens appointment history. `/logout_luxmed` removes the saved credentials and pauses active watches.
- Open LuxMed from an alert to view or book the appointment. If the portal requires a fresh sign-in, a questionnaire or a referral, finish that step in LuxMed. A partial search can still report the slots it verified.

The default minimum between checks of the same connected account is two minutes. `DATA_RETENTION_DAYS` defaults to 180 for appointment and notification history. Set it to 0 to disable cleanup.

## Architecture and verification

A single Go process runs the scheduler. LuxMed supplies slots through the HTTP client, storage supplies the same repository on SQLite or PostgreSQL, and Telegram handles interactions and notification delivery. The app assembles these adapters at startup.

```sh
go test -race ./...
go vet ./...
```

Set `TEST_DATABASE_URL` to run storage integration tests against PostgreSQL. Tests create and remove isolated schemas and require a role with `CREATE SCHEMA` permission. The Telegram and LuxMed tests use synthetic data and local HTTP fixtures. [LuxMed API notes](docs/LUXMED_API_NOTES.md)
