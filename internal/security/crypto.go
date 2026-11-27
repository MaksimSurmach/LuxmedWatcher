package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

type Cryptor struct {
	aead cipher.AEAD
}

func NewCryptor(masterKey []byte) (*Cryptor, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cryptor{aead: aead}, nil
}

func (c *Cryptor) Encrypt(plain string) (string, error) {
	return c.seal([]byte(plain), nil)
}

func (c *Cryptor) seal(plain, aad []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plain, aad)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *Cryptor) Decrypt(encoded string) (string, error) {
	return c.open(encoded, nil)
}

func (c *Cryptor) open(encoded string, aad []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	nonceSize := c.aead.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("encrypted value is too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

type Credentials struct {
	Login    string
	Password string
}

func credentialAAD(userID int64) []byte {
	return []byte("luxmed:credentials:v1:" + strconv.FormatInt(userID, 10))
}

func (c *Cryptor) SealCredentials(userID int64, creds Credentials) (string, error) {
	payload, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return c.seal(payload, credentialAAD(userID))
}

func (c *Cryptor) OpenCredentials(userID int64, encoded string) (Credentials, error) {
	payload, err := c.open(encoded, credentialAAD(userID))
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	err = json.Unmarshal([]byte(payload), &creds)
	return creds, err
}
