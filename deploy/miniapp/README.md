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

## Additional users

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

Send `/app` in the private bot chat. The three screens share the bot's searches, slot history and user preferences. New searches start active. The app can edit or pause them, and notices can open the history screen. Preferences set the default city and language for the app, the bot and notifications. Polish, English, Russian, Belarusian and Ukrainian are available. The procedure catalogue keeps the original Polish names. Add display translations to `internal/procedures/names.json`; when a translation is missing, the app and bot show the Polish source. The app exchanges a Telegram launch proof for a short-lived API session. Connecting a browser also requires a fresh Telegram launch. Reopen the Mini App when it asks for a fresh launch.

Use **Settings → Sign in to LuxMed** or send `/connect` to open the server's Chromium. Opening the dashboard alone does not check or refresh the LuxMed session.

## Manual acceptance

1. From a phone on cellular data send `/connect` to the bot and open the latest **Open LuxMed** button.
2. Complete MFA on the LuxMed page in the server's Chromium. Wait for the dashboard, then press **Close** in the Mini App.
3. Confirm a fresh browser check returns `ready`. Run a read-only search and confirm at least one subsequent scheduled search succeeds after the viewer has closed.
4. Leave an ordinary scheduled watch enabled while measuring session retention. Record the first successful search, subsequent successful searches and eventual expiration, if observed. Review the worker logs for `luxmed_session_refresh_response`. Keep the existing schedule so the experiment measures normal use.

```sh
docker logs --since 3h luxmed-watcher-mvp-browser-1 | grep -E 'luxmed_session_(check|refresh_response)'
docker logs --since 3h luxmed-watcher-mvp-luxmed-watcher-1 2>&1 | grep -E 'scheduled slot search completed|watch check'
```

Session checks and refresh responses are observations. Continued successful searches prove retention. A successful explicit refresh response provides separate evidence of renewal. A run with no observed refresh calls cannot establish how the portal retained its session. LuxMed controls whether and when a new sign-in is required.

## Rollback

Stop the Mini App and recreate the bot using only `compose.browser.yml` for the original loopback viewer. Remove only the ingress, service, and endpoint in `gateway.yaml`. Preserve the SQLite and Chromium volumes.
