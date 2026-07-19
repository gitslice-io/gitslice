package analytics

import (
	"context"

	posthog "github.com/posthog/posthog-go"
)

type postHogClient struct {
	client posthog.Client
}

func newPostHogClient(apiKey, host string) (Client, error) {
	config := posthog.Config{}
	if host != "" {
		config.Endpoint = host
	}
	client, err := posthog.NewWithConfig(apiKey, config)
	if err != nil {
		return nil, err
	}
	return &postHogClient{client: client}, nil
}

func (c *postHogClient) Capture(ctx context.Context, e Event) {
	if c == nil || c.client == nil {
		return
	}
	_ = posthog.EnqueueWithContext(ctx, c.client, posthog.Capture{
		DistinctId: e.DistinctID,
		Event:      e.Name,
		Properties: postHogProperties(e.Props),
	})
}

func (c *postHogClient) Identify(ctx context.Context, distinctID string, props map[string]any) {
	if c == nil || c.client == nil {
		return
	}
	_ = posthog.EnqueueWithContext(ctx, c.client, posthog.Identify{
		DistinctId: distinctID,
		Properties: postHogProperties(props),
	})
}

func (c *postHogClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func postHogProperties(props map[string]any) posthog.Properties {
	if len(props) == 0 {
		return nil
	}
	converted := make(posthog.Properties, len(props))
	for key, value := range props {
		converted[key] = value
	}
	return converted
}
