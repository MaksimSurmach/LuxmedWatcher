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

// newAuthRequest creates a new http.Request with the given method, url and body
func (c *luxmedClient) newAuthRequest(ctx context.Context, method, url string, body *bytes.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// set authorization token if it exists
	if c.tokens.AccessToken != "" {
		req.Header.Set("authorization-token", c.tokens.AccessToken)
	}
	// set XSRF token if it exists
	if c.tokens.XsrfToken != "" {
		req.Header.Set("X-XSRF-TOKEN", c.tokens.XsrfToken)
	}
	// set cookies
	for k, v := range c.tokens.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	return req, nil
}

func (c *luxmedClient) Authenticate(ctx context.Context, creds domain.Credentials) error {
	c.creds = creds

	payload := map[string]string{
		"login":    creds.Username,
		"password": creds.Password,
	}
	body, _ := json.Marshal(payload)

	req, err := c.newAuthRequest(ctx, http.MethodPost, LoginURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

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

	xsrfToken, err := c.GetXsrfToken(ctx)
	if err == nil {
		c.tokens.XsrfToken = xsrfToken
	} else {
		log.Warn("Failed to fetch XSRF token:", err)
	}

	return nil
}

func (c *luxmedClient) GetXsrfToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, XsrfTokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("failed to fetch XSRF token")
	}
	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	return tokenResp.Token, nil
}

func (c *luxmedClient) IsLoggedIn() bool {
	req, err := http.NewRequest(http.MethodGet, RefreshURL, nil)
	if err != nil {
		return false
	}
	if c.tokens.AccessToken != "" {
		req.Header.Set("authorization-token", c.tokens.AccessToken)
	}
	for k, v := range c.tokens.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
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

func (c *luxmedClient) RefreshTokenIfNeeded(ctx context.Context) error {
	if !c.IsLoggedIn() {
		return c.ReAuthenticate(ctx)
	}
	return nil
}