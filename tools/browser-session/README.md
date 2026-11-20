# LuxMed browser sign-in experiment

This desktop helper checks access to the reservation page in a dedicated Chrome profile. Sign-in and MFA happen on the official LuxMed page. Chrome keeps the session in `~/.luxmed-watcher/chrome-profile`. The helper keeps Chrome’s normal sandbox and platform credential protection. It reports a status for the planner to consume in a future integration.

## Sign in and check the profile

Install Node.js 22 or later and Google Chrome. Open this directory on the machine that will run the browser.

```sh
npm ci
npm run --silent connect
npm run --silent check
```

The `connect` command opens Chrome for manual sign-in. If Chrome offers to remember the password, decline. It waits up to ten minutes for the portal to open the dashboard or reservation page, then checks the reservation form and closes that Chrome instance. The `check` command starts a fresh Chrome instance with the same profile and opens the reservation form once.

| JSON status | Exit code | Meaning |
| --- | --- | --- |
| `ready` | 0 | The protected reservation form opened |
| `needs_login` | 2 | LuxMed asked the user to sign in |
| `unavailable` | 3 | Chrome, the portal or the form could not be verified |

For the live experiment, record only the time and status after connecting, reopening Chrome, and leaving the profile idle. The actual LuxMed session lifetime and persistence across Chrome restarts need a confirmed positive live run.

## Integration boundary

The planner can keep a browser profile on the same trusted host that runs the browser and request ordinary read-only portal checks. A return to the login page should pause those checks and prompt the user to reconnect in that profile. Use the official page as a top-level window. The login response observed on 20 November 2026 returned `X-Frame-Options: SAMEORIGIN`.

This prototype checks readiness only. The discovery of slots and the Telegram connection flow still use the existing Go code. Run `npm test` to exercise sign-in, profile restart and session expiration against a local fixture.

## Single-account Docker MVP

From the repository root, prepare `.env` with the bot token, app master key and exactly one `ADMIN_TELEGRAM_IDS` value. Start both services:

```sh
docker compose -f compose.browser.yml up -d --build
```

The watcher uses SQLite in its own named volume by default. The Chrome profile lives in a separate volume inside the browser worker. Worker requests are limited to the LuxMed reservation dictionaries and appointment availability, behind the Docker network. The browser uses a non-root account with its sandbox enabled. The seccomp profile comes from [Playwright's Docker distribution](https://github.com/microsoft/playwright/blob/v1.65.1/utils/docker/seccomp_profile.json). The container adds the narrow chroot capability required for Chrome's namespace sandbox.

From the workstation that runs Telegram, forward localhost port 18769 to port 18769 on the Docker host over SSH. On that workstation send `/connect` in the owner's private bot chat and open the one-time address. In the server view, sign in to LuxMed, complete MFA and close the viewer. Use `/new` to configure a watch. When the watch runs successfully, the bot's logs include `scheduled slot search completed` with a match count.

The MVP's viewer is available only through the workstation's SSH forward. The bridge expires a launch address after five minutes and closes an interactive viewer after ten minutes. It keeps Chrome running when the viewer disconnects. LuxMed controls the lifetime of the authenticated profile and can require manual steps for particular services.
