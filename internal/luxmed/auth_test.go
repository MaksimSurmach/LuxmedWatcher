package luxmed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type stubTransport struct {
	status int
	body   string
}

func (s stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.status, Status: http.StatusText(s.status), Body: io.NopCloser(strings.NewReader(s.body)), Header: http.Header{}}, nil
}

// Only a real credential rejection may stop a user's watches; a LuxMed outage must not.
func TestAuthenticateSeparatesRejectionFromOutage(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"wrong password", http.StatusOK, `{"succeded":false,"errorMessage":"bad password"}`, true},
		{"unauthorized", http.StatusUnauthorized, `{"succeded":false}`, true},
		{"server error", http.StatusServiceUnavailable, `{"succeded":false}`, false},
	}
	for _, tc := range cases {
		c := NewHTTPClient(false)
		c.client.Transport = stubTransport{tc.status, tc.body}
		err := c.Authenticate(context.Background(), Credentials{Login: "a", Password: "b"})
		if err == nil || errors.Is(err, ErrAuthRejected) != tc.rejected {
			t.Errorf("%s: err=%v, want rejected=%v", tc.name, err, tc.rejected)
		}
	}
}
