---
version: 1
slug: "tools-miniapp-web-dashboard-html"
primary_target: "tools/miniapp/web/dashboard.html"
related_targets: ["tools/miniapp/web/index.html"]
---

# Mini App (Telegram) — dashboard and sign-in

Scope: tools/miniapp/web (dashboard.html/css/js, index.html/style.css). Mode: Operate.
Audience: owner + invited family/friends in Poland, phone, inside Telegram. Job order: see what was found → check searches are alive → request a new search.
Constraints: plain HTML/CSS/ES modules; copy in internal/i18n/web/*.json (pl, en, ru, uk, be); Warsaw time; light + dark (system); 360px+. User brand commitment: soft, calm, premium & minimal; no black-and-yellow.
History: Odjazdy Timetable (yellow/black) built then rejected by the user as too aggressive; replaced by this direction (third hand of the roll).

## Direction contract

THESIS: Every found appointment is a ticket coupon in your wallet: a perforated stub with the date, the body with time, doctor and street; a time that is gone keeps its coupon with a stamp. Refuses both the dashboard card stack and the generic white-and-blue booking app.

OWN-WORLD: Warm paper ground (#f3f1ec / dark #11141c), white coupon stock, navy ink (#1f2f57) for text and the one action colour, carbon purple (#5a4a8a) for every time figure, ticket red (#b8392f) only on date stubs, counts and stamps. Azeret Mono for figures (dates, times, counts); system sans for everything else. Dashed perforations and punched notches; soft shadows, 12–14px radii, pill chips.

STORY: The visitor opens from Telegram on Found: one status line says the searches are alive, the coupons show the soonest time first; tapping a coupon opens a bottom-sheet ticket with "Open LUX MED". Searches lists each search with an unambiguous state pill. New search is a three-step flow (what → when with preset chips → review with Change links).

FIRST VIEWPORT: Top: two-way switch (Found 5 · Searches 2) and the account button. Status line with a live dot. Coupons stacked full width. Sticky "New search" navy button at the bottom.

FORM: Ticket Wallet (catalog challenger vernacular-ephemera-jet-age-ticket-wallet, competitive verdict, user-chosen over assigned Linen & Charcoal and pick Reception Stone); seed:dbb4358e8e8f91c1. Signature move: found times as perforated coupons with a date stub and a stamp for gone times.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Open decisions
- Primary action kept navy rather than the sketch's red, per the user's "soft, not aggressive" brief.
