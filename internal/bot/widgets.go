package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

type telegramUpdate struct {
	tgbotapi.Update
	WidgetQuery *widgetQuery `json:"widget_query"`
}

type widgetQuery struct {
	ID        string         `json:"id"`
	From      *tgbotapi.User `json:"from"`
	Chat      *tgbotapi.Chat `json:"chat"`
	MessageID int            `json:"message_id"`
	WidgetID  string         `json:"widget_id"`
	Data      string         `json:"data"`
	Type      struct {
		Type string `json:"type"`
	} `json:"type"`
}

// The pinned client drops fields added after its version of the Bot API.
func (b *Bot) getUpdates(config tgbotapi.UpdateConfig) ([]telegramUpdate, error) {
	response, err := b.api.Request(config)
	if err != nil {
		return nil, err
	}
	var updates []telegramUpdate
	err = json.Unmarshal(response.Result, &updates)
	return updates, err
}

func (b *Bot) handleWidgetQuery(ctx context.Context, query *widgetQuery) {
	if query.ID == "" || query.MessageID <= 0 || query.WidgetID != "" || query.Type.Type != "data" ||
		!privateChat(query.Chat, query.From) || (b.cfg.Browsers != nil && b.cfg.Browsers[query.From.ID] == nil) ||
		!validWidgetAction(query.Data) {
		return
	}
	defer b.deliverDeleteWarnings(query.From.ID, b.readyDeleteWarning(query.From.ID))
	attempt := b.selectChrome(query.From.ID, query.Data)
	ctx, data := actionContext(ctx, query.Data, query.MessageID)
	params := tgbotapi.Params{"widget_query_id": query.ID}
	check := strings.HasPrefix(data, "watch:check:")
	if check {
		params["data"] = b.handleAction(ctx, query.From.ID, data, attempt)
	}
	if _, err := b.api.MakeRequest("answerWidgetQuery", params); err != nil {
		b.logger.Warn("answer Telegram widget query failed")
	}
	if !check {
		b.handleAction(ctx, query.From.ID, data, attempt)
	}
}

func validWidgetAction(action string) bool {
	if mode, _ := credentialButton(action); mode != "" {
		return true
	}

	switch action {
	case "account", "app", "app:history", "connect:password", "history", "menu", "new", "settings", "settings:language", "watches":
		return true
	}
	if locale, ok := strings.CutPrefix(action, "lang:"); ok {
		return slices.Contains(i18n.SupportedLocales, locale)
	}
	// Paging and hiding carry opaque cursors; handleAction checks them against the user's own data.
	for _, prefix := range []string{"options:", "hide:", "history:"} {
		if strings.HasPrefix(action, prefix) {
			return true
		}
	}
	for _, prefix := range []string{"watch:pause:", "watch:resume:", "watch:show:", "watch:check:", "watch:history:", "watch:edit:", "watch:delete:"} {
		if value, ok := strings.CutPrefix(action, prefix); ok {
			id, err := strconv.ParseInt(value, 10, 64)
			return err == nil && id > 0
		}
	}
	return false
}

// widgetCard is the visual half of a chat card. The caption above it carries the
// header (and is all that clients without widgets show); actions live in the keyboard.
type widgetCard struct {
	Locale      string
	Slots       []widgetSlot
	Passes      []widgetPass
	More        string
	BookingHint string
	// Actions replace the inline keyboard; Mini App buttons open AppLink when it is set.
	Actions [][]miniAppButton
	AppLink string
	Failure string
}

// widgetSlot is one ticket coupon: the stub holds the date, the body the time, doctor and street.
// A slot with a Stamp is void: the time is gone or past.
type widgetSlot struct {
	Label, Weekday, Day, Month, Time, Doctor, Place, URL, Stamp string
	Hide, HideLabel                                             string
	// AppURL opens this slot in the Mini App; the coupon then books through the Book pill.
	AppURL, Book string
}

// widgetPass is one saved search: the stub holds its state, the body what it watches.
type widgetPass struct {
	State, Tone, Figure, Unit string
	Lines                     []string
}

// newWidgetCard starts a card in the user's language with the Mini App link and the tap failure note.
func (b *Bot) newWidgetCard(locale string, actions [][]miniAppButton) widgetCard {
	card := widgetCard{Locale: locale, Actions: actions, Failure: b.catalog.T(locale, "widget.error")}
	if b.cfg.MiniApp != nil {
		card.AppLink = b.cfg.MiniAppLink
	}
	return card
}

func (b *Bot) newWidgetSlot(user domain.User, item domain.AppointmentHistory) widgetSlot {
	locale := user.Locale
	local := item.DateTime.In(luxmed.Location())
	place := item.Address
	if place == "" {
		place = slotPlace(item).facility
	}
	slot := widgetSlot{
		Weekday: strings.Split(b.catalog.T(locale, "slot.weekdays"), ",")[local.Weekday()],
		Day:     strconv.Itoa(local.Day()),
		Month:   strings.Split(b.catalog.T(locale, "slot.months"), ",")[local.Month()-1],
		Time:    local.Format("15:04"),
		Doctor:  truncateMessage(item.DoctorName, 64),
		Place:   truncateMessage(place, 64),
		URL:     portalURL(item.BookingURL),
	}
	if b.cfg.MiniApp != nil && b.cfg.MiniAppLink != "" && b.cfg.Browsers[user.TelegramUserID] != nil && item.ID > 0 {
		slot.AppURL = b.cfg.MiniAppLink + "?startapp=slot-" + strconv.FormatInt(item.ID, 10) + "-" + local.Format(time.DateOnly)
		slot.Book = b.catalog.T(locale, "button.book")
	}
	slot.Label = strings.Join(slices.DeleteFunc([]string{b.slotDateTime(locale, item.DateTime), slot.Doctor, slot.Place}, func(s string) bool { return s == "" }), " · ")
	return slot
}

// Telegram widgets load no fonts, so the Figures face falls back to the platform monospace.
// The palette follows DESIGN.md; prefers-color-scheme tracks the chat theme.
var chatWidget = template.Must(template.New("telegram-widget").Parse(`<!doctype html>
<html lang="{{.Locale}}">
<style>
:root{--paper:#f3f1ec;--stock:#fff;--ink:#1f2f57;--on-ink:#fff;--tint:#e8ebf3;--text:#27304a;--muted:#5f6884;--perf:#d7dbe5;--red:#b8392f;--carbon:#5a4a8a;--ok:#2f7a59;--warn:#8f5c00;--lift:0 1px 2px rgb(31 47 87/.06),0 6px 18px rgb(31 47 87/.07);--fig:ui-monospace,monospace;color-scheme:light dark}
@media(prefers-color-scheme:dark){:root{--paper:#11141c;--stock:#1a1f2c;--ink:#dfe5f5;--on-ink:#11141c;--tint:#252c3e;--text:#d3d9e8;--muted:#98a1b8;--perf:#2b3245;--red:#f08a7e;--carbon:#b9acef;--ok:#7ccfa6;--warn:#e8b86a;--lift:0 1px 2px rgb(0 0 0/.3),0 8px 22px rgb(0 0 0/.25)}}
html{background:var(--paper)}body{margin:0;font:15px/1.35 system-ui,sans-serif;color:var(--text)}
article{padding:10px;overflow-wrap:anywhere}p{margin:0}
ol{display:grid;gap:8px;margin:0;padding:0;list-style:none}
.coupon{position:relative;border-radius:12px;background:var(--stock);box-shadow:var(--lift)}
.open{display:grid;grid-template-columns:62px minmax(0,1fr);min-height:74px;color:inherit;text-decoration:none}
.book{position:absolute;top:10px;right:10px;padding:6px 12px;border-radius:999px;background:var(--ink);color:var(--on-ink);font:600 13px/1.2 system-ui,sans-serif;text-decoration:none}
.stub{position:relative;display:grid;align-content:center;justify-items:center;gap:2px;border-right:2px dashed var(--perf);color:var(--red)}
.stub:before,.stub:after{content:"";position:absolute;right:-8px;width:14px;height:14px;border-radius:50%;background:var(--paper)}
.stub:before{top:-7px}.stub:after{bottom:-7px}
.stub b{font:700 24px/1 var(--fig);letter-spacing:-.02em}
.stub small{font-size:10.5px;font-weight:700;line-height:1.1;letter-spacing:.06em;text-transform:uppercase}
.stub small:first-child{color:var(--muted)}
.slot{display:grid;align-content:center;min-width:0;padding:10px 14px}
time{color:var(--carbon);font:700 21px/1.1 var(--fig);font-variant-numeric:tabular-nums;letter-spacing:-.01em}
.next time{font-size:26px}
.slot strong{overflow:hidden;color:var(--ink);font-weight:600;white-space:nowrap;text-overflow:ellipsis}
.slot small{overflow:hidden;color:var(--muted);font-size:13px;white-space:nowrap;text-overflow:ellipsis}
.void{background:none;box-shadow:none;outline:1px solid var(--perf);outline-offset:-1px;opacity:.75}
.void .stub,.void strong{color:var(--muted)}.void time{text-decoration:line-through 2px}
.stamp{justify-self:start;margin-top:4px;padding:1px 7px;border:2px solid var(--red);border-radius:6px;color:var(--red);font-size:11px;font-weight:800;letter-spacing:.1em;text-transform:uppercase;transform:rotate(-4deg)}
.pass{display:grid;grid-template-columns:76px minmax(0,1fr);min-height:74px}.pass .stub{padding:10px 4px;color:var(--ink)}
.pass .stub small{text-align:center;color:var(--muted);overflow-wrap:normal;word-break:keep-all}
.pass .stub small:first-child{margin-bottom:4px;font-size:9.5px;letter-spacing:0}
.active .stub small:first-child{color:var(--ok)}.degraded .stub small:first-child{color:var(--warn)}.problem .stub small:first-child{color:var(--red)}
.paused .stub b{color:var(--muted)}.pass .slot small{white-space:normal}.pass .slot small:first-child{color:var(--text)}
.more{margin:10px 4px 0;color:var(--ink);font-weight:600}
.booking-hint{margin:4px 4px 0;color:var(--muted);font-size:13px}
nav{margin-top:12px}.row{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}
button,nav a{box-sizing:border-box;flex:1 1 96px;min-height:42px;padding:8px;border:0;border-radius:12px;background:var(--tint);color:var(--ink);font:600 14px/1.2 system-ui,sans-serif;text-align:center}
nav a{display:flex;align-items:center;justify-content:center;text-decoration:none}
nav .primary{background:var(--ink);color:var(--on-ink)}
button:disabled{opacity:.6}
.hideable .slot{padding-right:70px}.coupon .hide{position:absolute;right:6px;bottom:4px;min-height:0;padding:6px 8px;background:none;color:var(--muted);font-size:12px}
#status,#error{margin:8px 4px 0;font-size:13px}#error{color:var(--red)}
</style>
<article>{{if .Slots}}<ol>{{range $i, $s := .Slots}}<li class="coupon{{if $s.Stamp}} void{{else if eq $i 0}} next{{end}}{{if $s.Hide}} hideable{{end}}">{{if $s.Stamp}}<div class="open" aria-label="{{$s.Label}} · {{$s.Stamp}}">{{else if $s.AppURL}}<a class="open" href="{{$s.AppURL}}" data-app aria-label="{{$s.Label}}">{{else}}<a class="open" href="{{$s.URL}}" aria-label="{{$s.Label}}">{{end}}<span class="stub" aria-hidden="true"><small>{{$s.Weekday}}</small><b>{{$s.Day}}</b><small>{{$s.Month}}</small></span><span class="slot" aria-hidden="true"><time>{{$s.Time}}</time>{{if $s.Doctor}}<strong>{{$s.Doctor}}</strong>{{end}}{{if $s.Place}}<small>{{$s.Place}}</small>{{end}}{{if $s.Stamp}}<span class="stamp">{{$s.Stamp}}</span>{{end}}</span>{{if $s.Stamp}}</div>{{else}}</a>{{if $s.AppURL}}<a class="book" href="{{$s.URL}}" aria-label="{{$s.Book}}: {{$s.Label}}">{{$s.Book}}</a>{{end}}{{end}}{{if $s.Hide}}<button class="hide" data-callback="{{$s.Hide}}" aria-label="{{$s.HideLabel}}: {{$s.Label}}">{{$s.HideLabel}}</button>{{end}}</li>{{end}}</ol>{{end}}
{{if .Passes}}<ol>{{range .Passes}}<li class="coupon pass {{.Tone}}"><span class="stub"><small>{{.State}}</small><b>{{.Figure}}</b><small>{{.Unit}}</small></span><span class="slot">{{range .Lines}}<small>{{.}}</small>{{end}}</span></li>{{end}}</ol>{{end}}
{{if .More}}<p class="more">{{.More}}</p>{{end}}{{if .BookingHint}}<p class="booking-hint">{{.BookingHint}}</p>{{end}}
{{if .Actions}}<nav>{{range .Actions}}<div class="row">{{range .}}{{if .URL}}<a class="primary" href="{{.URL}}">{{.Text}}</a>{{else if .WidgetAction}}{{if $.AppLink}}<a href="{{$.AppLink}}" data-app>{{.Text}}</a>{{else}}<button data-callback="{{.WidgetAction}}">{{.Text}}</button>{{end}}{{else if .CallbackData}}<button data-callback="{{.CallbackData}}">{{.Text}}</button>{{end}}{{end}}</div>{{end}}</nav>{{end}}
<p id="status" role="status" hidden></p><p id="error" aria-live="polite" hidden>{{.Failure}}</p></article>
<script>
const w=window.TelegramWidget,status=document.getElementById("status"),error=document.getElementById("error");
for(const a of document.querySelectorAll("[data-app]"))a.onclick=e=>{if(w&&w.openTelegramLink){e.preventDefault();w.openTelegramLink(a.href)}};
for(const b of document.querySelectorAll("[data-callback]"))b.onclick=()=>{status.hidden=error.hidden=true;b.disabled=true;
w.request(b.dataset.callback).then(a=>{b.disabled=false;if(a.result){status.textContent=a.result;status.hidden=false}},()=>{error.hidden=false;b.disabled=false})};
</script></html>`))

func (b *Bot) sendWidget(card widgetCard, caption string, fallback tgbotapi.MessageConfig) (int, error) {
	var markup strings.Builder
	if err := chatWidget.Execute(&markup, card); err != nil {
		return 0, err
	}
	params := tgbotapi.Params{
		"chat_id":       strconv.FormatInt(fallback.ChatID, 10),
		"html":          markup.String(),
		"caption":       caption,
		"allow_queries": "true",
	}
	params.AddNonEmpty("parse_mode", fallback.ParseMode)
	params["show_caption_above_media"] = "true"
	// The client exposes Telegram errors and discards the HTTP status.
	api := *b.api
	client := &telegramStatusHTTP{HTTPClient: api.Client}
	api.Client = client
	res, err := api.MakeRequest("sendWidget", params)
	if err != nil {
		apiErr, rejected := errors.AsType[*tgbotapi.Error](err)
		if !rejected || client.status != apiErr.Code || (apiErr.Code != http.StatusBadRequest && apiErr.Code != http.StatusNotFound) {
			return 0, err
		}
		res, err = b.api.Request(fallback)
		if err != nil {
			return 0, err
		}
	}
	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(res.Result, &sent); err != nil {
		return 0, err
	}
	if sent.MessageID <= 0 {
		return 0, fmt.Errorf("Telegram delivery response has no message ID")
	}
	return sent.MessageID, nil
}

type telegramStatusHTTP struct {
	tgbotapi.HTTPClient
	status int
}

func (c *telegramStatusHTTP) Do(req *http.Request) (*http.Response, error) {
	res, err := c.HTTPClient.Do(req)
	if err == nil {
		c.status = res.StatusCode
	}
	return res, err
}
