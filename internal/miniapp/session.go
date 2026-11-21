package miniapp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"
)

const dashboardSessionLifetime = time.Hour

// Dashboard sessions are held in the Mini App's memory and expire on app restart.
type sessions struct {
	key [32]byte
}

func newSessions() sessions {
	var s sessions
	rand.Read(s.key[:])
	return s
}

func (s *sessions) issue(id int64, now time.Time) string {
	var data [16]byte
	binary.BigEndian.PutUint64(data[:8], uint64(id))
	binary.BigEndian.PutUint64(data[8:], uint64(now.Add(dashboardSessionLifetime).Unix()))
	mac := hmac.New(sha256.New, s.key[:])
	mac.Write(data[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(data[:]))
}

func (s *sessions) verify(authorization string, now time.Time) (int64, bool) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || len(token) != base64.RawURLEncoding.EncodedLen(16+sha256.Size) {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 16+sha256.Size {
		return 0, false
	}
	mac := hmac.New(sha256.New, s.key[:])
	mac.Write(raw[:16])
	if !hmac.Equal(raw[16:], mac.Sum(nil)) {
		return 0, false
	}
	id := int64(binary.BigEndian.Uint64(raw[:8]))
	until := time.Unix(int64(binary.BigEndian.Uint64(raw[8:16])), 0)
	return id, id > 0 && now.Before(until)
}
