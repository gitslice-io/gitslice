package clerk

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const jwksPath = "/.well-known/jwks.json"

func jwksURLFromPublishableKey(publishableKey string) (string, error) {
	encoded := strings.TrimSpace(publishableKey)
	encoded = strings.TrimPrefix(encoded, "pk_test_")
	encoded = strings.TrimPrefix(encoded, "pk_live_")
	if encoded == strings.TrimSpace(publishableKey) || encoded == "" {
		return "", errors.New("publishable key must start with pk_test_ or pk_live_")
	}

	decoded, err := decodePublishableKeyPayload(encoded)
	if err != nil {
		return "", fmt.Errorf("decode publishable key: %w", err)
	}

	domain := strings.TrimSuffix(string(decoded), "$")
	if domain == string(decoded) || domain == "" {
		return "", errors.New("publishable key payload must end with '$'")
	}
	if strings.ContainsAny(domain, "/:@") {
		return "", fmt.Errorf("publishable key payload contains invalid frontend API domain %q", domain)
	}

	return "https://" + domain + jwksPath, nil
}

func decodePublishableKeyPayload(encoded string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.RawStdEncoding,
		base64.StdEncoding,
		base64.RawURLEncoding,
		base64.URLEncoding,
	}
	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
