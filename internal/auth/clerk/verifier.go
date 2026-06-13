package clerk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config contains the Clerk configuration needed to verify session JWTs.
type Config struct {
	SecretKey      string // CLERK_SECRET_KEY (sk_test_... / sk_live_...)
	PublishableKey string // CLERK_PUBLISHABLE_KEY (pk_test_... / pk_live_...)
	// JWKSURL optionally overrides the JWKS endpoint. When empty it is derived
	// from PublishableKey: the part after "pk_test_"/"pk_live_" is base64 of
	// "<frontend-api-domain>$"; JWKS lives at
	// https://<frontend-api-domain>/.well-known/jwks.json
	JWKSURL string
}

// ConfigFromEnv reads CLERK_SECRET_KEY, CLERK_PUBLISHABLE_KEY, and CLERK_JWKS_URL.
func ConfigFromEnv() Config {
	return Config{
		SecretKey:      os.Getenv("CLERK_SECRET_KEY"),
		PublishableKey: os.Getenv("CLERK_PUBLISHABLE_KEY"),
		JWKSURL:        os.Getenv("CLERK_JWKS_URL"),
	}
}

// Claims contains the authenticated Clerk session identity.
type Claims struct {
	Subject string         // the "sub" claim = Clerk user id (user_...)
	Email   string         // best-effort: "email" claim if present, else ""
	Issuer  string         // the "iss" claim, if present
	Expiry  time.Time      // the "exp" claim
	Raw     map[string]any // all claims, for callers that need more
}

// Verifier verifies Clerk-issued session JWTs.
type Verifier struct {
	keys keySource
	now  func() time.Time
}

// NewVerifier builds a Clerk session verifier. It returns an error when neither
// JWKSURL nor a usable PublishableKey can identify a JWKS endpoint.
func NewVerifier(cfg Config) (*Verifier, error) {
	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if jwksURL == "" {
		var err error
		jwksURL, err = jwksURLFromPublishableKey(cfg.PublishableKey)
		if err != nil {
			if strings.TrimSpace(cfg.SecretKey) != "" {
				return nil, fmt.Errorf("clerk: no JWKS source can be determined from secret key alone; set PublishableKey or JWKSURL: %w", err)
			}
			return nil, fmt.Errorf("clerk: no JWKS source can be determined: %w", err)
		}
	}
	if err := validateJWKSURL(jwksURL); err != nil {
		return nil, err
	}

	return newVerifier(&remoteKeySource{
		url:    jwksURL,
		client: http.DefaultClient,
	}), nil
}

// Verify parses and cryptographically verifies a Clerk session JWT against the
// configured JWKS, validates exp/nbf, and returns authenticated claims.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	if v == nil || v.keys == nil {
		return nil, errors.New("clerk: verifier is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	mapClaims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.timeNow),
	)

	parsed, err := parser.ParseWithClaims(token, mapClaims, func(parsed *jwt.Token) (any, error) {
		return v.keys.Key(ctx, parsed)
	})
	if err != nil {
		return nil, fmt.Errorf("clerk: invalid session token: %w", err)
	}
	if parsed == nil || !parsed.Valid {
		return nil, errors.New("clerk: invalid session token")
	}

	subject, err := mapClaims.GetSubject()
	if err != nil || strings.TrimSpace(subject) == "" {
		return nil, errors.New("clerk: token missing subject")
	}
	expiresAt, err := mapClaims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		return nil, errors.New("clerk: token missing expiry")
	}

	issuer, _ := mapClaims.GetIssuer()
	email, _ := mapClaims["email"].(string)

	return &Claims{
		Subject: subject,
		Email:   email,
		Issuer:  issuer,
		Expiry:  expiresAt.Time,
		Raw:     cloneClaims(mapClaims),
	}, nil
}

func newVerifier(keys keySource) *Verifier {
	return &Verifier{
		keys: keys,
		now:  time.Now,
	}
}

func (v *Verifier) timeNow() time.Time {
	if v == nil || v.now == nil {
		return time.Now()
	}
	return v.now()
}

func cloneClaims(claims jwt.MapClaims) map[string]any {
	out := make(map[string]any, len(claims))
	for k, v := range claims {
		out[k] = v
	}
	return out
}

func validateJWKSURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("clerk: invalid JWKS URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("clerk: invalid JWKS URL %q: expected http or https scheme", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("clerk: invalid JWKS URL %q: missing host", raw)
	}
	return nil
}
