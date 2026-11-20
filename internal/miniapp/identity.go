package miniapp

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Telegram's published production Mini App signing key.
const telegramPublicKey = "e7bf03a2fa4602af4580703d88dda5bb59f32ed8b02a56c187fe7d34caed242d"

func authorizedUser(raw string, botID, ownerID int64, now time.Time, key ed25519.PublicKey) bool {
	if len(raw) > 12<<10 {
		return false
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(values.Get("signature"), "="))
	if err != nil {
		return false
	}
	lines := make([]string, 0, len(values))
	for name, entries := range values {
		if len(entries) != 1 || strings.ContainsAny(name, "\r\n=") || strings.ContainsAny(entries[0], "\r\n") {
			return false
		}
		if name != "hash" && name != "signature" {
			lines = append(lines, name+"="+entries[0])
		}
	}
	slices.Sort(lines)
	message := strconv.FormatInt(botID, 10) + ":WebAppData\n" + strings.Join(lines, "\n")
	if !ed25519.Verify(key, []byte(message), signature) {
		return false
	}
	seconds, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	age := now.Sub(time.Unix(seconds, 0))
	if err != nil || age < -30*time.Second || age > 5*time.Minute {
		return false
	}
	var user struct {
		ID int64 `json:"id"`
	}
	return json.Unmarshal([]byte(values.Get("user")), &user) == nil && user.ID > 0 && user.ID == ownerID
}

func productionKey() ed25519.PublicKey {
	key, _ := hex.DecodeString(telegramPublicKey)
	return key
}
