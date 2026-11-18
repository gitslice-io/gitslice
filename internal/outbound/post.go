package outbound

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

const defaultDrainBytes = 1 << 20

// DefaultRetryDelays is the standard 6-step outbound HTTP retry schedule.
var DefaultRetryDelays = []time.Duration{
	time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
}

// RetryDelay returns the backoff wait after a 1-indexed failed outbound HTTP
// attempt, clamped to the bounds of DefaultRetryDelays.
func RetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return DefaultRetryDelays[0]
	}
	idx := attempt - 1
	if idx >= len(DefaultRetryDelays) {
		idx = len(DefaultRetryDelays) - 1
	}
	return DefaultRetryDelays[idx]
}

// PostJSONRequest describes a signed outbound JSON POST request.
type PostJSONRequest struct {
	Client           *http.Client
	URL              string
	Body             []byte
	UserAgent        string
	Headers          map[string]string
	SignSecret       string
	SignHeader       string
	Signature        string
	MaxResponseBytes int64
}

// PostJSONResult holds the HTTP status and optional bounded response body from
// PostSignedJSON.
type PostJSONResult struct {
	StatusCode int
	Status     string
	Body       []byte
}

// PostSignedJSON sends a JSON POST request with optional HMAC body signing and
// bounded response body draining/reading.
func PostSignedJSON(ctx context.Context, in PostJSONRequest) (PostJSONResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.URL, bytes.NewReader(in.Body))
	if err != nil {
		return PostJSONResult{}, err
	}
	for name, value := range in.Headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	if in.UserAgent != "" {
		req.Header.Set("User-Agent", in.UserAgent)
	}
	switch {
	case in.Signature != "" && in.SignHeader != "":
		req.Header.Set(in.SignHeader, in.Signature)
	case in.SignSecret != "":
		header := in.SignHeader
		if header == "" {
			header = SignatureHeader
		}
		req.Header.Set(header, Sign(in.SignSecret, in.Body))
	}

	client := in.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return PostJSONResult{}, err
	}
	defer resp.Body.Close()

	var respBody []byte
	if in.MaxResponseBytes > 0 {
		respBody, _ = io.ReadAll(io.LimitReader(resp.Body, in.MaxResponseBytes))
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, defaultDrainBytes))
	}
	return PostJSONResult{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       respBody,
	}, nil
}
