package luxmed

import (
	"LuxmedWatcher/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	log "github.com/sirupsen/logrus"
)

type luxmedClient struct {
	httpClient *http.Client
	tokens     domain.AuthTokens
	creds      domain.Credentials
}

func NewLuxmedClient() LuxmedClient {
	return &luxmedClient{
		httpClient: &http.Client{},
		tokens:     domain.AuthTokens{Cookies: make(map[string]string)},
		creds:      domain.Credentials{},
	}
}

func (c *luxmedClient) Authenticate(ctx context.Context, creds domain.Credentials) error {
	c.creds = creds

	payload := map[string]string{
		"login":    creds.Username,
		"password": creds.Password,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://portalpacjenta.luxmed.pl/PatientPortal/Account/LogIn",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Debug("Error while making request")
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Succeeded    bool   `json:"succeded"`
		Token        string `json:"token"`
		ErrorMessage string `json:"errorMessage"`
	}

	for _, cookie := range resp.Cookies() {
		c.tokens.Cookies[cookie.Name] = cookie.Value
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.Succeeded || resp.StatusCode != http.StatusOK {
		return errors.New("authentication failed: " + result.ErrorMessage)
	}
	c.tokens.AccessToken = result.Token

	return nil
}

func (c *luxmedClient) ReAuthenticate(ctx context.Context) error {
	payload := map[string]string{
		"login":    c.creds.Username,
		"password": c.creds.Password,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://portalpacjenta.luxmed.pl/PatientPortal/Account/LogIn",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Succeeded    bool   `json:"succeded"`
		Token        string `json:"token"`
		ErrorMessage string `json:"errorMessage"`
	}

	for _, cookie := range resp.Cookies() {
		c.tokens.Cookies[cookie.Name] = cookie.Value
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.Succeeded || resp.StatusCode != http.StatusOK {
		return errors.New("authentication failed: " + result.ErrorMessage)
	}
	c.tokens.AccessToken = result.Token

	return nil
}
