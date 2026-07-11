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
	SecretKey         string   // CLERK_SECRET_KEY (sk_test_... / sk_live_...)
	PublishableKey    string   // CLERK_PUBLISHABLE_KEY (pk_test_... / pk_live_...)
	Issuer            string   // CLERK_ISSUER (for example https://clerk.gitslice.io)
	AuthorizedParties []string // CLERK_AUTHORIZED_PARTIES (comma-separated origins)
	// JWKSURL optionally overrides the JWKS endpoint. When empty it is derived
	// from PublishableKey: the part after "pk_test_"/"pk_live_" is base64 of
	// "<frontend-api-domain>$"; JWKS lives at
	// https://<frontend-api-domain>/.well-known/jwks.json
	JWKSURL string
}

// ConfigFromEnv reads the Clerk session-verification environment.
func ConfigFromEnv() Config {
	return Config{
		SecretKey:         os.Getenv("CLERK_SECRET_KEY"),
		PublishableKey:    os.Getenv("CLERK_PUBLISHABLE_KEY"),
		Issuer:            os.Getenv("CLERK_ISSUER"),
		AuthorizedParties: commaSeparatedValues(os.Getenv("CLERK_AUTHORIZED_PARTIES")),
		JWKSURL:           os.Getenv("CLERK_JWKS_URL"),
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
	keys              keySource
	issuer            string
	authorizedParties map[string]struct{}
	now               func() time.Time
}

// NewVerifier builds a Clerk session verifier. It fails closed when the key
// source, expected issuer, or authorized-party allowlist cannot be determined.
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

	issuer := strings.TrimSpace(cfg.Issuer)
	if issuer == "" {
		derivedJWKSURL, err := jwksURLFromPublishableKey(cfg.PublishableKey)
		if err != nil {
			return nil, fmt.Errorf("clerk: CLERK_ISSUER is required when it cannot be derived from CLERK_PUBLISHABLE_KEY: %w", err)
		}
		issuer = strings.TrimSuffix(derivedJWKSURL, jwksPath)
	}
	issuer, err := normalizeOrigin(issuer)
	if err != nil {
		return nil, fmt.Errorf("clerk: invalid issuer: %w", err)
	}
	authorizedParties, err := normalizeAuthorizedParties(cfg.AuthorizedParties)
	if err != nil {
		return nil, err
	}
	if len(authorizedParties) == 0 {
		return nil, errors.New("clerk: CLERK_AUTHORIZED_PARTIES is required")
	}

	return newVerifier(&remoteKeySource{
		url:    jwksURL,
		client: http.DefaultClient,
	}, issuer, authorizedParties), nil
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
		jwt.WithIssuer(v.issuer),
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
	if rawAuthorizedParty, ok := mapClaims["azp"]; ok && rawAuthorizedParty != nil {
		authorizedParty, ok := rawAuthorizedParty.(string)
		if !ok {
			return nil, errors.New("clerk: token has invalid authorized party")
		}
		if authorizedParty != "" {
			if _, ok := v.authorizedParties[authorizedParty]; !ok {
				return nil, fmt.Errorf("clerk: token authorized party %q is not allowed", authorizedParty)
			}
		}
	}
	email, _ := mapClaims["email"].(string)

	return &Claims{
		Subject: subject,
		Email:   email,
		Issuer:  issuer,
		Expiry:  expiresAt.Time,
		Raw:     cloneClaims(mapClaims),
	}, nil
}

func newVerifier(keys keySource, issuer string, authorizedParties []string) *Verifier {
	parties := make(map[string]struct{}, len(authorizedParties))
	for _, party := range authorizedParties {
		parties[party] = struct{}{}
	}
	return &Verifier{
		keys:              keys,
		issuer:            issuer,
		authorizedParties: parties,
		now:               time.Now,
	}
}

func commaSeparatedValues(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func normalizeAuthorizedParties(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	parties := make([]string, 0, len(values))
	for _, value := range values {
		party, err := normalizeOrigin(value)
		if err != nil {
			return nil, fmt.Errorf("clerk: invalid authorized party %q: %w", value, err)
		}
		if _, ok := seen[party]; ok {
			continue
		}
		seen[party] = struct{}{}
		parties = append(parties, party)
	}
	return parties, nil
}

func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("expected http or https scheme")
	}
	if parsed.Host == "" {
		return "", errors.New("missing host")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("expected an origin without credentials, path, query, or fragment")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
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
