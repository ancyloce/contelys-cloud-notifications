// Package identity is Notifications' client of Identity, generated from Identity's contract into
// gen/. Edge's internal listener addresses Identity by its slug, so the client's base address is the
// listener's followed by /identity.
package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/clients/identity/gen"
)

// callTimeout bounds each call to Identity.
const callTimeout = 5 * time.Second

// Client calls Identity through Edge's internal listener.
type Client struct {
	api *gen.ClientWithResponses
}

// New returns a client of Identity behind the internal listener at internalURL.
func New(internalURL string, transport http.RoundTripper) (*Client, error) {
	api, err := gen.NewClientWithResponses(strings.TrimSuffix(internalURL, "/")+"/identity",
		gen.WithHTTPClient(&http.Client{Transport: transport, Timeout: callTimeout}))
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	return &Client{api: api}, nil
}

// Reachable reports whether Identity answers through the internal listener. It reads the key set,
// the one operation of Identity that needs no token.
func (c *Client) Reachable(ctx context.Context) error {
	answer, err := c.api.GetKeysWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	if answer.StatusCode() != http.StatusOK {
		return fmt.Errorf("identity: the key set answered %d", answer.StatusCode())
	}
	return nil
}
