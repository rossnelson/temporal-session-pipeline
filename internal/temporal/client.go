package temporal

import (
	"fmt"
	"os"

	"go.temporal.io/sdk/client"
)

// NewClient creates a Temporal client connection.
// Uses TEMPORAL_HOST env var, defaults to localhost:7233.
func NewClient() (client.Client, error) {
	host := os.Getenv("TEMPORAL_HOST")
	if host == "" {
		host = "localhost:7233"
	}

	c, err := client.Dial(client.Options{
		HostPort: host,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to Temporal at %s: %w", host, err)
	}

	return c, nil
}
