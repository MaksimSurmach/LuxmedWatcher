package luxmed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"bytes"
)

// luxmedClient — конкретная реализация LuxmedClient.
type luxmedClient struct {
	httpClient *http.Client  // можно кастомизировать timeout, redirect policy и т.д.
	tokens     AuthTokens    // текущие токены/куки
	creds      Credentials   // чтобы при необходимости заново логиниться
	isAuthed   bool          // признак авторизации
}

// NewLuxmedClient создаёт новый экземпляр клиента.
func NewLuxmedClient() LuxmedClient {
	return &luxmedClient{
		httpClient: &http.Client{},
		tokens:     AuthTokens{Cookies: map[string]string{}},
		isAuthed:   false,
	}
}

// Authenticate выполняет POST /PatientPortal/Account/LogIn с логином/паролем.
func (c *luxmedClient) Authenticate(ctx context.Context, creds Credentials) error {
	c.creds = creds

	payload := map[string]string{
		"login":    creds.Username,
		"password": creds.Password,
	}
	body, _ := json.Marshal(payload)

	// create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://portalpacjenta.luxmed.pl/PatientPortal/Account/LogIn",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Succeeded bool   `json:"succeded"`
		Token     string `json:"token"`
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

	// c.tokens.ExpirationTime = 
	// find how to get expiration time from response

	c.isAuthed = true

	return nil
}

func (c *luxmedClient) RefreshTokenIfNeeded(ctx context.Context) error {
	// Здесь можно проверить c.tokens.ExpirationTime
	// и если осталось мало времени, заново вызвать Authenticate(...) или
	// отдельный эндпоинт refresh (если Luxmed позволяет).

	if !c.isAuthed {
		fmt.Println("Refreshing token via re-login...")
		return c.Authenticate(ctx, c.creds)
	}
	return nil
}

func (c *luxmedClient) IsAuthenticated() bool {
	return c.isAuthed
}
