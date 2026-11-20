# Telegram Mini App over HTTPS

The bot sends an inline `web_app` button in the owner's private Telegram chat. The home Mini App verifies Telegram's signature for this bot and owner, consumes the link ticket, and grants one 45-second WebSocket ticket. The browser worker opens its own one-time VNC connection. The server's Chromium profile and the SQLite database remain in their Docker volumes on Ubuntu. The public route uses the existing Cloudflare HTTPS edge and Traefik over the existing server-to-server WireGuard link. The phone does not need a VPN.

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

The public app listens on port 19090 on the WireGuard address. Traefik reaches it over the private existing link. The bot's ticket API is accessible only inside Compose. The owner is the single ID already in `ADMIN_TELEGRAM_IDS`. The public origin must match the HTTPS address sent by Telegram.

## Public ingress

On `contabo-main`, check a healthy response from the live Traefik pod before applying [gateway.yaml](gateway.yaml). The manifest contains its own selectorless service and WireGuard endpoint. It uses the existing TLS configuration and leaves the other routes in place.

```sh
sudo kubectl -n traefik exec deploy/traefik -c traefik -- \
  wget -S -q -O /dev/null -T 5 http://10.250.183.2:19090/healthz
sudo kubectl apply -f deploy/miniapp/gateway.yaml
curl -fsS -o /dev/null -w '%{http_code}\n' https://luxmed.ranus.site/healthz
```

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
