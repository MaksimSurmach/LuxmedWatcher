# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

The owner plus a handful of invited family and friends who live in Poland and are LUX MED patients. They use the product on phones, inside Telegram: the bot sends a message when a slot opens, and the Mini App opens from that chat. Languages: Polish, English, Russian, Ukrainian, Belarusian.

## Product Purpose

LUX MED appointment slots for specialists and tests are scarce and disappear within minutes. Luxmed Watcher keeps checking the LUX MED portal on the user's behalf for a saved request (service, city, dates, hours, optional doctor/clinic) and tells them in Telegram the moment a matching time appears. Success means the user gets a time they actually want and books it before someone else does.

The most frequent job in the Mini App is **seeing what was found**: open from a notification, scan open times, pick one, go book. Second: confirming that searches are alive (watching, last successful check, sign-in needed). Third: requesting a new search.

## Positioning

A private watcher bound to the user's own LUX MED account, living in Telegram. It does not replace the portal; it watches it continuously and hands the user straight to booking (or books, where the server offers it).

## Operating Context

- Opened from a Telegram chat message, usually in a hurry, one-handed, on a phone; Telegram light or dark theme.
- Times are in Europe/Warsaw.
- LUX MED sign-in happens in a remote Chromium (MFA possible) or with an encrypted saved password; sessions expire and need re-sign-in.
- Booking hands off to the LUX MED Patient Portal; referrals live in the portal's Skierowania section.

## Capabilities and Constraints

- Mini App screens: Home, Searches (watches), Results (found slots history), Settings, plus the new/edit search editor and booking dialog.
- Plain HTML/CSS/ES modules served by the Go Mini App server; no build step. Svelte 5 + Vite only if complexity grows.
- All copy comes from `internal/i18n/web/*.json` (five locales); no hardcoded user-facing strings.
- Searches have states: active, paused; check health: last success, LUX MED unavailable, partial results, sign-in required.
- Slots: available or expired/taken; grouped by day, then clinic address.

## Brand Commitments

- Name: "Luxmed Watcher". LUX MED is a third-party brand; the app must not impersonate the LUX MED portal.
- Voice: short, plain, calm; no hype.
- Visual tone (user, 2026-11-28): soft and calm, appropriate for a medical subject. Not aggressive; no black-and-yellow (signage/warning) palettes.

## Evidence on Hand

- Preview server with canned data: `tools/miniapp/preview/server.py` (scenarios: default, empty, session, long, booking; five languages).
- Earlier mockups: `design/mockups/`.
- No testimonials, metrics, or public claims exist; none may be invented.

## Product Principles

1. Found times first: the fastest route from notification to booking wins every layout argument.
2. State is never ambiguous: each search shows whether it is watching and when it last worked.
3. Asking for a search is quick by default; detail is opt-in.
4. Never silent: every tap answers, every failure says what to do next.

## Accessibility & Inclusion

Phones from 360px; Telegram light and dark themes; long Polish and Cyrillic strings; tap targets ≥44px; WCAG AA contrast.
