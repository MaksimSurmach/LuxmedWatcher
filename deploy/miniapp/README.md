# Telegram Mini App over HTTPS

The bot sends an inline `web_app` button in an active user's private Telegram chat. The Go API verifies fresh Telegram launch data and issues a one-hour session kept in the Mini App's memory. Every request also checks the user's current status and browser assignment. Sign-in links and 45-second WebSocket tickets are single use and bound to the user's browser. The browser worker opens its own one-time VNC connection. The server's Chromium profile and the SQLite database remain in their Docker volumes on Ubuntu. The public route uses the existing Cloudflare HTTPS edge and Traefik over the existing server-to-server WireGuard link. The phone does not need a VPN.

## Ubuntu

Keep the existing `.env` and browser volumes. The separate `.miniapp.env` file contains only the following deployment settings.

| Variable | Value |
| --- | --- |
| `LUXMED_MINIAPP_URL` | `https://luxmed.ranus.site` |
| `LUXMED_MINIAPP_BIND` | `10.250.183.2`, the home WireGuard address |
| `LUXMED_BOT_ID` | The numeric portion of the Telegram bot token before the colon |

```sh
docker compose --env-file .env --env-file .miniapp.env \
  -p luxmed-watcher-mvp -f compose.browser.yml -f compose.miniapp.yml up -d --build
```

The public app listens on port 19090 on the WireGuard address. Traefik reaches it over the private existing link. The bot's ticket API is accessible only inside Compose. With the default Compose files the browser is assigned to the single ID in `ADMIN_TELEGRAM_IDS`. The public origin must match the HTTPS address sent by Telegram. `LUXMED_DASHBOARD_API_URL` points to the private Go API, which must be online to authorize new browser streams.

## Shared browser backends

Use `compose.browser.shared.yml` in place of `compose.browser.yml` with the Mini App overlay. Keep `LUXMED_INTERNAL_KEY` and set a distinct `LUXMED_WORKER_KEY`, both generated locally with `openssl rand -hex 32`. If you omit `LUXMED_BROWSER_ACCOUNT_IDS`, the configured Telegram admins receive a browser assignment. Set a comma-separated list to include invited accounts. Existing invite checks still apply. Remove `LUXMED_BROWSER_ROUTES` when switching to a shared worker.

- Set `LUXMED_BROWSER_BACKEND=chromium` for one local Chromium process. The worker downloads the pinned Playwright headless shell to its cache on first use if it is absent. Each account gets an isolated context and a private state file with cookies, local storage and IndexedDB.
- Set `LUXMED_BROWSER_BACKEND=steel` and `STEEL_BASE_URL` to the private origin of a self-hosted Steel server. The worker connects over CDP and uses the same per-account state storage. If the server returns a debugger address unreachable from the Docker network, configure `STEEL_CDP_URL` with the trusted CDP URL. Keep Steel's API and debugger on a trusted network. Self-hosted Steel serves one session at a time, so concurrent requests wait for a subsequent check. Use an instance reserved for this worker if other clients can create Steel sessions.

```sh
docker compose --env-file .env --env-file .miniapp.env \
  -p luxmed-watcher-mvp -f compose.browser.shared.yml -f compose.miniapp.yml up -d --build
```

The shared worker uses direct HTTP for searches, account checks and token renewal. Chromium or Steel opens only for interactive sign-in and MFA. Closing the viewer saves the profile and releases its browser context. The worker serializes HTTP requests and browser access to each account so they share one cookie store.

Shared mode uses the existing watcher database and a new `browser-profiles` volume. Sign in to LuxMed again for each shared profile. To return to the existing noVNC setup, deploy with `compose.browser.yml` and the Mini App overlay. Keep both profile volumes if you plan to switch back.

## Quick procedure selection

Open **Recommendations** from the Mini App home to see services from your Lux Med referrals, recent searches, saved searches and popular services. Choose a city to load the suggestions. **Referrals** opens the official portal's **Skierowania** section for referral-based booking.

## Additional users with dedicated noVNC workers

Assign one dedicated browser worker, private Docker network and profile volume to each Telegram user ID. The users share the Go service and city and procedure dictionaries, with separate search settings, slot history and LuxMed sessions. The provider evaluates appointment availability through each user's worker. Keep only administrators in `ADMIN_TELEGRAM_IDS`. An admin assigned to a browser can issue `/invite` codes. After the assignment is deployed, the invited user opens the private chat and sends `/start CODE`. A worker assignment alone does not activate an account.

To add a second user, set `LUXMED_INTERNAL_KEY` in `.miniapp.env` to a 32-byte hex secret generated locally with `openssl rand -hex 32`. The watcher and gateway use this shared secret for ticket issuance. The browser workers do not receive it. Put the following route example in the same file. Replace the example IDs with the existing owner's Telegram ID and the new user's Telegram ID. Keep the first worker named `browser` to preserve the owner's current profile. Explicit routes replace the default single-owner mapping.

```dotenv
LUXMED_BROWSER_ROUTES='{"10001":{"api":"http://browser:8091","viewer":"http://browser:8092/vnc"},"10002":{"api":"http://browser-two:8091","viewer":"http://browser-two:8092/vnc"}}'
```

Apply the additional Compose file after the two base files.

```sh
docker compose --env-file .env --env-file .miniapp.env \
  -p luxmed-watcher-mvp -f compose.browser.yml -f compose.miniapp.yml \
  -f compose.browser.second.yml up -d --build
```

Only the Go watcher and the Mini App gateway join both networks. The secondary worker publishes no host ports. For more users add another unique service, private network and profile volume, then add the corresponding route. Keep the service and volume unique even if both people use the same clinic. Do not route more than one user to the same worker. The Go scheduler runs up to three checks in parallel across accounts and serializes checks within an account.

## Public ingress

On `contabo-main`, check a healthy response from the live Traefik pod before applying [gateway.yaml](gateway.yaml). The manifest contains its own selectorless service and WireGuard endpoint. It uses the existing TLS configuration and leaves the other routes in place.

```sh
sudo kubectl -n traefik exec deploy/traefik -c traefik -- \
  wget -S -q -O /dev/null -T 5 http://10.250.183.2:19090/healthz
sudo kubectl apply -f deploy/miniapp/gateway.yaml
curl -fsS -o /dev/null -w '%{http_code}\n' https://luxmed.ranus.site/healthz
```

## App and settings

Send `/start` in the private bot chat for the welcome card and its buttons, or `/app` to open the Mini App directly. The app shares the bot's searches, slot history and user preferences. Telegram clients without widget support can use the inline keyboard under the card. New searches start active. The app can edit or pause them, and notices can open the history screen. Preferences set the default city and language for the app, the bot and notifications. Polish, English, Russian, Belarusian and Ukrainian are available. The procedure catalogue keeps the original Polish names. Add display translations to `internal/procedures/names.json`; when a translation is missing, the app and bot show the Polish source. The app exchanges a Telegram launch proof for a short-lived API session. Connecting a browser also requires a fresh Telegram launch. Reopen the Mini App when it asks for a fresh launch.

Use **Settings → Sign in to LuxMed** or send `/connect` to open the server's Chromium. Opening the dashboard alone does not check or refresh the LuxMed session.

## Private API and Claude Code

Add a separate 32-byte hex `LUXMED_CONTROL_KEY` to the watcher's environment or to `.miniapp.env`. Generate it with `openssl rand -hex 32` and keep that file private. By default the key is assigned to the sole Telegram admin. Set `LUXMED_CONTROL_TELEGRAM_ID` explicitly if there are multiple admins or if Claude should manage a specific invited account. The assigned user must be active and have a browser. Recreate the watcher after changing the key or assignment.

The HTTP listener binds to `127.0.0.1:8094` inside the watcher container. The public Mini App host does not route to it. All requests require `Authorization: Bearer <LUXMED_CONTROL_KEY>`.

| Method and path | Operation | JSON body |
| --- | --- | --- |
| `GET /v1/workspace` | List this user's searches, preferences and interval limits | |
| `GET /v1/cities` | List cities | |
| `POST /v1/procedures` | Search a city's procedures in the user's locale | `{"CityID":1,"Query":"dermatolog","Offset":0}` |
| `POST /v1/providers` | List doctors and facilities for a procedure | `{"CityID":1,"ServiceID":2}` |
| `POST /v1/preferences` | Set locale and default city. Use city ID 0 to clear it. | `{"Locale":"pl","CityID":1}` |
| `POST /v1/searches` | Create a search or replace its filters by ID | `{"CityID":1,"ServiceID":2,"NextDays":14,"CheckMinutes":5}` |
| `POST /v1/searches/status` | Pause or resume a search by ID | `{"ID":3,"Status":"paused"}` |

When saving a search, supply `NextDays` from 1 to 90 or both `DateFrom` and `DateTo` as YYYY-MM-DD. Optional filters are `DoctorID`, `FacilityIDs`, and `TimeWindows`, for example `[{"weekday":1,"from":"09:00","to":"14:00"}]`. Weekdays run from 1 (Monday) to 7 (Sunday). Use 0 for a single window that applies every day. Resend all filters when editing. An edit preserves the active or paused status. Successful writes return HTTP 204. HTTP 409 means the owner needs to sign in to Lux Med again.

Use Claude Code over stdio. From the machine running Claude Code, set up a trusted SSH alias `luxmed-home` for the Ubuntu host, then register the MCP server from the Claude Code project directory.

```sh
claude mcp add --transport stdio luxmed-watcher -- \
  ssh -T -o BatchMode=yes luxmed-home \
  sudo -n docker exec -i luxmed-watcher-mvp-luxmed-watcher-1 luxmed-mcp
```

Use an existing administrator SSH identity. The MCP process inherits the key inside the container. Claude Code receives only the seven configuration tools and their results. Start with `get_workspace`. The standalone `luxmed-mcp` command can also run directly alongside the core with the same key and its loopback API address.

## Manual acceptance

1. From a phone on cellular data send `/connect` to the bot and open the latest **Open LuxMed** button.
2. Complete MFA on the LuxMed page in the server's Chromium. Wait for the dashboard, then press **Close** in the Mini App.
3. Confirm **Check connection** reports connected. Check that a subsequent scheduled search reaches the appointment API after the viewer has closed.
4. Leave an ordinary scheduled watch enabled. In shared mode, compare successive `luxmed_http_refresh` entries with status 200 and a later `expires_at`, and verify subsequent `luxmed_http_search` responses. Keep the existing search schedule so the experiment measures normal use.

```sh
docker logs --since 3h luxmed-watcher-mvp-browser-1 | grep -E 'luxmed_http_(refresh|search)|luxmed_session_check'
docker logs --since 3h luxmed-watcher-mvp-luxmed-watcher-1 2>&1 | grep -E 'scheduled slot search completed|watch check'
```

The shared worker logs a successful refresh after the expiry advances and the new cookies have been saved. `luxmed_http_search` records HTTP 200 from the appointment API. The watcher can still report incomplete coverage for a day. LuxMed controls when a new sign-in is required.

## Rollback

Stop the Mini App and recreate the bot using only `compose.browser.yml` for the original loopback viewer. Remove only the ingress, service, and endpoint in `gateway.yaml`. Preserve the SQLite and Chromium volumes.
