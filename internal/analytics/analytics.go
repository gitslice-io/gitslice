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
// no-op client. host may be empty (SDK default endpoint is used). environment,
// when non-empty, is attached to every captured event as the "environment"
// property so one PostHog project can serve staging and production.
func New(apiKey, host, environment string) (Client, error) {
	if apiKey == "" {
		return noopClient{}, nil
	}
	return newPostHogClient(apiKey, host, environment)
}
