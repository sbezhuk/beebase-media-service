// Package hiveclient implements application/media.HiveVerifier against the
// real hive-service over HTTP.
package hiveclient

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
)

const requestTimeout = 5 * time.Second

// Client verifies hive ownership by forwarding the caller's own access
// token to hive-service's GET /api/v1/hives/{id}, and trusting
// hive-service's own ownership check: a 200 means whoever holds that
// token owns that hive (and, transitively, its apiary), a 404 means they
// don't. This service never queries hive or apiary ownership itself.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client that calls hive-service at baseURL (e.g.
// "http://hive-service:8080").
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// Verify implements application/media.HiveVerifier.
func (c *Client) Verify(ctx context.Context, accessToken string, hiveID uuid.UUID) error {
	url := fmt.Sprintf("%s/api/v1/hives/%s", c.baseURL, hiveID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("hiveclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hiveclient: call hive-service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return appmedia.ErrHiveNotFound
	default:
		// Anything else (401, 5xx, ...) is unexpected for a token this
		// service already verified itself: fail closed with a distinct,
		// observable error rather than silently treating it as "not
		// found", which would mask a real problem (e.g. hive-service
		// misconfigured or unreachable) as a client-facing 404.
		return fmt.Errorf("hiveclient: unexpected status %d from hive-service", resp.StatusCode)
	}
}
