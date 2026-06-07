# LuxMed Watcher Telegram Bot — Product and Implementation Plan for Codex

## 0. Context

This repository is intended to become a private Telegram bot that monitors LuxMed appointment availability for authorized users and sends notifications when matching appointments appear.

The target is not a public SaaS product. It is a small, reliable, secure household/friends product with clean internals, so that it can later be extended without rewriting everything.

Important implementation instruction for Codex:

1. Inspect the whole repository, all branches, git history, and local files before coding.
2. Reuse any existing LuxMed API knowledge already present in this repository: login URLs, search endpoints, DTOs, parsed date formats, mock JSON, and previous Go code.
3. If there is a previous Go branch, treat it as a source of LuxMed API fixtures and behavior, not necessarily as the final architecture.
4. Do not hardcode real LuxMed credentials, Telegram bot token, invite codes, or personal medical data.
5. Prefer a working MVP over framework-heavy architecture.

## 1. Confirmed product decisions

These decisions are already made and must be implemented unless explicitly changed later.

1. UI must support multiple languages from day one.
2. All user-facing text must live in i18n files, not inline in handlers.
3. MVP account model: one Telegram user maps to one LuxMed account.
4. Do not support LuxMed dependents / children / family profiles in MVP.
5. A watch continues running until the user manually cancels, pauses, deletes, or stops it.
6. User can configure the watch check interval, but the app must enforce an admin/global minimum interval.
7. One watch supports either any doctor or one exact doctor. Multiple doctors in one watch are not MVP.
8. Store history of found appointments and notifications.
9. Telegram UX must be button-first: the user should mostly click buttons instead of typing.
10. Docker deployment must preserve the database across restarts, updates, and container rebuilds.

## 2. Product goal

Build a Telegram bot where an authorized user can:

1. Join the bot using an invite code or admin whitelist.
2. Choose UI language.
3. Connect their LuxMed credentials securely.
4. Create one or more appointment watches.
5. Define what they want:
   - city;
   - medical service / specialization;
   - exact doctor or any doctor;
   - exact service with doctor or generic service;
   - all facilities or selected LuxMed facilities;
   - date range;
   - preferred weekdays and time ranges;
   - check interval.
6. Let the bot periodically check LuxMed.
7. Receive Telegram notifications when matching appointments appear.
8. See history of found appointments.
9. Stop, pause, resume, edit, or delete a watch from Telegram buttons.
10. Open LuxMed manually from the notification to book the appointment.

MVP must solve the real problem: a user can say, through Telegram UI, “I need this doctor/service/city/facilities/time window,” and the bot actually monitors LuxMed and reports real results.

## 3. Non-goals for MVP

Do not implement these in MVP unless the basic watcher is already stable:

1. Automatic booking of visits.
2. CAPTCHA / MFA bypassing.
3. Public registration without invite/admin approval.
4. Payment system.
5. Multi-tenant SaaS admin panel.
6. Telegram Mini App before the regular button-first bot flow works.
7. Browser automation if direct LuxMed API calls still work.
8. Multiple LuxMed accounts per Telegram user.
9. LuxMed dependents / children profiles.
10. Multiple exact doctors in one watch.

If LuxMed requires MFA, implement a user-driven flow: ask the user to paste the MFA code into Telegram. Do not bypass it.

## 4. Recommended stack

Use Go unless there is a strong reason not to, because this repository was already started as a Go learning project and Go is suitable for long-running bots, schedulers, HTTP clients, and simple deployment.

Recommended stack:

- Language: Go 1.23+ or latest stable available in CI.
- Telegram library: `github.com/go-telegram-bot-api/telegram-bot-api/v5` or another maintained library selected by Codex after checking current maintenance.
- Storage for MVP: SQLite with migrations.
- Storage extension path: Postgres-compatible schema later.
- SQL access: `sqlc` or simple repository layer. Prefer clarity over framework magic.
- Migrations: `golang-migrate` or embedded migration runner.
- Config: env vars + optional config file.
- Runtime: single binary + Docker image.
- Deployment: Docker Compose.
- Observability: structured logs, health endpoint, Prometheus metrics if cheap.
- i18n: simple file-based locale catalog, e.g. YAML/JSON/TOML.

Why SQLite first:

- One private bot instance.
- Simple deployment on VPS/Raspberry Pi.
- No extra database service required.
- Can preserve data through a Docker bind mount or named volume.
- Can still be clean if migrations and repository interfaces are used.

If Codex sees that Postgres is already used in the repo, it may keep Postgres. Do not spend time migrating storage if it delays a working bot.

## 5. Architecture

Use a modular monolith. Avoid microservices.

Suggested package layout:

```text
cmd/luxmed-watcher/
  main.go

internal/app/
  app.go                  # wiring, lifecycle, graceful shutdown
  config.go               # env/config parsing

internal/bot/
  telegram.go             # Telegram client adapter
  router.go               # command/callback routing
  keyboards.go            # inline/reply keyboard builders
  callback.go             # callback data parsing/building
  flows/                  # conversational flows / state machine
    onboarding.go
    language.go
    credentials.go
    create_watch.go
    edit_watch.go
    list_watches.go
    history.go
    admin.go

internal/i18n/
  catalog.go              # loads translations
  locale.go               # user locale detection/validation
  format.go               # localized formatting helpers
  locales/
    en.yaml
    ru.yaml
    pl.yaml

internal/domain/
  user.go
  invite.go
  watch.go
  appointment.go
  catalog.go
  history.go
  errors.go

internal/luxmed/
  client.go               # high-level LuxMed API interface
  auth.go                 # login/session/token refresh
  appointments.go         # search endpoints
  catalog.go              # cities/services/doctors/facilities
  dto.go                  # LuxMed API DTOs
  parser.go               # date/time parsing, normalization
  fixtures/               # mock LuxMed responses

internal/scheduler/
  scheduler.go            # periodic due-watch runner
  runner.go               # executes one watch check
  dedupe.go               # notification fingerprinting

internal/storage/
  db.go
  migrations/
  users.go
  invites.go
  credentials.go
  watches.go
  catalog.go
  history.go
  notifications.go

internal/security/
  crypto.go               # credentials encryption/decryption
  secrets.go              # no logging of secrets

internal/observability/
  logger.go
  metrics.go
  health.go

tests/
  e2e/
  integration/

docs/
  PRODUCT_PLAN_CODEX.md
  LUXMED_API_NOTES.md
  OPERATIONS.md
```

Core rule: Telegram, LuxMed, scheduler, i18n, and storage must be adapters around domain logic. Do not put all logic into Telegram handlers.

## 6. i18n requirements

Implement i18n from the first milestone.

Required locales for MVP:

- `en`
- `ru`
- `pl`

Default locale:

- Use Telegram language code if it is one of the supported locales.
- Otherwise use `ru` unless configured differently via `DEFAULT_LOCALE`.

All user-facing text must be referenced by message keys, not hardcoded in handlers.

Example structure:

```yaml
menu.main.title: "Main menu"
menu.main.new_watch: "➕ New watch"
menu.main.my_watches: "📋 My watches"
watch.create.city.title: "Choose city"
watch.create.confirm.title: "Confirm watch"
notification.appointment_found.title: "Found LuxMed appointment"
errors.auth_failed: "Login failed. Check login and password."
```

Rules:

1. Callback data must not contain localized text.
2. Tests should fail if a required key is missing in any supported locale.
3. Localized formatting must support dates/times and plural-like messages where needed.
4. Add `/language` command and language selection buttons in settings.
5. User locale must be stored in DB.

## 7. Main domain model

### User

Fields:

- `id`
- `telegram_user_id`
- `telegram_chat_id`
- `telegram_username`
- `display_name`
- `locale`: `en`, `ru`, `pl`
- `status`: `pending_invite`, `active`, `blocked`
- `role`: `user`, `admin`
- `created_at`, `updated_at`, `last_seen_at`

### Invite code

Fields:

- `id`
- `code_hash`
- `created_by_user_id`
- `max_uses`
- `used_count`
- `expires_at`
- `disabled_at`
- `created_at`

Invite code should be stored hashed, not plaintext.

### LuxMed account

MVP rule: exactly one active LuxMed account per Telegram user.

Fields:

- `id`
- `user_id`
- `login`
- `encrypted_password`
- `encrypted_session_data`
- `session_expires_at`
- `last_login_at`
- `last_login_error`
- `status`: `not_configured`, `active`, `auth_failed`, `mfa_required`, `disabled`

Credentials must be encrypted at rest using an application master key from env.

### Watch request

Fields:

- `id`
- `user_id`
- `name`
- `status`: `active`, `paused`, `deleted`
- `city_id`, `city_name`
- `service_id`, `service_name`
- `doctor_id`, `doctor_name`, nullable
- `doctor_mode`: `any`, `exact`
- `facility_mode`: `all`, `selected`
- `date_from`, `date_to`, or `next_days`
- `time_windows`: JSON, e.g. weekdays + hour ranges
- `check_interval_seconds`
- `last_checked_at`
- `last_success_at`
- `last_error`
- `created_at`, `updated_at`

Do not use `completed` as an automatic status in MVP. Watches must keep running until the user manually pauses, deletes, or stops them.

### Appointment result / history

Store history of found appointments.

Fields:

- `id`
- `watch_id`
- `external_id` if LuxMed provides one
- `fingerprint`: stable hash from appointment date/time + doctor + facility + service
- `date_time`
- `service_id`, `service_name`
- `doctor_id`, `doctor_name`
- `facility_id`, `facility_name`, `address`
- `city_id`, `city_name`
- `booking_url` if available
- `first_seen_at`
- `last_seen_at`
- `last_notified_at`
- `seen_count`
- `status`: `new`, `seen_again`, `notified`, `expired`, `hidden`
- `raw_payload` only if explicitly enabled for debug mode

History is used for:

- user-visible “Found history” screen;
- deduplication;
- debugging missed notifications;
- later analytics.

### Notification history

Fields:

- `id`
- `watch_id`
- `appointment_history_id`
- `appointment_fingerprint`
- `sent_at`
- `message_id`
- `status`: `sent`, `failed`
- `error`

Used to prevent spam and duplicate notifications.

## 8. Access control / onboarding

MVP should support both mechanisms:

1. Admin whitelist via env:
   - `ADMIN_TELEGRAM_IDS=123,456`
   - Admins can always use the bot.
2. Invite codes:
   - Admin creates an invite from Telegram using buttons or command.
   - Bot returns a one-time or limited-use code.
   - New user sends `/start <code>` or enters code in the bot.
   - User becomes active after code validation.

Recommended flow:

1. Unknown user opens bot.
2. Bot detects Telegram language and shows language buttons.
3. Bot asks for invite code.
4. If valid, create active user.
5. Then show main menu.
6. Admin can block users.

Do not rely only on hardcoded user IDs. Invite codes are more flexible. Keep admin whitelist as a bootstrap mechanism.

## 9. Telegram UX

Use regular Telegram bot UI for MVP. Mini App is optional Phase 2.

The UX must be button-first. Users should mostly choose options by pressing inline buttons. Text input is allowed only where it is genuinely useful: invite code, LuxMed login/password, search query, custom date/time, or custom interval.

Required commands:

- `/start` — onboarding or main menu.
- `/menu` — main menu.
- `/new` — create watch.
- `/watches` — list watches.
- `/history` — found appointment history.
- `/settings` — user settings.
- `/language` — change language.
- `/help` — short help.
- `/cancel` — cancel current flow.

Main menu buttons:

- `➕ New watch`
- `📋 My watches`
- `🕘 Found history`
- `🔐 LuxMed account`
- `⚙️ Settings`
- `❓ Help`

Use localized labels from i18n files.

### Button-first create watch flow

Target UX:

1. User clicks `New watch`.
2. Bot asks city.
   - Show popular cities as buttons.
   - Provide `Search city` button.
3. Bot asks service/specialization.
   - Show `Search service` button.
   - User types partial text only after choosing search.
   - Bot shows top 5-10 matches as buttons.
4. Bot asks doctor mode.
   - `Any doctor`
   - `Specific doctor`
5. If specific doctor:
   - Show `Search doctor` button.
   - User types doctor name.
   - Bot shows matches as buttons.
6. Bot asks facilities.
   - `All in selected city`
   - `Choose facilities`
7. If choose facilities:
   - Show paginated checklist-style inline buttons.
   - Each click toggles selection.
   - Selected facilities should be visually marked with `✅`.
   - Buttons: `Next page`, `Previous page`, `Done`, `Select all`, `Clear`.
8. Bot asks date range.
   - `Next 7 days`, `Next 14 days`, `Next 30 days`, `Custom`.
9. Bot asks time preference.
   - `Any time`
   - `Morning`
   - `Afternoon`
   - `Evening`
   - `Custom hours`
10. Bot asks weekdays.
   - `Any day`
   - checklist-style weekdays.
11. Bot asks check interval.
   - Buttons: `2 min`, `3 min`, `5 min`, `10 min`, `Custom`.
   - Enforce global minimum to avoid hammering LuxMed.
12. Bot shows summary and asks confirmation.
13. On confirm, create active watch and schedule first check.

Every step must include:

- `Back`
- `Cancel`
- current progress indicator, e.g. `Step 3/8`

### Watch list UX

For each watch show:

- name;
- status;
- target service/doctor;
- city/facilities;
- date/time filters;
- interval;
- last check status;
- last found result count.

Buttons:

- `Pause`
- `Resume`
- `Edit`
- `Check now`
- `History`
- `Delete`

### History UX

User can open `Found history` from main menu or per-watch.

History screen requirements:

- Show recent found appointments, newest first.
- Allow filter by watch.
- Show whether a result was already notified.
- Show `Open LuxMed` if a booking URL exists.
- Show `Hide` button for old/noisy result.
- Add pagination: `Next`, `Previous`.

### Notification UX

When a matching appointment appears, send message like:

```text
Found LuxMed appointment

Watch: Onkolog — Warsaw
Date: 2026-06-12 14:30
Doctor: Dr. Jan Kowalski
Facility: LuxMed Example, Street 1
Service: Konsultacja onkologiczna

This result matches your filters.
```

Buttons:

- `Open LuxMed`
- `Stop this watch`
- `Pause this watch`
- `Show watch`
- `History`
- `Check again`

Do not send duplicate notifications for the same appointment fingerprint unless it disappeared and reappeared after a configurable cooldown. Still store the reappearance in history by updating `last_seen_at` and `seen_count`.

## 10. Telegram Mini App decision

Do not build Mini App in MVP unless regular Telegram flow becomes too painful.

Reason:

- A Mini App needs a hosted web frontend, HTTPS, frontend build pipeline, and Telegram WebApp validation.
- Regular Telegram inline keyboards are enough for the first working version.
- The hard part is LuxMed search reliability, not UI polish.

Phase 2 Mini App can be useful for:

- facility multi-select with search;
- complex time windows;
- dashboard of watches;
- richer history view;
- credential setup with better form UX.

If Mini App is added later, keep it as another adapter on top of the same backend and domain logic.

## 11. LuxMed integration

Codex must inspect existing repo branches/fixtures first. Preserve all known working endpoints and parsing rules.

Implement LuxMed integration behind this interface:

```go
type Client interface {
    Login(ctx context.Context, credentials Credentials) (Session, error)
    RefreshSession(ctx context.Context, session Session) (Session, error)
    SearchCities(ctx context.Context, query string) ([]City, error)
    SearchServices(ctx context.Context, query ServiceQuery) ([]Service, error)
    SearchDoctors(ctx context.Context, query DoctorQuery) ([]Doctor, error)
    ListFacilities(ctx context.Context, cityID string) ([]Facility, error)
    SearchAppointments(ctx context.Context, query AppointmentQuery, session Session) ([]Appointment, error)
}
```

Important behavior:

- Keep LuxMed DTOs separate from normalized domain models.
- Log endpoint, status code, and request ID if available.
- Never log username, password, full token, cookies, or personal medical data.
- Handle session expiration with one retry after refresh/login.
- Add rate limiting per LuxMed account.
- Add backoff on 429/5xx/network errors.
- Treat LuxMed schema changes as explicit errors with saved fixture for debugging.

If LuxMed API changed, first update `docs/LUXMED_API_NOTES.md` with observed endpoints and payload shapes before coding around it.

## 12. Scheduler

Implement an internal scheduler, not cron-only.

Requirements:

- Poll active watches based on `next_check_at` / `last_checked_at`.
- Allow per-watch interval selected by user.
- Enforce global minimum interval, e.g. `MIN_CHECK_INTERVAL_SECONDS=120`.
- Enforce max concurrent LuxMed requests.
- Avoid concurrent checks for the same LuxMed account.
- Save every run result: success, no matches, auth failed, transient error.
- Store every matching appointment in history.
- Allow `Check now` from Telegram, but still rate-limit it.
- Continue working after restart.

Simple algorithm:

1. Every `SCHEDULER_TICK_SECONDS`, load due active watches.
2. Group by LuxMed account/user.
3. Run checks with bounded concurrency.
4. For each result, normalize appointments.
5. Filter appointments by date/time/facility/doctor.
6. Upsert found appointments into history.
7. Deduplicate notifications against history/notification tables.
8. Send notifications.
9. Update watch state and next check time.

## 13. Credentials and security

Required:

- `TELEGRAM_BOT_TOKEN` from env.
- `APP_MASTER_KEY` from env; used to encrypt LuxMed passwords/session data.
- `ADMIN_TELEGRAM_IDS` from env.
- No secrets in git.
- No credentials in logs.
- Redaction middleware for logs.
- `/logout_luxmed` or `Remove LuxMed account` button deletes encrypted credentials and sessions.

Encryption approach:

- Use AES-GCM or libsodium secretbox.
- Master key must be 32 bytes after decoding.
- Store nonce with ciphertext.
- Add unit tests for encryption/decryption and wrong-key failure.

User privacy:

- Keep raw LuxMed API payloads only in dev/test by default.
- In production, store normalized appointment history and notification history.
- Add `DATA_RETENTION_DAYS` for optional cleanup.
- Default retention can be long enough for practical use, e.g. 180 days, but configurable.

## 14. Configuration

Required env vars:

```env
TELEGRAM_BOT_TOKEN=
APP_MASTER_KEY=
ADMIN_TELEGRAM_IDS=
DB_PATH=/data/luxmed-watcher.db
DEFAULT_LOCALE=ru
SUPPORTED_LOCALES=en,ru,pl
DEFAULT_CHECK_INTERVAL_SECONDS=180
MIN_CHECK_INTERVAL_SECONDS=120
SCHEDULER_TICK_SECONDS=30
MAX_CONCURRENT_CHECKS=3
LOG_LEVEL=info
```

Optional:

```env
LUXMED_BASE_URL=
LUXMED_USER_AGENT=
HTTP_TIMEOUT_SECONDS=20
DATA_RETENTION_DAYS=180
SAVE_RAW_LUXMED_PAYLOADS=false
DEV_LUXMED_LOGIN=
DEV_LUXMED_PASSWORD=
```

Dev credentials are only for local manual smoke tests, never for CI.

## 15. Docker deployment and persistence

Codex must add production-ready Docker deployment docs.

Required files:

```text
Dockerfile
docker-compose.yml
.env.example
docs/OPERATIONS.md
```

Recommended Docker Compose shape:

```yaml
services:
  luxmed-watcher:
    build: .
    container_name: luxmed-watcher
    restart: unless-stopped
    env_file:
      - .env
    volumes:
      - ./data:/data
    healthcheck:
      test: ["CMD", "/app/luxmed-watcher", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
```

Persistence rules:

1. SQLite DB must live under `/data`, not inside the container filesystem.
2. Default `DB_PATH` must be `/data/luxmed-watcher.db`.
3. `./data` must be gitignored.
4. Restarting the container must not lose data.
5. Rebuilding/updating the image must not lose data.
6. Migrations must run automatically on startup and be idempotent.
7. `docs/OPERATIONS.md` must include backup and restore commands.

Minimum operations doc content:

```bash
# first deploy
cp .env.example .env
mkdir -p data
docker compose up -d --build

# update without losing DB
git pull
docker compose up -d --build

# backup DB
sqlite3 ./data/luxmed-watcher.db ".backup './backup/luxmed-watcher-$(date +%F-%H%M).db'"

# restore DB
cp ./backup/luxmed-watcher-YYYY-MM-DD-HHMM.db ./data/luxmed-watcher.db
docker compose restart luxmed-watcher

# logs
docker compose logs -f luxmed-watcher
```

If the final implementation uses Postgres instead of SQLite, `docker-compose.yml` must use a named volume for Postgres data and `OPERATIONS.md` must include `pg_dump`/restore commands.

## 16. Tests

Do not overbuild tests, but add the tests that prevent the bot from silently lying.

Minimum useful test suite:

### Unit tests

- i18n catalog loading;
- missing translation key detection;
- invite code validation;
- watch filter matching;
- time window matching;
- appointment fingerprint generation;
- history upsert / seen count logic;
- deduplication logic;
- LuxMed date/time parsing;
- service/doctor/facility search ranking;
- credentials encryption/decryption;
- Telegram callback data parsing.

### Integration tests

- LuxMed client against fake HTTP server with fixtures;
- login/session refresh behavior;
- appointment search parsing;
- scheduler runs due watches and sends notification via fake Telegram client;
- history is written before notification is sent;
- storage migrations and repositories.

### End-to-end tests with mocks

Script a full fake flow:

1. user starts bot;
2. chooses language;
3. enters invite code;
4. configures fake LuxMed credentials;
5. creates watch mostly through buttons;
6. fake LuxMed server returns appointment;
7. fake Telegram client receives notification;
8. appointment is visible in history;
9. repeated scheduler run does not duplicate notification;
10. user clicks `Stop this watch`;
11. watch becomes paused/deleted depending on chosen action.

### Live smoke test

Optional, not in default CI:

- enabled only when `RUN_LIVE_LUXMED_TESTS=true` and credentials are present;
- performs login and one harmless search;
- does not book appointments.

## 17. CI / quality gates

Add GitHub Actions or equivalent:

- `gofmt` check;
- `go vet`;
- unit tests;
- integration tests with fake server;
- race test for scheduler package if feasible;
- build Docker image.

Do not require live LuxMed credentials in CI.

## 18. Milestones

### Milestone 0 — Repository discovery and cleanup

Deliverables:

- inspect all branches and existing code;
- document current LuxMed API findings in `docs/LUXMED_API_NOTES.md`;
- add clean project structure;
- add config loading;
- add i18n catalog with `en`, `ru`, `pl`;
- add Docker Compose skeleton with persistent `/data` volume;
- add README quickstart.

Acceptance:

- app starts locally;
- config validation fails clearly when required env vars are missing;
- i18n catalog validates all required keys;
- `/data` is used for SQLite DB path.

### Milestone 1 — Telegram shell, i18n, and access control

Deliverables:

- Telegram bot starts;
- `/start`, `/menu`, `/language`, `/help`;
- language selection buttons;
- admin bootstrap from env;
- invite code creation and redemption;
- user state stored in DB.

Acceptance:

- unknown user cannot use bot without invite;
- admin can generate invite;
- invited user sees localized main menu;
- user can change language.

### Milestone 2 — Secure LuxMed account setup

Deliverables:

- user can enter LuxMed login/password;
- credentials encrypted at rest;
- exactly one LuxMed account per Telegram user;
- login is tested against LuxMed or fake server;
- auth failure shown clearly;
- remove credentials flow.

Acceptance:

- user can connect LuxMed account;
- app never logs password/token;
- wrong password produces a localized Telegram error.

### Milestone 3 — Catalog search

Deliverables:

- sync or fetch cities/services/doctors/facilities;
- local cache;
- text search with top matches;
- button-based search result selection;
- facility pagination/select-all flow.

Acceptance:

- user can type partial service name and choose a mapped LuxMed service ID;
- user can choose all facilities or selected facilities using buttons.

### Milestone 4 — Create/list/edit watches

Deliverables:

- complete button-first `New watch` flow;
- list watches;
- pause/resume/delete;
- user-selected check interval with global minimum enforcement;
- summary confirmation before saving.

Acceptance:

- user can create a watch for service + city + optional exact doctor + facility/time filters;
- watch persists after restart;
- watch keeps running until user manually stops/pauses/deletes it.

### Milestone 5 — Scheduler, history, and notifications

Deliverables:

- due watch runner;
- LuxMed appointment search;
- filtering;
- appointment history upsert;
- notification dedupe;
- Telegram notifications with buttons;
- `Stop this watch` from notification;
- `Found history` screens.

Acceptance:

- fake LuxMed fixture returns appointment;
- bot stores appointment in history;
- bot sends exactly one notification;
- repeated scheduler run updates `last_seen_at` / `seen_count` and does not spam duplicates;
- history is visible from Telegram;
- stop button disables watch.

### Milestone 6 — Deployment and operations

Deliverables:

- Dockerfile;
- docker-compose.yml;
- `.env.example`;
- `docs/OPERATIONS.md`;
- backup/restore instructions for SQLite DB;
- metrics/logging;
- healthcheck.

Acceptance:

- clean deployment on VPS/Raspberry Pi using Docker Compose;
- restart does not lose watches/history;
- rebuild/update does not lose watches/history;
- migrations are automatic and safe;
- logs are enough to debug auth/search errors.

### Milestone 7 — Optional Telegram Mini App

Deliverables:

- small web frontend only if needed;
- Telegram WebApp validation;
- watch create/edit UI;
- richer history UI;
- same backend API and domain logic.

Acceptance:

- Mini App can create/edit watches;
- bot still works without Mini App.

## 19. Deep links / opening LuxMed

MVP should include `Open LuxMed` button, but do not rely on guaranteed iOS app opening.

Implementation options:

1. If LuxMed exposes a web booking URL, use it.
2. If LuxMed uses universal links, the same HTTPS link may open the native app on iOS.
3. If no stable deep link exists, open the LuxMed website and include all appointment details in the Telegram message.
4. Later investigate iOS Shortcuts only as a convenience, not as a core booking path.

Acceptance:

- notification always contains enough data to manually find the appointment in LuxMed app.
- link is best-effort only.

## 20. Admin functions

Admin menu:

- create invite code;
- list active users;
- block/unblock user;
- see scheduler status;
- run catalog sync;
- see recent errors;
- trigger test notification to self;
- see storage path and DB status;
- create manual DB backup if feasible.

Do not expose admin functions to normal users.

## 21. Error handling

User-facing errors must be actionable and localized:

- LuxMed auth failed: “Login failed. Check login/password.”
- LuxMed session expired: silently retry once, then ask user to reconnect.
- LuxMed unavailable: “LuxMed did not respond. I will retry later.”
- No services found: ask user to change search text.
- No appointments found: do not notify every time; only update watch status.
- Rate limited: back off and show status in watch details.
- Interval too low: show minimum allowed interval.

Internal errors must include enough technical context without secrets.

## 22. Codex implementation rules

1. Make small commits by milestone.
2. Keep code simple and idiomatic.
3. Add tests around behavior, not framework internals.
4. Use interfaces only where there is a real external boundary: Telegram, LuxMed, storage, clock, i18n.
5. Use fixtures for LuxMed payloads.
6. Do not hide failures. Surface broken LuxMed parsing in logs/tests.
7. Keep Telegram callback data versioned and compact.
8. Do not store raw medical data unless explicitly enabled.
9. Prefer working MVP over perfect architecture.
10. Update README after each milestone.
11. Do not inline user-facing text in code; use i18n keys.
12. Do not store SQLite DB inside the Docker image/container filesystem.

## 23. Final MVP defaults

Use these defaults unless the product owner changes them:

1. One Telegram user = one LuxMed account.
2. Notifications only in private chat.
3. Invite code can be one-time or limited-use; admin chooses.
4. Admin enforces global minimum polling interval.
5. User can choose interval above minimum.
6. Watch continues after notification until user pauses/stops/deletes it.
7. One exact doctor per watch; multiple doctors require multiple watches.
8. Facilities are searchable and paginated; grouping later.
9. Bot UI supports `en`, `ru`, `pl` from day one.
10. LuxMed dependents are not MVP.
11. Store found appointment history and notification history.
12. SQLite DB is stored in `/data/luxmed-watcher.db` and preserved through Docker volumes.
