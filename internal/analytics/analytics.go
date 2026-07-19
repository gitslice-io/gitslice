package analytics

import "context"

type Event struct {
	Name       string
	DistinctID string
	Props      map[string]any
}

type Client interface {
	Capture(ctx context.Context, e Event)
	Identify(ctx context.Context, distinctID string, props map[string]any)
	Close() error
}

// New returns a PostHog-backed client when apiKey is non-empty, otherwise a
// no-op client. host may be empty (SDK default endpoint is used).
func New(apiKey, host string) (Client, error) {
	if apiKey == "" {
		return noopClient{}, nil
	}
	return newPostHogClient(apiKey, host)
}
