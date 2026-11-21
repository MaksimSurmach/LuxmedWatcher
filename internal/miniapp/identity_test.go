package miniapp

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
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

const testBot int64 = 12

var testAuthAt = time.Unix(1_800_000_000, 0)

func testAuthDate() string { return strconv.FormatInt(testAuthAt.Unix(), 10) }

func newTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

// fields returns a minimal valid field set with the given name/value pairs applied.
func fields(pairs ...string) map[string]string {
	f := map[string]string{"auth_date": testAuthDate(), "user": `{"id":34}`, "hash": "not-signed"}
	for i := 0; i+1 < len(pairs); i += 2 {
		f[pairs[i]] = pairs[i+1]
	}
	return f
}

func without(name string) map[string]string {
	f := fields()
	delete(f, name)
	return f
}

// signedValues signs f with Telegram's formula, so rejections of the result are
// caused by the rule under test rather than by a bad signature.
func signedValues(private ed25519.PrivateKey, bot int64, f map[string]string) url.Values {
	values := url.Values{}
	names := make([]string, 0, len(f))
	for name, value := range f {
		values.Set(name, value)
		if name != "hash" && name != "signature" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = name + "=" + f[name]
	}
	message := strconv.FormatInt(bot, 10) + ":WebAppData\n" + strings.Join(lines, "\n")
	values.Set("signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(message))))
	return values
}

func signedData(private ed25519.PrivateKey, f map[string]string) string {
	return signedValues(private, testBot, f).Encode()
}

func withSignature(values url.Values, signature string) string {
	c := maps.Clone(values)
	c.Set("signature", signature)
	return c.Encode()
}

func expectID(t *testing.T, data string, bot int64, now time.Time, maxAge time.Duration, key ed25519.PublicKey, want int64) {
	t.Helper()
	id, ok := verifiedTelegramID(data, bot, now, maxAge, key)
	if want == 0 {
		if ok || id != 0 {
			t.Fatalf("verifiedTelegramID = (%d, %v), want (0, false)", id, ok)
		}
		return
	}
	if !ok || id != want {
		t.Fatalf("verifiedTelegramID = (%d, %v), want (%d, true)", id, ok, want)
	}
}

func TestVerifiedTelegramIDAccepts(t *testing.T) {
	public, private := newTestKey(t)
	valid := signedValues(private, testBot, fields())
	signature, err := base64.RawURLEncoding.DecodeString(valid.Get("signature"))
	if err != nil {
		t.Fatal(err)
	}
	rehashed := maps.Clone(valid)
	rehashed.Set("hash", "changed-after-signing")

	// Hand-written data-check-string: Telegram sorts by field name, so "x"
	// precedes "x1" even though the line "x1=2" sorts before "x=1".
	message := "12:WebAppData\nauth_date=" + testAuthDate() + "\nuser={\"id\":34}\nx=1\nx1=2"
	ordered := url.Values{
		"auth_date": {testAuthDate()},
		"user":      {`{"id":34}`},
		"x":         {"1"},
		"x1":        {"2"},
		"signature": {base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(message)))},
	}.Encode()

	for _, tt := range []struct {
		name string
		data string
		want int64
	}{
		{"minimal", valid.Encode(), 34},
		{"padded signature", withSignature(valid, base64.URLEncoding.EncodeToString(signature)), 34},
		{"hash is not signed", rehashed.Encode(), 34},
		{"fields sorted by name", ordered, 34},
		{"other telegram fields", signedData(private, fields("query_id", "AAE", "chat_instance", "-1", "start_param", "a=b&c")), 34},
		{"user with whitespace and nested ids", signedData(private, fields("user", ` { "id" : 34 , "first_name" : "A", "photo" : {"id": 1}, "ids": [1, 2] } `)), 34},
		{"largest exact id", signedData(private, fields("user", `{"id":9007199254740991}`)), 9007199254740991},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectID(t, tt.data, testBot, testAuthAt, 5*time.Minute, public, tt.want)
		})
	}
}

func TestVerifiedTelegramIDKey(t *testing.T) {
	public, private := newTestKey(t)
	other, _ := newTestKey(t)
	data := signedData(private, fields())
	for _, tt := range []struct {
		name string
		key  ed25519.PublicKey
	}{
		{"nil", nil},
		{"empty", ed25519.PublicKey{}},
		{"short", public[:ed25519.PublicKeySize-1]},
		{"long", append(slices.Clone(public), 0)},
		{"other key", other},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A wrong-length key must fail closed, not panic inside ed25519.Verify.
			expectID(t, data, testBot, testAuthAt, 5*time.Minute, tt.key, 0)
		})
	}
}

func TestVerifiedTelegramIDSignature(t *testing.T) {
	public, private := newTestKey(t)
	_, otherPrivate := newTestKey(t)
	valid := signedValues(private, testBot, fields())
	text := valid.Get("signature")
	signature, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}

	// Same bytes under a lenient decoder, but non-zero unused trailing bits.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	loose := text[:len(text)-1] + string(alphabet[strings.IndexByte(alphabet, text[len(text)-1])|1])
	if decoded, err := base64.RawURLEncoding.DecodeString(loose); err != nil || !bytes.Equal(decoded, signature) {
		t.Fatalf("test setup: loose encoding decodes to %x, %v", decoded, err)
	}

	tampered := maps.Clone(valid)
	tampered.Set("user", `{"id":35}`)
	missing := maps.Clone(valid)
	missing.Del("signature")

	for _, tt := range []struct {
		name string
		data string
	}{
		{"missing", missing.Encode()},
		{"empty", withSignature(valid, "")},
		{"one padding byte", withSignature(valid, text+"=")},
		{"three padding bytes", withSignature(valid, text+"===")},
		{"truncated", withSignature(valid, text[:len(text)-1])},
		{"non-zero trailing bits", withSignature(valid, loose)},
		{"embedded newline", withSignature(valid, text[:40]+"\n"+text[40:])},
		{"63 bytes", withSignature(valid, base64.RawURLEncoding.EncodeToString(signature[:63]))},
		{"other signer", signedValues(otherPrivate, testBot, fields()).Encode()},
		{"tampered field", tampered.Encode()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectID(t, tt.data, testBot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
}

func TestVerifiedTelegramIDBotBinding(t *testing.T) {
	public, private := newTestKey(t)
	data := signedData(private, fields())
	for _, bot := range []int64{13, 0, -12} {
		t.Run(strconv.FormatInt(bot, 10), func(t *testing.T) {
			expectID(t, data, bot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
	zero := signedValues(private, 0, fields()).Encode()
	expectID(t, zero, 0, testAuthAt, 5*time.Minute, public, 0)
}

func TestVerifiedTelegramIDDuplicates(t *testing.T) {
	public, private := newTestKey(t)
	valid := signedValues(private, testBot, fields())
	data := valid.Encode()
	for _, tt := range []struct {
		name string
		data string
	}{
		{"user", data + "&user=" + url.QueryEscape(`{"id":34}`)},
		{"user differently encoded", data + "&us%65r=" + url.QueryEscape(`{"id":34}`)},
		{"auth_date", data + "&auth_date=" + testAuthDate()},
		{"signature", data + "&signature=" + valid.Get("signature")},
		{"hash", data + "&hash=x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectID(t, tt.data, testBot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
}

func TestVerifiedTelegramIDLexical(t *testing.T) {
	public, private := newTestKey(t)
	data := signedData(private, fields())
	for _, tt := range []struct {
		name string
		data string
	}{
		{"empty input", ""},
		{"leading question mark", "?" + data},
		{"semicolon separators", strings.ReplaceAll(data, "&", ";")},
		{"bad percent escape", data + "&query_id=%zz"},
		{"empty field name", data + "&=x"},
		{"newline in field name", data + "&a%0Ab=c"},
		{"uppercase field name", signedData(private, fields("Query_id", "x"))},
		{"dash in field name", signedData(private, fields("query-id", "x"))},
		{"newline in value", signedData(private, fields("query_id", "a\nuser={\"id\":35}"))},
		{"carriage return in value", signedData(private, fields("query_id", "a\rb"))},
		{"oversized", signedData(private, fields("query_id", strings.Repeat("a", maxInitDataSize)))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectID(t, tt.data, testBot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
}

func TestVerifiedTelegramIDAuthDate(t *testing.T) {
	public, private := newTestKey(t)
	t.Run("missing", func(t *testing.T) {
		expectID(t, signedData(private, without("auth_date")), testBot, testAuthAt, 5*time.Minute, public, 0)
	})
	for _, value := range []string{
		"", "+1800000000", "01800000000", "-1800000000", "0", "1800000000.0", "1.8e9",
		" 1800000000", "1800000000 ", "0x6B49D200", "99999999999999999999",
	} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			expectID(t, signedData(private, fields("auth_date", value)), testBot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
}

func TestVerifiedTelegramIDFreshness(t *testing.T) {
	public, private := newTestKey(t)
	data := signedData(private, fields())
	const maxAge = 5 * time.Minute
	for _, tt := range []struct {
		name   string
		now    time.Time
		maxAge time.Duration
		want   int64
	}{
		{"issued now", testAuthAt, maxAge, 34},
		{"at max age", testAuthAt.Add(maxAge), maxAge, 34},
		{"past max age", testAuthAt.Add(maxAge + time.Second), maxAge, 0},
		{"within clock skew", testAuthAt.Add(-maxClockSkew), maxAge, 34},
		{"beyond clock skew", testAuthAt.Add(-maxClockSkew - time.Second), maxAge, 0},
		{"zero max age", testAuthAt, 0, 0},
		{"negative max age", testAuthAt, -time.Minute, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expectID(t, data, testBot, tt.now, tt.maxAge, public, tt.want)
		})
	}
	farFuture := signedData(private, fields("auth_date", "9223372036854775807"))
	expectID(t, farFuture, testBot, testAuthAt, maxAge, public, 0)
}

func TestVerifiedTelegramIDUser(t *testing.T) {
	public, private := newTestKey(t)
	t.Run("missing", func(t *testing.T) {
		expectID(t, signedData(private, without("user")), testBot, testAuthAt, 5*time.Minute, public, 0)
	})
	for _, user := range []string{
		``, `null`, `34`, `"34"`, `[]`, `[{"id":34}]`, `{}`, `{"first_name":"A"}`,
		`{"id":"34"}`, `{"id":34.0}`, `{"id":3.4e1}`, `{"id":-34}`, `{"id":0}`, `{"id":034}`,
		`{"id":null}`, `{"id":true}`, `{"id":{"id":34}}`,
		`{"id":34,"id":35}`, `{"id":34,"id":34}`, `{"id":35,"id":34}`,
		`{"ID":34}`, `{"Id":34}`, `{"id":34,"ID":35}`,
		`{"id":34} x`, `{"id":34}{}`, `{"id":34}}`, `{"id":34`, `{"id":34,}`,
		`{"id":9007199254740992}`, `{"id":9223372036854775808}`,
	} {
		t.Run(user, func(t *testing.T) {
			expectID(t, signedData(private, fields("user", user)), testBot, testAuthAt, 5*time.Minute, public, 0)
		})
	}
}

func TestAuthorizedUserWrappers(t *testing.T) {
	public, private := newTestKey(t)
	data := signedData(private, fields())
	later := testAuthAt.Add(30 * time.Minute)
	if !authorizedUserWithMaxAge(data, testBot, 34, later, time.Hour, public) {
		t.Fatal("authorizedUserWithMaxAge rejected a fresh owner")
	}
	if authorizedUser(data, testBot, 34, later, public) {
		t.Fatal("authorizedUser accepted data older than five minutes")
	}
	for _, owner := range []int64{0, -34, 35} {
		if authorizedUserWithMaxAge(data, testBot, owner, testAuthAt, time.Hour, public) {
			t.Fatalf("authorizedUserWithMaxAge accepted owner %d", owner)
		}
	}
}
