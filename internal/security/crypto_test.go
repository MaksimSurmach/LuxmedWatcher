package security

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealedCredentialsRequireOriginalOwnerKeyAndContents(t *testing.T) {
	key, err := NewCryptor(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := NewCryptor(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{"fixture login", " fixture password "}
	first, err := key.SealCredentials(42, creds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := key.SealCredentials(42, creds)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("repeated encryption produced identical ciphertext")
	}
	got, err := key.OpenCredentials(42, first)
	if err != nil || got != creds {
		t.Fatal("credential round trip failed", err)
	}
	raw, err := base64.StdEncoding.DecodeString(first)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	for _, tc := range []struct {
		name    string
		key     *Cryptor
		owner   int64
		encoded string
	}{
		{"other key", otherKey, 42, first},
		{"other owner", key, 43, first},
		{"changed record", key, 42, base64.StdEncoding.EncodeToString(raw)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if value, err := tc.key.OpenCredentials(tc.owner, tc.encoded); err == nil || value != (Credentials{}) {
				t.Fatal("invalid record yielded credentials")
			}
		})
	}
}
