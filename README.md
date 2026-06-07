# LuxmedWatcher

Private Telegram bot for watching LuxMed appointment availability.

The current MVP implements:

- Telegram onboarding with admin whitelist and invite codes.
- `en`, `ru`, and `pl` user-facing text from i18n catalogs.
- One Telegram user to one encrypted LuxMed account.
- Button-first Telegram menus for account setup, watch creation, watch list, history, pause/resume/delete, and manual check.
- SQLite persistence for users, invites, LuxMed credentials, watches, found appointment history, and notification history.
- Periodic scheduler with duplicate-notification protection.
- Direct LuxMed API client using the endpoints recovered from the old `origin/develop` branch.
- Docker Compose deployment with a persistent database volume.

## Run Locally

Create a Telegram bot token with BotFather, then prepare env:

```bash
cp .env.example .env
openssl rand -base64 32
```

Put the generated key into `APP_MASTER_KEY`, set `TELEGRAM_BOT_TOKEN`, and add your Telegram numeric id to `ADMIN_TELEGRAM_IDS`.

Run:

```bash
go test ./...
go run ./cmd/luxmed-watcher
```

The bot exposes `GET /healthz` on `HEALTH_ADDR` (`:8080` by default).

## Docker

```bash
docker compose up --build -d
```

The SQLite database is stored in the `luxmed-watcher-data` named volume at `/app/data`, so container rebuilds do not delete bot state.

## Telegram Commands

- `/start` - onboarding or main menu.
- `/menu` - main menu.
- `/new` - create a watch.
- `/watches` - list watches.
- `/history` - found appointment history.
- `/settings` and `/language` - language/settings.
- `/help` - short help.
- `/cancel` - cancel the current flow.
- `/invite [max_uses]` - admin-only invite creation.

## MVP Watch Input Format

The bot is button-first where practical, but MVP city/service/facility lookup accepts explicit LuxMed ids while the full catalog search flow is still small:

```text
1|Warsaw
456|Konsultacja kardiologiczna
123|Dr. Jan Kowalski
all
```

The LuxMed client includes dictionary methods for cities, services, doctors, and facilities, so the next step is to replace these typed ids with paginated search buttons.
