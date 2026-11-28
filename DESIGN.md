---
name: Luxmed Watcher
description: A Telegram Mini App that watches LUX MED for appointment times and keeps every found time as a ticket coupon in a wallet.
colors:
  navy-ink: "#1f2f57"
  carbon-purple: "#5a4a8a"
  ticket-red: "#b8392f"
  red-wash: "#f8e9e6"
  warm-paper: "#f3f1ec"
  coupon-stock: "#ffffff"
  body-ink: "#27304a"
  muted-ink: "#5f6884"
  perforation: "#d7dbe5"
  rule-line: "#c9cfdc"
  ink-tint: "#e8ebf3"
  watching-green: "#2f7a59"
  green-wash: "#e3f1ea"
  caution-amber: "#8f5c00"
  amber-wash: "#f6ecd8"
typography:
  display:
    fontFamily: "Figures, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "2.4rem"
    fontWeight: 700
    lineHeight: 1.05
  headline:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1.6rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "-0.015em"
  headline-step:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1.35rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "-0.01em"
  title:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1.1rem"
    fontWeight: 650
    lineHeight: 1.2
  lead:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1.05rem"
    fontWeight: 650
    lineHeight: 1.4
  body:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.5
  label:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "0.9rem"
    fontWeight: 600
    lineHeight: 1.3
  figure-time:
    fontFamily: "Figures, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "1.45rem"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "-0.01em"
    fontFeature: "tnum"
  figure-date:
    fontFamily: "Figures, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "1.75rem"
    fontWeight: 700
    lineHeight: 1
    letterSpacing: "-0.02em"
  stub-label:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "0.7rem"
    fontWeight: 700
    lineHeight: 1.15
    letterSpacing: "0.06em"
  stub-state:
    fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "clamp(0.58rem, 2.7vw, 0.7rem)"
    fontWeight: 700
    lineHeight: 1.15
    letterSpacing: "0em"
rounded:
  notch: "6px"
  field: "10px"
  button: "12px"
  coupon: "14px"
  sheet: "22px"
  pill: "999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "14px"
  lg: "16px"
  xl: "22px"
components:
  button-primary:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.coupon-stock}"
    rounded: "{rounded.button}"
    padding: "10px 18px"
    height: "52px"
  button-secondary:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.button}"
    padding: "10px 18px"
    height: "48px"
  button-flat:
    textColor: "{colors.navy-ink}"
    padding: "10px 4px"
    height: "44px"
  chip:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.pill}"
    padding: "8px 16px"
    height: "44px"
  chip-selected:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.coupon-stock}"
    rounded: "{rounded.pill}"
  switch-tab-current:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.coupon-stock}"
    rounded: "{rounded.pill}"
    padding: "6px 14px"
    height: "40px"
  toolbar-pill:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.pill}"
    padding: "8px 16px"
    height: "44px"
  toolbar-pill-pressed:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.coupon-stock}"
    rounded: "{rounded.pill}"
  account-button:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.pill}"
    size: "44px"
  account-button-current:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.coupon-stock}"
    rounded: "{rounded.pill}"
    size: "44px"
  input:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.body-ink}"
    rounded: "{rounded.field}"
    padding: "8px 12px"
    height: "48px"
  coupon:
    backgroundColor: "{colors.coupon-stock}"
    textColor: "{colors.body-ink}"
    rounded: "{rounded.coupon}"
    height: "88px"
  coupon-stub:
    textColor: "{colors.ticket-red}"
    width: "76px"
    padding: "12px 6px"
  pass-stub:
    textColor: "{colors.navy-ink}"
    width: "92px"
  card:
    backgroundColor: "{colors.coupon-stock}"
    rounded: "{rounded.coupon}"
    padding: "16px"
  intro-card:
    backgroundColor: "{colors.coupon-stock}"
    rounded: "{rounded.coupon}"
    padding: "18px"
  trouble-card:
    backgroundColor: "{colors.red-wash}"
    textColor: "{colors.ticket-red}"
    rounded: "{rounded.coupon}"
    padding: "18px"
  remote-screen:
    backgroundColor: "{colors.coupon-stock}"
    rounded: "{rounded.coupon}"
  booking-sheet:
    backgroundColor: "{colors.coupon-stock}"
    rounded: "{rounded.sheet}"
    padding: "22px"
  badge-active:
    backgroundColor: "{colors.green-wash}"
    textColor: "{colors.watching-green}"
    rounded: "{rounded.pill}"
    padding: "4px 10px"
  badge-attention:
    backgroundColor: "{colors.amber-wash}"
    textColor: "{colors.caution-amber}"
    rounded: "{rounded.pill}"
    padding: "4px 10px"
  badge-problem:
    backgroundColor: "{colors.red-wash}"
    textColor: "{colors.ticket-red}"
    rounded: "{rounded.pill}"
    padding: "4px 10px"
  stamp:
    textColor: "{colors.ticket-red}"
    rounded: "{rounded.notch}"
    padding: "2px 8px"
---

# Design System: Luxmed Watcher

## Overview

**Creative North Star: "The Ticket Wallet"**

Every found appointment is a ticket coupon kept in a wallet. A coupon has a perforated date stub on the left, punched notches where the stub tears off, and a body carrying the time, the doctor, the service and the street. A time that has been taken or has passed keeps its coupon, voided and stamped, so the history never silently disappears. Searches are boarding passes built from the same stub-and-body anatomy, with the stub reporting whether the search is still watching. The same paper, stock and ink carry the LUX MED sign-in page and the standalone remote viewer, so leaving the wallet to sign in never changes worlds: the remote browser sits in a sheet of stock like any other content.

The mood is soft, calm and quiet, as the medical subject asks: warm paper ground, white coupon stock, one navy ink for text and every action. Colour carries meaning, never decoration: carbon purple marks times, ticket red marks dates, counts and stamps, green and amber report search health. Density is phone-first and single column, one coupon per row at full width, with the next action docked at the bottom of the screen. The world refuses the dashboard card stack, the generic white-and-blue booking app, and any black-and-yellow signage palette.

**Key Characteristics:**
- Coupons with a 76px date stub, a 2px dashed perforation and two punched notches cut in the paper colour.
- Navy ink is the only action colour; the current tab, the selected chip and the primary button all fill with it.
- Every figure (date, time, count) is set in Azeret Mono ("Figures"); all words are system sans.
- Soft two-layer shadows lift stock off paper; there are no borders on coupons and cards.
- One status line pattern everywhere: an 8px dot aligned to the first text line, then muted words.
- Three surfaces, one token set: the dashboard, the sign-in page and the remote viewer repeat the same custom properties; light and dark switch by `prefers-color-scheme`.

## Colors

A warm, low-saturation paper-and-navy palette with three meaning colours used sparingly.

### Primary
- **Navy Ink** (navy-ink): headings, names, links and the single action colour. Fills the primary button, the current switch tab, the selected chip, the current account button, the pressed toolbar pill on the sign-in page, the viewer's close button, the toast, the focus outline, the caret and native control accents. Fills are written through an action pair (`--act` / `--act-ink`) that equals navy on stock in light mode; in dark mode the action inverts to a pale ink (#dfe5f5) with paper-dark text (#11141c).

### Secondary
- **Carbon Purple** (carbon-purple): every appointment time figure, on coupons and on the booking sheet, and nothing else. Dark mode #b9acef.

### Tertiary
- **Ticket Red** (ticket-red): the coupon date stub, the tab counts, the "gone" and "past" stamps, errors, alerts, the destructive action, and the problem state. Paired with **Red Wash** (red-wash) as the fill for alerts, notices, problem badges, the "taken" outcome and the sign-in trouble card. Dark mode #f08a7e on #3a2224.

### Neutral
- **Warm Paper** (warm-paper): the page ground, the sticky top bar and dock, and the notch cut-outs. Dark #11141c; also the `theme-color` meta.
- **Coupon Stock** (coupon-stock): coupons, passes, cards, inputs, chips, secondary buttons and the booking sheet. Dark #1a1f2c.
- **Body Ink** (body-ink): running text. Dark #d3d9e8.
- **Muted Ink** (muted-ink): secondary lines, weekday on the stub, metadata, placeholders, the paused state. Dark #98a1b8.
- **Perforation** (perforation): dashed tear lines, dividers inside summaries, health blocks and the sign-in fine print, the sheet grabber, step dots at rest, void-coupon outline, the empty-state ghost stub. Dark #2b3245.
- **Rule Line** (rule-line): 1px borders of inputs, chips, toolbar pills, secondary buttons and the account button. Dark #3a4258.
- **Ink Tint** (ink-tint): neutral badge fill, the confirmed-selection panel, text selection. Dark #252c3e.
- **Watching Green** (watching-green) on **Green Wash** (green-wash): the live status dot and its halo (dashboard and a live sign-in), active badges and pass state words, the connected account, the confirmed booking outcome.
- **Caution Amber** (caution-amber) on **Amber Wash** (amber-wash): the degraded search state only (badge and pass state word).

### Named Rules
**The One Ink Rule.** Navy ink is the only action colour. Anything tappable that commits or selects fills navy; nothing else does.

**The Red Stub Rule.** Ticket red is reserved for dates on stubs, counts, stamps and failures. It never fills a button or a large surface; its wash only backs an error or problem message.

**The Carbon Time Rule.** An appointment time is always carbon purple in Figures. No other text uses carbon purple.

## Typography

**Body Font:** system-ui (with -apple-system, Segoe UI, Roboto, sans-serif)
**Figure Font:** Azeret Mono variable, self-hosted as the family "Figures" in latin and latin-ext subsets (with ui-monospace, SF Mono, Menlo)

**Character:** A plain native sans for every word keeps the app feeling like part of Telegram; a wide, engineered mono for the numbers makes dates and times read like print on a ticket.

### Hierarchy
- **Display** (Figures 700, 2.4rem, 1.05): the appointment time on the booking sheet.
- **Headline** (700, 1.6rem, 1.2, -0.015em): dashboard screen titles; the sheet title 1.2rem.
- **Headline step** (700, 1.35rem, 1.2, -0.01em): wizard step titles and the sign-in page title.
- **Title** (650, 1.1rem, 1.2): pass and card headings; h3 at 0.95rem.
- **Lead** (650, 1.05rem, 1.4): the one navy sentence that opens the sign-in intro card.
- **Body** (400, 1rem, 1.5): running text and inputs. Secondary text, status lines, hints and fine print are 0.86–0.92rem in muted ink; empty-state copy caps at 32ch.
- **Label** (600, 0.9rem): field labels and group labels in navy ink; buttons are 650.
- **Figure time** (Figures 700, 1.45rem, tabular): the time on a coupon; the first coupon grows to 1.85rem.
- **Figure date** (Figures 700, 1.75rem, -0.02em): the day number on a stub; counts use Figures 600 at 0.8rem.
- **Stub label** (700, 0.7rem, 0.06em, uppercase): weekday above and month below the stub's day number, and the unit under a pass horizon. These are printed ticket fields, not section labels.
- **Stub state** (700, clamp(0.58rem, 2.7vw, 0.7rem), no tracking, uppercase): the state word on a pass stub. It shrinks a step instead of breaking; long Polish words never split mid-word (`word-break: keep-all`).
- **Ticket date line** (700, 0.85rem, 1.3, 0.06em, uppercase, system sans): the date on the booking sheet, in ticket red between the perforations.

### Named Rules
**The Figures-Only Rule.** Azeret Mono sets digits only: stub days, pass horizons, times and counts. Words, headings, labels and the booking sheet's date line stay in system sans. The colon inside a time is pulled in (-0.22em each side) so "08:15" closes up.

**The Native Weight Rule.** Weights step 400 / 600 / 650 / 700 / 800; 800 is used only by the stamp.

## Layout

A single centred column, max 560px, with a gutter of max(16px, safe-area-inset-left) and padding that respects both device and Telegram content safe areas. The top bar (two-way switch plus a 44px round account button) is sticky on paper; the primary action lives in a sticky dock at the bottom, also on paper. Coupons stack in a grid with 10px gaps; general stacks use 14px; the wizard uses 16px. Internal padding: coupon body 12px 16px, card 16px, sheet 22px. Two-column field rows and the pass action grid collapse to one column below 360px, where the coupon stub narrows to 64px. Tap targets are at least 44px; primary buttons are 52px, inputs 48px.

The sign-in page is a full-height flex column (12px gaps) sized to the Telegram viewport: title and status at the top, then either the intro card or the remote screen, which takes all remaining height; below it the pill toolbar (keyboard, paste, zoom on the left, the two scroll arrows grouped at the right edge), a muted hint line, and the typing card when the keyboard is open. When the session closes, the status becomes the page's card and Close becomes its 52px navy action. The standalone viewer is a header (status plus close) over one stock screen filling the rest of the viewport with a 16px margin.

## Elevation & Depth

Depth is paper-and-stock: white stock lifted off warm paper by one soft navy-tinted shadow. Coupons, passes, cards, the advanced panel and the review summary share it; nothing else has a resting shadow. The booking sheet casts an upward shadow over a 45% dark backdrop; the toast floats on its own deeper shadow. Dark mode swaps to black-based shadows.

### Shadow Vocabulary
- **Stock lift** (`box-shadow: 0 1px 2px rgb(31 47 87 / .06), 0 6px 18px rgb(31 47 87 / .07)`): every piece of coupon stock at rest.
- **Sheet** (`box-shadow: 0 -10px 40px rgb(17 20 28 / .18)`): the bottom-sheet ticket.
- **Toast** (`box-shadow: 0 10px 30px rgb(17 20 28 / .25)`): the announcement.

### Named Rules
**The Void Goes Flat Rule.** A voided coupon loses its shadow and fill: it becomes a 1px perforation outline at 60% opacity with a struck-through time and a stamp. Spent tickets do not float.

## Shapes

Gently rounded stock (14px) with softer controls (12px buttons, 10px fields) and full pills for chips, tabs and badges; the bottom sheet rounds only its top corners (22px). The ticket geometry is the signature: a 2px dashed perforation divides stub from body, and two 16px circles in the paper colour punch half-notches at the top and bottom of the perforation. The stamp is a 6px-rounded 2px red frame rotated -7deg. Notches are punched only on coupons; passes keep the dashed perforation without them. Dashed perforation lines also divide rows in the review summary, the health block of a pass, the fine print of the sign-in intro and the date band of the booking sheet. An empty slot is drawn as an unprinted coupon: a 2px dashed outline with 12px corners (the 76×64px ghost stub in an empty wallet, a 96×72px blank in the viewer while the remote screen has not arrived).

## Components

### Buttons
Calm, solid and large.
- **Shape:** 12px radius, min height 48px (primary 52px), weight 650.
- **Primary:** navy fill, stock text. Hover brightens 12%.
- **Secondary:** stock fill, 1px rule-line border, navy text; hover turns the border navy.
- **Flat:** no fill, navy text with a rule-line underline offset 4px; used for in-card links (Edit, Pause, Change). `.danger` turns text red.
- **One main action:** a card or state carries one navy action. The account card fills its connect button navy only when sign-in is needed; otherwise it stays secondary.
- **Focus:** 2px navy outline, 2px offset, on every interactive element. Disabled is 50% opacity.

### Chips
- **Style:** pill, stock fill, 1px rule-line border, navy 600 text, 44px tall.
- **State:** `aria-pressed="true"` fills navy with stock text. Quick-service shortcuts reuse the secondary button as a pill.

### Toolbar pills
On the sign-in page every control that is not the main action is a pill: stock fill, 1px rule-line border, navy 650 text, 44px tall, 8px 16px padding; hover turns the border navy. A pressed or expanded pill (zoom, keyboard) fills navy. Icon-only scroll arrows are 44px pills with a 20px stroked arrow, grouped at the trailing edge.

### Cards / Containers
- **Corner Style:** 14px.
- **Background:** coupon stock on paper.
- **Shadow Strategy:** stock lift (see Elevation).
- **Border:** none.
- **Internal Padding:** 16px.

### Inputs / Fields
- **Style:** stock fill, 1px rule-line border, 10px radius, 48px tall, 1rem 400 text; label sits above in navy 600 at 0.9rem.
- **Focus:** border turns navy with the navy outline at 0 offset.
- **Error:** red 600 text below; a selection panel turns red-wash with red text.

### Navigation
A two-way pill switch (Found, Searches) with a red Figures count beside each label; the current tab is a navy pill and its count fades to 75% of the label colour. A 44px round account button on the right shows the Telegram user's initials (700, 0.85rem) or, without a name, a line-icon person; it fills navy when Settings is open. The bar is sticky on paper. Icons are 20px inline SVG strokes at 1.75 width.

### Coupon (signature)
The found-time ticket. A 76px stub (weekday muted, day number in red Figures, month in red) behind a dashed perforation with punched notches; the body holds the carbon time, the navy doctor and service, and the muted street truncated to one line. The soonest coupon's time grows to 1.85rem. Coupons deal in with an 8px rise and fade (0.42s, cubic-bezier(.16, 1, .3, 1), staggered 40ms), and lift 1px on hover. A gone or past coupon is voided and carries a red stamp.

### Boarding pass
A search card on the same anatomy with a 92px navy stub, top-aligned: the stub-state word on top (green active, red problem, amber degraded, muted paused), then the horizon figure (day number or day count, muted when paused) and its muted unit. The body carries the title, muted meta lines, the last successful check and flat-link actions.

### Booking sheet
A bottom-sheet ticket in stock with a 40px perforation-coloured grabber, the service as title, and a date band between two dashed perforations: the date line in red system-sans caps, the time in carbon Figures at display size. Primary "open" and secondary cancel stack below. Opens with a 40px rise (0.32s, same curve).

### Status line
A muted summary (0.88–0.92rem) led by an 8px dot that sits on the first text line (top-aligned, 0.45em down) so wrapped text never centres it. Green with a 4px green-wash halo that breathes to 7px over 2.4s when watching; red with a red-wash halo, still, when attention is needed; plain muted, no halo, when off or connecting. The sign-in page and the viewer use the same line for their connection state; only the dashboard's live dot breathes.

### Badges
Pill, 0.8rem 650, a 7px dot in the current colour; tint/muted by default, green-wash, red-wash or amber-wash by state; paused shows a hollow ring. The account status pill uses the same form and names a state in words ("Connected", "Sign-in needed").

### Sign-in intro and closing cards
The intro is a stock card (18px padding, 14px radius, stock lift) holding the lead sentence, one regular paragraph, fine print in muted 0.86rem below a dashed perforation, and the 52px navy start button. When the session ends the status becomes a card of the same size: stock with navy 600 text when it finished normally, red-wash with red text and no shadow when it failed.

### Remote screen
The remote LUX MED browser is drawn inside a stock sheet (14px radius, stock lift) that takes the remaining height; the canvas is letterboxed inside it and gets an inset 2px navy focus outline. Zoom lets it scroll at native width. Before the first frame arrives the viewer shows the blank-coupon outline in its centre.

## Do's and Don'ts

### Do:
- **Do** keep warm paper (#f3f1ec) as the ground and white stock (#ffffff) for every surface that holds content.
- **Do** fill every committing or selected control with navy ink and nothing else.
- **Do** set every date, time and count in Figures; times in carbon purple, stub dates and counts in ticket red.
- **Do** build new list items on the stub-and-body ticket anatomy with a 2px dashed perforation and paper-coloured notches.
- **Do** lead every connection or health summary with the 8px status dot aligned to the first text line.
- **Do** repeat the full token set, light and dark, on any page served outside the dashboard stylesheet (sign-in, viewer).
- **Do** keep voided coupons visible: outline only, 60% opacity, struck-through time, rotated red stamp.
- **Do** honour `prefers-reduced-motion`: all transitions and the deal, sheet and breathe animations sit inside the no-preference query. The sign-in page and viewer carry no animation.
- **Do** keep tap targets at 44px or more and test at 360px with long Polish and Cyrillic strings in both themes.

### Don't:
- **Don't** use black-and-yellow or any signage/warning palette; the tone stays soft and calm.
- **Don't** use ticket red as a button fill or large surface, or carbon purple for anything but times.
- **Don't** add a second action colour, gradients or glows.
- **Don't** put borders on coupons or cards; depth comes from the stock-lift shadow.
- **Don't** set words or headings in Figures, or put uppercase tracked labels above headings; uppercase belongs only to stub fields, the booking sheet's date line and the stamp.
- **Don't** imitate the LUX MED portal's branding; the sign-in screen is a framed remote view inside this world.
