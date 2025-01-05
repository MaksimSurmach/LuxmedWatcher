// src/luxmed/auth.go
package luxmed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	persistentcookiejar "github.com/juju/persistent-cookiejar" // Задаем псевдоним
)

// AuthResponse представляет ответ сервера при авторизации
type AuthResponse struct {
	Succeeded     bool   `json:"succeeded"`
	Token         string `json:"token"`
	ErrorMessage  string `json:"errorMessage"`
	ShowCannotLogin bool `json:"showCannotLogin"`
	ReturnUrl string `json:"returnUrl"`
}

// AuthClient управляет авторизацией и токенами
type AuthClient struct {
	BaseURL    string
	Username   string
	Password   string
	Token      string
	HTTPClient *http.Client
	CookieFile string
}

// NewAuthClient создает клиента с поддержкой persistent CookieJar
func NewAuthClient(baseURL, username, password, cookieFile string) (*AuthClient, error) {
	jar, err := persistentcookiejar.New(&persistentcookiejar.Options{Filename: cookieFile})
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	client := &AuthClient{
		BaseURL:    baseURL,
		Username:   username,
		Password:   password,
		HTTPClient: &http.Client{Timeout: 15 * time.Second, Jar: jar},
		CookieFile: cookieFile,
	}

	// Выполняем авторизацию сразу при создании клиента
	if err := client.Authenticate(); err != nil {
		return nil, err
	}

	return client, nil
}

// Authenticate выполняет логин и сохраняет токен
func (a *AuthClient) Authenticate() error {
	// Тело запроса с логином и паролем
	payload := map[string]string{
		"login":    a.Username,
		"password": a.Password,
	}

	// Кодируем JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error encoding JSON: %w", err)
	}

	// Формируем запрос
	authURL := fmt.Sprintf("%s/Account/LogIn", a.BaseURL)
	req, err := http.NewRequest("POST", authURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	// Устанавливаем заголовок
	req.Header.Set("Content-Type", "application/json")

	// Выполняем запрос
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("error sending auth request: %w", err)
	}
	defer resp.Body.Close()

	// Проверка статуса ответа
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed with status: %d", resp.StatusCode)
	}

	// Декодируем ответ
	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("error decoding JSON response: %w", err)
	}
//     fmt.Println(authResp)

// 	// Проверяем результат
// 	if !authResp.Succeeded  {
// 		return fmt.Errorf("Luxmed return auth failed: %s", authResp.ErrorMessage)
// 	}

    if authResp.Token == "" {
        return fmt.Errorf("Token is empty, auth failed")
    }

	// Сохраняем токен
	a.Token = authResp.Token
	log.Println("Authenticated successfully")
	return nil
}

// AddAuthHeader добавляет токен в заголовок Authorization
func (a *AuthClient) AddAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.Token)
}

// EnsureAuthenticated гарантирует, что клиент аутентифицирован
func (a *AuthClient) EnsureAuthenticated() error {
	if a.Token == "" {
		log.Println("Token missing, authenticating...")
		return a.Authenticate()
	}

    // Send request, check if token is valid
    req, err := http.NewRequest("GET", a.BaseURL + "/NewPortal/UserProfile/GetUser", nil)
    if err != nil {
        return fmt.Errorf("error creating request: %w", err)
    }

    a.AddAuthHeader(req)

    resp, err := a.HTTPClient.Do(req)
    if err != nil {
        return fmt.Errorf("error sending request: %w", err)
    }

    if resp.StatusCode == http.StatusUnauthorized {
        log.Println("Token expired, re-authenticating...")
        return a.Authenticate()
    }


	return nil
}
