package analytics

import "context"

type noopClient struct{}

func (noopClient) Capture(context.Context, Event) {}

func (noopClient) Identify(context.Context, string, map[string]any) {}

func (noopClient) Close() error {
	return nil
}
