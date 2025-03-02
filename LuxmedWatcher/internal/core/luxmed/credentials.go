package luxmed

import (
	"time"
)

// Credentials — auth credentials
type Credentials struct {
	Username string
	Password string
}

// AuthTokens — auth tokens
type AuthTokens struct {
	AccessToken    string            // тот самый token, приходящий в body["token"]
	RefreshToken   string            // приходит в Set-Cookie: RefreshToken=...
	LXToken        string            // приходит в Set-Cookie: LXToken=...
	XsrfToken      string            // приходит в Set-Cookie: XSRF-TOKEN=...
	Cookies        map[string]string // любые другие куки, нужные для запросов
	ExpirationTime time.Time         // когда токен истечёт (если известно)
}

// loginResponse login response
type loginResponse struct {
	Succeeded     bool   `json:"succeded"`
	ErrorMessage  string `json:"errorMessage"`
	ReturnURL     string `json:"returnUrl"`
	Token         string `json:"token"`
}

// forgeryResponse forgery response
type forgeryResponse struct {
	Token string `json:"token"`
}