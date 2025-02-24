package luxmed

import (
	"time"
)

// Credentials — данные для авторизации в Luxmed.
type Credentials struct {
	Username string
	Password string
}

// AuthTokens — структура для хранения токенов, куки и их сроков.
type AuthTokens struct {
	AccessToken    string            // тот самый token, приходящий в body["token"]
	RefreshToken   string            // приходит в Set-Cookie: RefreshToken=...
	LXToken        string            // приходит в Set-Cookie: LXToken=...
	XsrfToken      string            // приходит в Set-Cookie: XSRF-TOKEN=...
	Cookies        map[string]string // любые другие куки, нужные для запросов
	ExpirationTime time.Time         // когда токен истечёт (если известно)
}

// loginResponse описывает JSON-ответ на POST /LogIn
type loginResponse struct {
	Succeeded     bool   `json:"succeded"`
	ErrorMessage  string `json:"errorMessage"`
	ReturnURL     string `json:"returnUrl"`
	Token         string `json:"token"`
}

// forgeryResponse описывает JSON-ответ на POST /security/getforgerytoken
type forgeryResponse struct {
	Token string `json:"token"`
}