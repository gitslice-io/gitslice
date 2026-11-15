package clerk

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testClerkIssuer          = "https://amusing-ram-19.clerk.accounts.dev"
	testClerkAuthorizedParty = "https://app.example.com"
)

func TestVerifyAcceptsValidTokenFromInjectedJWKS(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	privateKey := generateRSAKey(t)
	kid := "test-key"

	verifier := verifierWithKey(t, kid, &privateKey.PublicKey)
	verifier.now = func() time.Time { return now }

	token := signToken(t, privateKey, kid, jwt.MapClaims{
		"sub":   "user_123",
		"email": "ada@example.com",
		"iss":   testClerkIssuer,
		"azp":   testClerkAuthorizedParty,
		"exp":   now.Add(time.Hour).Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
	})

	claims, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "user_123" {
		t.Fatalf("Subject = %q, want user_123", claims.Subject)
	}
	if claims.Email != "ada@example.com" {
		t.Fatalf("Email = %q, want ada@example.com", claims.Email)
	}
	if claims.Issuer != testClerkIssuer {
		t.Fatalf("Issuer = %q", claims.Issuer)
	}
	if !claims.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("Expiry = %s, want %s", claims.Expiry, now.Add(time.Hour))
	}
	if claims.Raw["sub"] != "user_123" {
		t.Fatalf("Raw sub = %v, want user_123", claims.Raw["sub"])
	}
}

func TestVerifyRejectsWrongOrMissingIssuer(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	privateKey := generateRSAKey(t)
	kid := "test-key"
	verifier := verifierWithKey(t, kid, &privateKey.PublicKey)
	verifier.now = func() time.Time { return now }

	for _, tc := range []struct {
		name   string
		issuer any
	}{
		{name: "wrong", issuer: "https://attacker.example.com"},
		{name: "missing", issuer: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{
				"sub": "user_123",
				"azp": testClerkAuthorizedParty,
				"exp": now.Add(time.Hour).Unix(),
				"nbf": now.Add(-time.Minute).Unix(),
			}
			if tc.issuer != nil {
				claims["iss"] = tc.issuer
			}
			token := signToken(t, privateKey, kid, claims)
			if _, err := verifier.Verify(context.Background(), token); err == nil {
				t.Fatal("Verify() error = nil, want issuer validation error")
			}
		})
	}
}

func TestVerifyRequiresAuthorizedParty(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	privateKey := generateRSAKey(t)
	kid := "test-key"
	verifier := verifierWithKey(t, kid, &privateKey.PublicKey)
	verifier.now = func() time.Time { return now }

	baseClaims := jwt.MapClaims{
		"sub": "user_123",
		"iss": testClerkIssuer,
		"exp": now.Add(time.Hour).Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
	}

	for _, tc := range []struct {
		name string
		set  bool
		azp  any
	}{
		{name: "unexpected", set: true, azp: "https://attacker.example.com"},
		{name: "missing", set: false},
		{name: "empty", set: true, azp: ""},
		{name: "whitespace", set: true, azp: "   "},
		{name: "non-string", set: true, azp: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := cloneMapClaims(baseClaims)
			if tc.set {
				claims["azp"] = tc.azp
			}
			token := signToken(t, privateKey, kid, claims)
			if _, err := verifier.Verify(context.Background(), token); err == nil {
				t.Fatalf("Verify() error = nil, want authorized-party validation error for %s", tc.name)
			}
		})
	}
}

func TestRemoteJWKSRefreshSingleflightAndCooldown(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	key1 := generateRSAKey(t)
	key2 := generateRSAKey(t)

	var fetches atomic.Int32
	var serveSecondKey atomic.Bool
	gate := make(chan struct{})
	var closeOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		<-gate
		w.Header().Set("Content-Type", "application/json")
		if serveSecondKey.Load() {
			_, _ = w.Write(jwksForKey(t, "kid-2", &key2.PublicKey))
			return
		}
		_, _ = w.Write(jwksForKey(t, "kid-1", &key1.PublicKey))
	}))
	defer srv.Close()

	var currentNow atomic.Int64
	currentNow.Store(now.UnixNano())
	clock := func() time.Time { return time.Unix(0, currentNow.Load()).UTC() }

	src := &remoteKeySource{
		url:    srv.URL,
		client: srv.Client(),
		now:    clock,
	}
	verifier := newVerifier(src, testClerkIssuer, []string{testClerkAuthorizedParty})
	verifier.now = clock

	goodToken := signToken(t, key1, "kid-1", jwt.MapClaims{
		"sub": "user_1",
		"iss": testClerkIssuer,
		"azp": testClerkAuthorizedParty,
		"exp": now.Add(time.Hour).Unix(),
	})

	// 16 concurrent verifications on cold cache must coalesce into 1 HTTP fetch.
	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := verifier.Verify(context.Background(), goodToken)
			errs <- err
		}()
	}
	time.Sleep(25 * time.Millisecond)
	closeOnce.Do(func() { close(gate) })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Verify() error = %v", err)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1", got)
	}

	// Unknown kid storms within the 30s cooldown must NOT trigger extra fetches.
	unknownToken := signToken(t, key2, "kid-2", jwt.MapClaims{
		"sub": "user_2",
		"iss": testClerkIssuer,
		"azp": testClerkAuthorizedParty,
		"exp": now.Add(time.Hour).Unix(),
	})
	serveSecondKey.Store(true)
	for i := 0; i < 10; i++ {
		if _, err := verifier.Verify(context.Background(), unknownToken); err == nil {
			t.Fatal("Verify() succeeded before key rotation cooldown elapsed")
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetches during cooldown = %d, want 1", got)
	}

	// Once cooldown elapses, rotated key is fetched and accepted.
	currentNow.Store(now.Add(jwksRefreshCooldown + time.Second).UnixNano())
	if _, err := verifier.Verify(context.Background(), unknownToken); err != nil {
		t.Fatalf("Verify() after cooldown error = %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetches after cooldown = %d, want 2", got)
	}
}

func TestRemoteJWKSHonorsBodyLimitAndDefaultTimeout(t *testing.T) {
	if defaultJWKSClient.Timeout != jwksFetchTimeout {
		t.Fatalf("defaultJWKSClient.Timeout = %v, want %v", defaultJWKSClient.Timeout, jwksFetchTimeout)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"x","n":"` + strings.Repeat("A", maxJWKSSize+1024) + `","e":"AQAB"}]}`))
	}))
	defer srv.Close()

	verifier, err := NewVerifier(Config{
		JWKSURL:           srv.URL,
		Issuer:            testClerkIssuer,
		AuthorizedParties: []string{testClerkAuthorizedParty},
	})
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	key := generateRSAKey(t)
	token := signToken(t, key, "x", jwt.MapClaims{
		"sub": "user_1",
		"iss": testClerkIssuer,
		"azp": testClerkAuthorizedParty,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() error = nil on oversized JWKS response")
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	privateKey := generateRSAKey(t)
	kid := "test-key"

	verifier := verifierWithKey(t, kid, &privateKey.PublicKey)
	verifier.now = func() time.Time { return now }

	token := signToken(t, privateKey, kid, jwt.MapClaims{
		"sub": "user_123",
		"exp": now.Add(-time.Minute).Unix(),
		"nbf": now.Add(-time.Hour).Unix(),
	})

	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() error = nil, want expired token error")
	}
}

func TestVerifyRejectsTokenSignedByDifferentKey(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	trustedKey := generateRSAKey(t)
	attackerKey := generateRSAKey(t)
	kid := "test-key"

	verifier := verifierWithKey(t, kid, &trustedKey.PublicKey)
	verifier.now = func() time.Time { return now }

	token := signToken(t, attackerKey, kid, jwt.MapClaims{
		"sub": "user_123",
		"exp": now.Add(time.Hour).Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
	})

	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() error = nil, want signature verification error")
	}
}

func TestJWKSURLFromPublishableKey(t *testing.T) {
	got, err := jwksURLFromPublishableKey("pk_test_YW11c2luZy1yYW0tMTkuY2xlcmsuYWNjb3VudHMuZGV2JA")
	if err != nil {
		t.Fatalf("jwksURLFromPublishableKey() error = %v", err)
	}

	want := "https://amusing-ram-19.clerk.accounts.dev/.well-known/jwks.json"
	if got != want {
		t.Fatalf("jwksURLFromPublishableKey() = %q, want %q", got, want)
	}
}

func TestConfigFromEnvReadsSessionValidationSettings(t *testing.T) {
	t.Setenv("CLERK_ISSUER", testClerkIssuer)
	t.Setenv("CLERK_AUTHORIZED_PARTIES", " https://app.example.com, http://127.0.0.1:5173 ,, ")

	cfg := ConfigFromEnv()
	if cfg.Issuer != testClerkIssuer {
		t.Fatalf("Issuer = %q, want %q", cfg.Issuer, testClerkIssuer)
	}
	want := []string{"https://app.example.com", "http://127.0.0.1:5173"}
	if len(cfg.AuthorizedParties) != len(want) {
		t.Fatalf("AuthorizedParties = %#v, want %#v", cfg.AuthorizedParties, want)
	}
	for i := range want {
		if cfg.AuthorizedParties[i] != want[i] {
			t.Fatalf("AuthorizedParties = %#v, want %#v", cfg.AuthorizedParties, want)
		}
	}
}

func TestNewVerifierRequiresAuthorizedParties(t *testing.T) {
	_, err := NewVerifier(Config{
		JWKSURL: "https://clerk.example.com/.well-known/jwks.json",
		Issuer:  "https://clerk.example.com",
	})
	if err == nil {
		t.Fatal("NewVerifier() error = nil, want missing authorized parties error")
	}
}

func verifierWithKey(t *testing.T, kid string, publicKey *rsa.PublicKey) *Verifier {
	t.Helper()

	verifier, err := newVerifierWithJWKS(
		jwksForKey(t, kid, publicKey),
		testClerkIssuer,
		[]string{testClerkAuthorizedParty},
	)
	if err != nil {
		t.Fatalf("newVerifierWithJWKS() error = %v", err)
	}
	return verifier
}

func cloneMapClaims(claims jwt.MapClaims) jwt.MapClaims {
	out := make(jwt.MapClaims, len(claims))
	for key, value := range claims {
		out[key] = value
	}
	return out
}

func generateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	return key
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func jwksForKey(t *testing.T, kid string, publicKey *rsa.PublicKey) []byte {
	t.Helper()

	doc := jwksDocument{
		Keys: []jwk{{
			Kty: "RSA",
			Use: "sig",
			Kid: kid,
			Alg: jwt.SigningMethodRS256.Alg(),
			N:   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
		}},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return out
}
