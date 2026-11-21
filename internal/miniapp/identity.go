package miniapp

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Telegram's published production Mini App signing key.
const telegramPublicKey = "e7bf03a2fa4602af4580703d88dda5bb59f32ed8b02a56c187fe7d34caed242d"

const (
	// maxInitDataSize bounds the work done on untrusted input before verification.
	maxInitDataSize = 12 << 10
	// maxClockSkew tolerates an auth_date slightly ahead of the local clock.
	maxClockSkew = 30 * time.Second
	// maxTelegramID is the largest integer a JavaScript client represents exactly.
	// Telegram guarantees user IDs fit in 52 significant bits, so anything larger
	// would be read differently by the Mini App frontend and this backend.
	maxTelegramID = 1<<53 - 1
)

// signatureTextSize is the unpadded base64url length of an Ed25519 signature.
var signatureTextSize = base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize)

func authorizedUser(raw string, botID, ownerID int64, now time.Time, key ed25519.PublicKey) bool {
	return authorizedUserWithMaxAge(raw, botID, ownerID, now, 5*time.Minute, key)
}

func authorizedUserWithMaxAge(raw string, botID, ownerID int64, now time.Time, maxAge time.Duration, key ed25519.PublicKey) bool {
	id, ok := verifiedTelegramID(raw, botID, now, maxAge, key)
	return ok && ownerID > 0 && id == ownerID
}

// verifiedTelegramID returns the Telegram user ID carried in Mini App init data
// only if the data is unambiguous, carries a valid Ed25519 signature from key
// for botID using Telegram's third-party validation formula
//
//	"<bot_id>:WebAppData\n" + sorted "key=value" lines (excluding hash and signature)
//
// and auth_date is no older than maxAge (and at most maxClockSkew in the future).
// Any doubt yields (0, false).
func verifiedTelegramID(raw string, botID int64, now time.Time, maxAge time.Duration, key ed25519.PublicKey) (int64, bool) {
	// ed25519.Verify panics on a key of the wrong length; fail closed instead.
	if len(key) != ed25519.PublicKeySize || botID <= 0 || maxAge <= 0 || raw == "" || len(raw) > maxInitDataSize {
		return 0, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, false
	}
	names := make([]string, 0, len(values))
	for name, entries := range values {
		// Exactly one value per field, and no byte that could forge a line or a
		// key/value boundary in the data-check-string.
		if len(entries) != 1 || !validFieldName(name) || strings.ContainsAny(entries[0], "\r\n") {
			return 0, false
		}
		if name != "hash" && name != "signature" {
			names = append(names, name)
		}
	}
	signature, ok := decodeSignature(values.Get("signature"))
	if !ok {
		return 0, false
	}
	// Telegram sorts by field name; sorting whole "name=value" lines differs
	// when one name is a prefix of another (e.g. "x" vs "x1").
	slices.Sort(names)
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = name + "=" + values.Get(name)
	}
	message := strconv.FormatInt(botID, 10) + ":WebAppData\n" + strings.Join(lines, "\n")
	if !ed25519.Verify(key, []byte(message), signature) {
		return 0, false
	}

	seconds, ok := canonicalPositive(values.Get("auth_date"), 1<<62)
	if !ok {
		return 0, false
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age < -maxClockSkew || age > maxAge {
		return 0, false
	}
	return telegramUserID(values.Get("user"))
}

// validFieldName accepts the lexical shape of every Telegram init data field
// name (query_id, user, auth_date, ...). Anything else is rejected rather than
// signed, which also rules out empty names and names containing '=' or newlines.
func validFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// decodeSignature accepts exactly one textual form of a 64-byte signature:
// canonical base64url, optionally with its standard "==" padding.
func decodeSignature(text string) ([]byte, bool) {
	if len(text) == signatureTextSize+2 && strings.HasSuffix(text, "==") {
		text = text[:signatureTextSize]
	}
	if len(text) != signatureTextSize {
		return nil, false
	}
	// Strict rejects non-zero trailing bits, so no two strings map to one signature.
	signature, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, false
	}
	return signature, true
}

// canonicalPositive parses a plain decimal integer in [1, limit]: digits only,
// no sign, no leading zero, no exponent or fraction.
func canonicalPositive(text string, limit int64) (int64, bool) {
	if text == "" || len(text) > 19 || text[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 || n > limit {
		return 0, false
	}
	return n, true
}

// telegramUserID extracts the "id" member of the signed user JSON object.
// Unmarshalling into a struct would silently keep the last of duplicate keys
// and match "ID" or "Id" case-insensitively, so the top-level object is walked
// explicitly: duplicate or case-variant id keys, non-objects and trailing data
// are rejected, and the id must be a canonical positive JSON integer.
func telegramUserID(text string) (int64, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return 0, false
	}
	seen := make(map[string]bool)
	var id json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, false
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return 0, false
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return 0, false
		}
		switch {
		case name == "id":
			id = value
		case strings.EqualFold(name, "id"):
			return 0, false
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return 0, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, false
	}
	return canonicalPositive(string(bytes.TrimSpace(id)), maxTelegramID)
}

func productionKey() ed25519.PublicKey {
	key, _ := hex.DecodeString(telegramPublicKey)
	return key
}
