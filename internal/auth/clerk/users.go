package clerk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIURL is the Clerk Backend API base URL.
const DefaultAPIURL = "https://api.clerk.com"

// UserClient reads user profiles from the Clerk Backend API. Session JWTs do
// not carry email verification state by default, so flows that must trust an
// email address (agent claims) ask Clerk directly.
type UserClient struct {
	secretKey string
	baseURL   string
	http      *http.Client
}

// NewUserClient returns a Backend API client, or nil when secretKey is empty.
func NewUserClient(secretKey string) *UserClient {
	secretKey = strings.TrimSpace(secretKey)
	if secretKey == "" {
		return nil
	}
	return &UserClient{
		secretKey: secretKey,
		baseURL:   DefaultAPIURL,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// WithBaseURL points the client at another Backend API host (tests).
func (c *UserClient) WithBaseURL(baseURL string) *UserClient {
	clone := *c
	clone.baseURL = strings.TrimRight(baseURL, "/")
	return &clone
}

type clerkUser struct {
	EmailAddresses []struct {
		EmailAddress string `json:"email_address"`
		Verification *struct {
			Status string `json:"status"`
		} `json:"verification"`
	} `json:"email_addresses"`
}

// VerifiedEmails returns the user's email addresses whose verification status
// is "verified". Unverified addresses are never returned.
func (c *UserClient) VerifiedEmails(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("clerk: user id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/users/"+url.PathEscape(userID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clerk: get user: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("clerk: get user: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clerk: get user: unexpected status %d", resp.StatusCode)
	}
	var user clerkUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("clerk: decode user: %w", err)
	}
	var out []string
	for _, address := range user.EmailAddresses {
		if address.Verification != nil && address.Verification.Status == "verified" && strings.TrimSpace(address.EmailAddress) != "" {
			out = append(out, address.EmailAddress)
		}
	}
	return out, nil
}
