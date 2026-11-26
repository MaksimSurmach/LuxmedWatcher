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

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
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
	if _, err := b.api.MakeRequest("answerWidgetQuery", tgbotapi.Params{"widget_query_id": query.ID}); err != nil {
		b.logger.Warn("answer Telegram widget query failed")
		return
	}
	b.handleAction(ctx, query.From.ID, query.Data)
}

func validWidgetAction(action string) bool {
	switch action {
	case "account", "app", "app:history", "connect:chrome", "connect:open", "connect:password", "history", "menu", "new", "settings", "settings:language", "watches":
		return true
	}
	if locale, ok := strings.CutPrefix(action, "lang:"); ok {
		return slices.Contains(i18n.SupportedLocales, locale)
	}
	for _, prefix := range []string{"watch:pause:", "watch:show:", "watch:check:", "watch:history:"} {
		if value, ok := strings.CutPrefix(action, prefix); ok {
			id, err := strconv.ParseInt(value, 10, 64)
			return err == nil && id > 0
		}
	}
	return false
}

type widgetCard struct {
	Locale      string
	Title       string
	Body        string
	Slot        *widgetSlot
	More        string
	BookingHint string
	Actions     [][]miniAppButton
	Failure     string
}

type widgetSlot struct {
	When     string
	Doctor   string
	Facility string
	Address  string
}

func newWidgetSlot(locale string, item domain.AppointmentHistory) *widgetSlot {
	return &widgetSlot{
		When:     appointmentDateTime(locale, item.DateTime),
		Doctor:   truncateMessage(item.DoctorName, 64),
		Facility: truncateMessage(slotPlace(item).facility, 90),
		Address:  truncateMessage(item.Address, 64),
	}
}

var chatWidget = template.Must(template.New("telegram-widget").Parse(`<!doctype html>
<html lang="{{.Locale}}">
<style>
body{margin:8px 10px;color:var(--tg-theme-text-color,#162826)}
article{padding:14px;border:1px solid var(--tg-theme-section-separator-color,#dfe7e3);border-radius:12px;overflow-wrap:anywhere}
h2,p{margin:0}h2{font-size:1.05em;line-height:1.24}p{margin-top:5px}
.body{white-space:pre-line}.slot{margin:8px 0;padding:10px 12px;background:var(--tg-theme-secondary-bg-color,#f2f6f4);border-radius:8px}
.slot strong{font-size:1.24em;line-height:1.25}.slot p{margin-top:4px}
.more,.booking-hint{font-size:.9em;color:var(--tg-theme-subtitle-text-color,#4e625e)}
.row{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}nav{margin-top:10px}
button,a{box-sizing:border-box;flex:1 1 112px;min-height:44px;text-align:center;border:0;border-radius:8px;padding:8px;background:var(--tg-theme-secondary-bg-color,#f2f6f4);color:var(--tg-theme-text-color,#162826);font:inherit}
a{display:flex;align-items:center;justify-content:center;text-decoration:none}
.row:first-child button,.row:first-child a{background:var(--tg-theme-button-color,#00695c);color:var(--tg-theme-button-text-color,#fff)}
</style>
<article><h2>{{.Title}}</h2>
{{with .Slot}}<section class="slot"><strong>{{.When}}</strong>{{if .Doctor}}<p>{{.Doctor}}</p>{{end}}{{if .Facility}}<p>{{.Facility}}</p>{{end}}{{if .Address}}<p>{{.Address}}</p>{{end}}</section>{{end}}
{{if .Body}}<p class="body">{{.Body}}</p>{{end}}{{if .More}}<p class="more">{{.More}}</p>{{end}}
{{if .BookingHint}}<p class="booking-hint">{{.BookingHint}}</p>{{end}}
<nav>{{range .Actions}}<div class="row">{{range .}}
{{if .URL}}<a href="{{.URL}}">{{.Text}}</a>{{else if .WidgetAction}}<button data-callback="{{.WidgetAction}}">{{.Text}}</button>{{else if .CallbackData}}<button data-callback="{{.CallbackData}}">{{.Text}}</button>{{end}}
{{end}}</div>{{end}}</nav>
<p id="error" aria-live="polite" hidden>{{.Failure}}</p></article>
<script>
const feedback = document.getElementById('error');
for (const button of document.querySelectorAll('[data-callback]')) {
  button.addEventListener('click', () => {
    feedback.hidden = true;
    button.disabled = true;
    TelegramWidget.request(button.getAttribute('data-callback')).then(
      () => { button.disabled = false; },
      () => { feedback.hidden = false; button.disabled = false; }
    );
  });
}
</script></html>`))

func (b *Bot) sendWidget(card widgetCard, caption string, fallback tgbotapi.MessageConfig) (int, error) {
	var markup strings.Builder
	if err := chatWidget.Execute(&markup, card); err != nil {
		return 0, err
	}
	keyboard, err := json.Marshal(fallback.ReplyMarkup)
	if err != nil {
		return 0, err
	}
	params := tgbotapi.Params{
		"chat_id":       strconv.FormatInt(fallback.ChatID, 10),
		"html":          markup.String(),
		"caption":       caption,
		"reply_markup":  string(keyboard),
		"allow_queries": "true",
	}
	params.AddNonEmpty("parse_mode", fallback.ParseMode)
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
