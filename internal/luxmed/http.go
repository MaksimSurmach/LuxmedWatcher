package luxmed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"time"
)

const (
	portalURL       = "https://portalpacjenta.luxmed.pl"
	LoginURL        = "/PatientPortal/Account/LogIn"
	XsrfTokenURL    = "/PatientPortal/NewPortal/security/getforgerytoken"
	maxResponseSize = 8 << 20
	maxRetryWait    = 3 * time.Second
)

var (
	ErrAuthentication = errors.New("luxmed authentication required or rejected")
	ErrUnavailable    = errors.New("luxmed temporarily unavailable")
	ErrResponse       = errors.New("unexpected luxmed response")
)

// HTTPError reports an upstream status without including query parameters or response data.
type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	path       string
	login      bool
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("luxmed %s: HTTP %d", e.path, e.StatusCode)
}

func (e *HTTPError) Is(target error) bool {
	return target == ErrUnavailable && (e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError)
}

// HTTPClient holds one account's session and is intended for serial use.
type HTTPClient struct {
	client      *http.Client
	baseURL     string
	accessToken string
	xsrfToken   string
	creds       Credentials
	rawPayloads bool
}

func NewHTTPClient(rawPayloads bool) *HTTPClient {
	return newHTTPClient(portalURL, rawPayloads)
}

func newHTTPClient(baseURL string, rawPayloads bool) *HTTPClient {
	return &HTTPClient{
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL:     baseURL,
		rawPayloads: rawPayloads,
	}
}

func (c *HTTPClient) Authenticate(ctx context.Context, creds Credentials) error {
	c.creds = creds
	c.accessToken = ""
	c.xsrfToken = ""
	c.client.Jar, _ = cookiejar.New(nil)
	if strings.TrimSpace(creds.Login) == "" || creds.Password == "" {
		return ErrAuthentication
	}
	body, _ := json.Marshal(map[string]string{"login": creds.Login, "password": creds.Password})
	data, err := c.request(ctx, http.MethodPost, LoginURL, body)
	var status *HTTPError
	if errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden) {
		return fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	if err != nil {
		return err
	}
	var result struct {
		Succeeded *bool  `json:"succeded"`
		Token     string `json:"token"`
	}
	if json.Unmarshal(data, &result) != nil || result.Succeeded == nil {
		return fmt.Errorf("luxmed login: %w", ErrResponse)
	}
	if !*result.Succeeded {
		return ErrAuthentication
	}
	if result.Token == "" {
		return fmt.Errorf("luxmed login missing token: %w", ErrResponse)
	}

	c.accessToken = result.Token
	data, err = c.request(ctx, http.MethodGet, XsrfTokenURL, nil)
	if ctx.Err() != nil {
		c.accessToken = ""
		return ctx.Err()
	}
	if errors.Is(err, ErrUnavailable) {
		c.accessToken = ""
		return err
	}
	// The read-only portal endpoints also work without an XSRF token on older deployments.
	var xsrf struct {
		Token string `json:"token"`
	}
	if err == nil && json.Unmarshal(data, &xsrf) == nil {
		c.xsrfToken = xsrf.Token
	}
	return nil
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, target any) error {
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, target) != nil {
		return fmt.Errorf("luxmed %s: %w", strings.SplitN(endpoint, "?", 2)[0], ErrResponse)
	}
	return nil
}

func (c *HTTPClient) get(ctx context.Context, endpoint string) ([]byte, error) {
	if c.accessToken == "" {
		if err := c.Authenticate(ctx, c.creds); err != nil {
			return nil, err
		}
	}
	data, err := c.getWithRetry(ctx, endpoint)
	var status *HTTPError
	if !errors.As(err, &status) || !status.login {
		return data, err
	}
	if err := c.Authenticate(ctx, c.creds); err != nil {
		return nil, err
	}
	data, err = c.getWithRetry(ctx, endpoint)
	if errors.As(err, &status) && status.login {
		c.accessToken = ""
		return nil, fmt.Errorf("%w: %w", ErrAuthentication, err)
	}
	return data, err
}

func (c *HTTPClient) getWithRetry(ctx context.Context, endpoint string) ([]byte, error) {
	var err error
	for attempt := range 3 {
		var data []byte
		data, err = c.request(ctx, http.MethodGet, endpoint, nil)
		if !errors.Is(err, ErrUnavailable) || attempt == 2 {
			return data, err
		}

		base := (200 * time.Millisecond) << attempt
		jitter := rand.N(base)
		delay := base + jitter
		var status *HTTPError
		if errors.As(err, &status) && status.RetryAfter > 0 {
			if status.RetryAfter > maxRetryWait {
				return nil, err
			}
			delay = max(base, status.RetryAfter) + jitter
		}
		// Let the scheduler honor long server backoff without blocking a worker.
		if delay > maxRetryWait {
			return nil, err
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func (c *HTTPClient) request(ctx context.Context, method, endpoint string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create luxmed request: %w", ErrResponse)
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.accessToken != "" {
		req.Header.Set("authorization-token", c.accessToken)
	}
	if c.xsrfToken != "" {
		req.Header.Set("X-XSRF-TOKEN", c.xsrfToken)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Transport errors may contain the URL and its sensitive query.
		return nil, fmt.Errorf("luxmed %s request failed: %w", req.URL.Path, ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		login := resp.StatusCode == http.StatusUnauthorized
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location, err := resp.Location()
			login = err == nil && location.Host == req.URL.Host && location.Scheme == req.URL.Scheme &&
				strings.HasSuffix(strings.ToLower(location.Path), "/account/login")
		}
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			path:       req.URL.Path,
			login:      login,
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("luxmed %s read failed: %w", req.URL.Path, ErrUnavailable)
	}
	if len(data) > maxResponseSize {
		return nil, fmt.Errorf("luxmed %s response exceeds %d bytes: %w", req.URL.Path, maxResponseSize, ErrResponse)
	}
	return data, nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		const maxDuration = time.Duration(1<<63 - 1)
		if seconds > uint64(maxDuration/time.Second) {
			return maxDuration
		}
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return max(0, until.Sub(now))
	}
	return 0
}
