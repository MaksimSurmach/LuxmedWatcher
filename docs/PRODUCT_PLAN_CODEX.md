# LuxMed Watcher Telegram Bot — Product and Implementation Plan for Codex

## 0. Context

This repository is intended to become a private Telegram bot that monitors LuxMed appointment availability for authorized users and sends notifications when matching appointments appear.

The current target is not a public SaaS product. It is a small, reliable, secure household/friends product with clean internals, so that it can later be extended without rewriting everything.

Important implementation instruction for Codex:

1. Inspect the whole repository, all branches, git history, and local files before coding.
2. Reuse any existing LuxMed API knowledge already present in this repository: login URLs, search endpoints, DTOs, parsed date formats, mock JSON, and previous Go code.
3. If there is a previous Go branch, treat it as a source of LuxMed API fixtures and behavior, not necessarily as the final architecture.
4. Do not hardcode real LuxMed credentials, Telegram bot token, invite codes, or personal medical data.

## 1. Product goal

Build a Telegram bot where an authorized user can:

1. Join the bot using an invite code or admin whitelist.
2. Connect their LuxMed credentials securely.
3. Create one or more appointment watches.
4. Define what they want:
   - city;
   - medical service / specialization;
   - exact doctor or any doctor;
   - exact service with doctor or generic service;
   - all facilities or selected LuxMed facilities;
   - date range;
   - preferred weekdays and time ranges.
5. Let the bot periodically check LuxMed every configurable interval.
6. Receive a Telegram notification when matching appointments appear.
7. Stop, pause, resume, edit, or delete a watch from Telegram buttons.
8. Open LuxMed manually from the notification to book the appointment.

MVP must solve the real problem: a user can say, through Telegram UI, “I need this doctor/service/city/facilities/time window,” and the bot actually monitors LuxMed and reports real results.

## 2. Non-goals for MVP

Do not implement these in MVP unless the basic watcher is already stable:

1. Automatic booking of visits.
2. CAPTCHA / MFA bypassing.
3. Public registration without invite/admin approval.
4. Payment system.
5. Multi-tenant SaaS admin panel.
6. Complex Telegram Mini App UI before the regular bot flow works.
7. Browser automation if direct LuxMed API calls still work.

If LuxMed requires MFA, implement a user-driven flow: ask the user to paste the MFA code into Telegram. Do not bypass it.

## 3. Recommended stack

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

Why SQLite first:

- One private bot instance.
- Simple deployment on VPS/Raspberry Pi.
- No extra database service required.
- Can still be clean if migrations and repository interfaces are used.

If Codex sees that Postgres is already used in the repo, it may keep Postgres. Do not spend time migrating storage if it delays a working bot.

## 4. Architecture

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
  flows/                  # conversational flows / state machine
    onboarding.go
    credentials.go
    create_watch.go
    edit_watch.go
    list_watches.go

internal/domain/
  user.go
  invite.go
  watch.go
  appointment.go
  catalog.go
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

Core rule: Telegram, LuxMed, scheduler, and storage must be adapters around domain logic. Do not put all logic into Telegram handlers.

## 5. Main domain model

### User

Fields:

- `id`
- `telegram_user_id`
- `telegram_chat_id`
- `telegram_username`
- `display_name`
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
- `status`: `active`, `paused`, `completed`, `deleted`
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

### Appointment result

Normalized fields:

- `external_id` if LuxMed provides one
- `fingerprint`: stable hash from appointment date/time + doctor + facility + service
- `date_time`
- `service_id`, `service_name`
- `doctor_id`, `doctor_name`
- `facility_id`, `facility_name`, `address`
- `city_id`, `city_name`
- `booking_url` if available
- `raw_payload` for debugging, with personal data minimized

### Notification history

Fields:

- `id`
- `watch_id`
- `appointment_fingerprint`
- `sent_at`
- `message_id`
- `status`: `sent`, `failed`

Used to prevent spam and duplicate notifications.

## 6. Access control / onboarding

MVP should support both mechanisms:

1. Admin whitelist via env:
   - `ADMIN_TELEGRAM_IDS=123,456`
   - Admins can always use the bot.
2. Invite codes:
   - Admin creates an invite from Telegram: `/invite 1 7d` or button flow.
   - Bot returns a one-time or limited-use code.
   - New user sends `/start <code>` or enters code in the bot.
   - User becomes active after code validation.

Recommended flow:

1. Unknown user opens bot.
2. Bot shows: “Enter invite code.”
3. If valid, create active user.
4. Then show main menu.
5. Admin can block users.

Do not rely only on hardcoded wife/user IDs. Invite codes are more flexible. Keep admin whitelist as a bootstrap mechanism.

## 7. Telegram UX

Use regular Telegram bot UI for MVP. Mini App is optional Phase 2.

Required commands:

- `/start` — onboarding or main menu.
- `/menu` — main menu.
- `/new` — create watch.
- `/watches` — list watches.
- `/settings` — user settings.
- `/help` — short help.
- `/cancel` — cancel current flow.

Main menu buttons:

- `➕ New watch`
- `📋 My watches`
- `🔐 LuxMed account`
- `⚙️ Settings`
- `❓ Help`

### Create watch flow

Target UX:

1. User clicks `New watch`.
2. Bot asks city.
   - Show popular cities as buttons.
   - Allow text search.
3. Bot asks service/specialization.
   - User types partial text: `onkolog`, `dermatolog`, `USG`, etc.
   - Bot searches local LuxMed catalog/cache.
   - Bot shows top 5-10 matches as buttons.
4. Bot asks doctor mode.
   - `Any doctor`
   - `Specific doctor`
5. If specific doctor:
   - User types doctor name.
   - Bot shows matches as buttons.
6. Bot asks facilities.
   - `All in selected city`
   - `Choose facilities`
7. If choose facilities:
   - Show paginated checklist-style inline buttons.
   - Each click toggles selection.
   - Buttons: `Next page`, `Done`, `All`, `Clear`.
8. Bot asks date range.
   - `Next 7 days`, `Next 14 days`, `Next 30 days`, `Custom`.
9. Bot asks time preference.
   - `Any time`
   - `Morning`, `Afternoon`, `Evening`
   - `Custom hours`
10. Bot asks check interval.
   - Default from config, e.g. 2-5 minutes.
   - Enforce global minimum to avoid hammering LuxMed.
11. Bot shows summary and asks confirmation.
12. On confirm, create active watch and schedule first check.

### Watch list UX

For each watch show:

- name;
- status;
- target service/doctor;
- city/facilities;
- date/time filters;
- last check status;
- last found result count.

Buttons:

- `Pause`
- `Resume`
- `Edit`
- `Check now`
- `Delete`

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
- `Check again`

Do not send duplicate notifications for the same appointment fingerprint unless it disappeared and reappeared after a configurable cooldown.

## 8. Telegram Mini App decision

Do not build Mini App in MVP unless regular Telegram flow becomes too painful.

Reason:

- A Mini App needs a hosted web frontend, HTTPS, frontend build pipeline, and Telegram WebApp validation.
- Regular Telegram inline keyboards are enough for the first working version.
- The hard part is LuxMed search reliability, not UI polish.

Phase 2 Mini App can be useful for:

- facility multi-select with search;
- complex time windows;
- dashboard of watches;
- credential setup with better form UX.

If Mini App is added later, keep it as another adapter on top of the same backend and domain logic.

## 9. LuxMed integration

Codex must inspect existing repo branches/fixtures first. Preserve all known working endpoints and parsing rules.

Implement LuxMed integration behind this interface:

```go
type Client interface {
    Login(ctx context.Context, credentials Credentials) (Session, error)
    RefreshSession(ctx context.Context, session Session) (Session, error)
    SearchCities(ctx context.Context, query string) ([]City, error)
    SearchServices(ctx context.Context, query string, cityID string) ([]Service, error)
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
- Treat LuxMed schema changes as explicit errors with saved raw fixture for debugging.

If LuxMed API changed, first update `docs/LUXMED_API_NOTES.md` with observed endpoints and payload shapes before coding around it.

## 10. Scheduler

Implement an internal scheduler, not cron-only.

Requirements:

- Poll active watches based on `next_check_at` / `last_checked_at`.
- Enforce global minimum interval, e.g. `MIN_CHECK_INTERVAL_SECONDS=120`.
- Enforce max concurrent LuxMed requests.
- Avoid concurrent checks for the same LuxMed account.
- Save every run result: success, no matches, auth failed, transient error.
- Allow `Check now` from Telegram, but still rate-limit it.
- Continue working after restart.

Simple algorithm:

1. Every `SCHEDULER_TICK_SECONDS`, load due active watches.
2. Group by LuxMed account/user.
3. Run checks with bounded concurrency.
4. For each result, normalize appointments.
5. Filter appointments by date/time/facility/doctor.
6. Deduplicate against `notification_history`.
7. Send notifications.
8. Update watch state and next check time.

## 11. Credentials and security

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
- In production, store normalized appointment details and minimal metadata.
- Add `DATA_RETENTION_DAYS` for notification history cleanup.

## 12. Configuration

Required env vars:

```env
TELEGRAM_BOT_TOKEN=
APP_MASTER_KEY=
ADMIN_TELEGRAM_IDS=
DB_PATH=./luxmed-watcher.db
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
DATA_RETENTION_DAYS=30
DEV_LUXMED_LOGIN=
DEV_LUXMED_PASSWORD=
```

Dev credentials are only for local manual smoke tests, never for CI.

## 13. Tests

Do not overbuild tests, but add the tests that prevent the bot from silently lying.

Minimum useful test suite:

### Unit tests

- invite code validation;
- watch filter matching;
- time window matching;
- appointment fingerprint generation;
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
- storage migrations and repositories.

### End-to-end tests with mocks

Script a full fake flow:

1. user starts bot;
2. enters invite code;
3. configures fake LuxMed credentials;
4. creates watch;
5. fake LuxMed server returns appointment;
6. fake Telegram client receives notification;
7. user clicks `Stop this watch`;
8. watch becomes paused/completed.

### Live smoke test

Optional, not in default CI:

- enabled only when `RUN_LIVE_LUXMED_TESTS=true` and credentials are present;
- performs login and one harmless search;
- does not book appointments.

## 14. CI / quality gates

Add GitHub Actions or equivalent:

- `go fmt` / `gofmt` check;
- `go vet`;
- unit tests;
- integration tests with fake server;
- race test for scheduler package if feasible;
- build Docker image.

Do not require live LuxMed credentials in CI.

## 15. Milestones

### Milestone 0 — Repository discovery and cleanup

Deliverables:

- inspect all branches and existing code;
- document current LuxMed API findings in `docs/LUXMED_API_NOTES.md`;
- add clean project structure;
- add config loading;
- add Docker Compose skeleton;
- add README quickstart.

Acceptance:

- app starts locally;
- `/healthz` works if HTTP server exists;
- config validation fails clearly when required env vars are missing.

### Milestone 1 — Telegram shell and access control

Deliverables:

- Telegram bot starts;
- `/start`, `/menu`, `/help`;
- admin bootstrap from env;
- invite code creation and redemption;
- user state stored in DB.

Acceptance:

- unknown user cannot use bot without invite;
- admin can generate invite;
- invited user sees main menu.

### Milestone 2 — Secure LuxMed account setup

Deliverables:

- user can enter LuxMed login/password;
- credentials encrypted at rest;
- login is tested against LuxMed or fake server;
- auth failure shown clearly;
- remove credentials flow.

Acceptance:

- user can connect LuxMed account;
- app never logs password/token;
- wrong password produces a readable Telegram error.

### Milestone 3 — Catalog search

Deliverables:

- sync or fetch cities/services/doctors/facilities;
- local cache;
- text search with top matches;
- facility pagination/select-all flow.

Acceptance:

- user can type partial service name and choose a mapped LuxMed service ID;
- user can choose all facilities or selected facilities.

### Milestone 4 — Create/list/edit watches

Deliverables:

- complete `New watch` flow;
- list watches;
- pause/resume/delete;
- check interval setting;
- summary confirmation before saving.

Acceptance:

- user can create a watch for service + city + optional doctor + facility/time filters;
- watch persists after restart.

### Milestone 5 — Scheduler and notifications

Deliverables:

- due watch runner;
- LuxMed appointment search;
- filtering;
- dedupe;
- Telegram notifications with buttons;
- `Stop this watch` from notification.

Acceptance:

- fake LuxMed fixture returns appointment;
- bot sends exactly one notification;
- repeated scheduler run does not spam duplicates;
- stop button disables watch.

### Milestone 6 — Deployment and operations

Deliverables:

- Dockerfile;
- docker-compose.yml;
- `.env.example`;
- backup/restore instructions for SQLite DB;
- metrics/logging;
- operations doc.

Acceptance:

- clean deployment on VPS/Raspberry Pi using Docker Compose;
- restart does not lose watches;
- logs are enough to debug auth/search errors.

### Milestone 7 — Optional Telegram Mini App

Deliverables:

- small web frontend only if needed;
- Telegram WebApp validation;
- watch create/edit UI;
- same backend API and domain logic.

Acceptance:

- Mini App can create/edit watches;
- bot still works without Mini App.

## 16. Deep links / opening LuxMed

MVP should include `Open LuxMed` button, but do not rely on guaranteed iOS app opening.

Implementation options:

1. If LuxMed exposes a web booking URL, use it.
2. If LuxMed uses universal links, the same HTTPS link may open the native app on iOS.
3. If no stable deep link exists, open the LuxMed website and include all appointment details in the Telegram message.
4. Later investigate iOS Shortcuts only as a convenience, not as a core booking path.

Acceptance:

- notification always contains enough data to manually find the appointment in LuxMed app.
- link is best-effort only.

## 17. Admin functions

Admin menu:

- create invite code;
- list active users;
- block/unblock user;
- see scheduler status;
- run catalog sync;
- see recent errors;
- trigger test notification to self.

Do not expose admin functions to normal users.

## 18. Error handling

User-facing errors must be actionable:

- LuxMed auth failed: “Login failed. Check login/password.”
- LuxMed session expired: silently retry once, then ask user to reconnect.
- LuxMed unavailable: “LuxMed did not respond. I will retry later.”
- No services found: ask user to change search text.
- No appointments found: do not notify every time; only update watch status.
- Rate limited: back off and show status in watch details.

Internal errors must include enough technical context without secrets.

## 19. Codex implementation rules

1. Make small commits by milestone.
2. Keep code simple and idiomatic.
3. Add tests around behavior, not framework internals.
4. Use interfaces only where there is a real external boundary: Telegram, LuxMed, storage, clock.
5. Use fixtures for LuxMed payloads.
6. Do not hide failures. Surface broken LuxMed parsing in logs/tests.
7. Keep Telegram callback data versioned and compact.
8. Do not store raw medical data unless explicitly needed.
9. Prefer working MVP over perfect architecture.
10. Update README after each milestone.

## 20. Open product questions

These can be clarified later, but do not block MVP:

1. Should one Telegram user be able to monitor multiple LuxMed accounts, e.g. spouse/child?
2. Should notifications go only to the creator or also to shared Telegram chats?
3. Should one invite code map to exactly one user or allow family use?
4. Should user be allowed to set very aggressive polling, or should admin enforce a fixed global interval?
5. Should watch stop automatically after first matching appointment, or continue until user stops it?
6. Should exact doctor search support multiple doctors in one watch?
7. Should facilities be grouped by district/address for Warsaw?
8. Should bot support Polish/Russian/English UI from day one?
9. Should the product support LuxMed child accounts/dependents?
10. Should old notifications/results be deleted after N days for privacy?

Default MVP answers if no clarification is provided:

1. One Telegram user = one LuxMed account.
2. Notifications only in private chat.
3. Invite code can be one-time or limited-use; admin chooses.
4. Admin enforces global minimum polling interval.
5. Watch continues after notification until user pauses/stops it.
6. One exact doctor per watch; multiple doctors require multiple watches.
7. Facilities are searchable and paginated; grouping later.
8. English internal code, Russian or English bot text configurable later; initial bot text can be English or Russian.
9. Dependents are not MVP unless current LuxMed API fixtures already support them.
10. Delete old raw/debug data; keep minimal notification history for dedupe.
