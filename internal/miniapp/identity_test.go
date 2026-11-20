package miniapp

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestTelegramIdentity(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticatedAt := time.Unix(1_800_000_000, 0)
	authTime := strconv.FormatInt(authenticatedAt.Unix(), 10)
	signature := ed25519.Sign(private, []byte("12:WebAppData\nauth_date="+authTime+"\nuser={\"id\":34}"))
	fields := url.Values{
		"auth_date": {authTime},
		"signature": {base64.URLEncoding.EncodeToString(signature)},
		"hash":      {"ignored-by-telegram-ed25519-verification"},
		"user":      {`{"id":34}`},
	}
	signed := fields.Encode()
	for _, tt := range []struct {
		name string
		data string
		bot  int64
		user int64
		when time.Time
		want bool
	}{
		{"valid", signed, 12, 34, authenticatedAt, true},
		{"wrong bot", signed, 13, 34, authenticatedAt, false},
		{"wrong owner", signed, 12, 35, authenticatedAt, false},
		{"expired", signed, 12, 34, authenticatedAt.Add(6 * time.Minute), false},
		{"duplicate", signed + "&user=%7B%22id%22%3A34%7D", 12, 34, authenticatedAt, false},
		{"missing signature", "auth_date=" + authTime, 12, 34, authenticatedAt, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorizedUser(tt.data, tt.bot, tt.user, tt.when, public); got != tt.want {
				t.Fatalf("authorizedUser returned %v, want %v", got, tt.want)
			}
		})
	}
	if len(productionKey()) != ed25519.PublicKeySize {
		t.Fatal("invalid production Telegram verification key")
	}
}
