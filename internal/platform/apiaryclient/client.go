// Package apiaryclient implements application/media.ApiaryVerifier against
// the real apiary-service over HTTP.
package apiaryclient

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
)

const requestTimeout = 5 * time.Second

// Client verifies apiary ownership by forwarding the caller's own access
// token to apiary-service's GET /api/v1/apiaries/{id}, and trusting
// apiary-service's own ownership check: a 200 means whoever holds that
// token owns that apiary, a 404 means they don't. This service never
// queries apiary ownership itself.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client that calls apiary-service at baseURL (e.g.
// "http://apiary-service:8080").
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Verify implements application/media.ApiaryVerifier.
func (c *Client) Verify(ctx context.Context, accessToken string, apiaryID uuid.UUID) error {
	url := fmt.Sprintf("%s/api/v1/apiaries/%s", c.baseURL, apiaryID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("apiaryclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apiaryclient: call apiary-service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return appmedia.ErrApiaryNotFound
	default:
		// Anything else (401, 5xx, ...) is unexpected for a token this
		// service already verified itself: fail closed with a distinct,
		// observable error rather than silently treating it as "not
		// found", which would mask a real problem (e.g. apiary-service
		// misconfigured or unreachable) as a client-facing 404.
		return fmt.Errorf("apiaryclient: unexpected status %d from apiary-service", resp.StatusCode)
	}
}
