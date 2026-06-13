package clerk

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
		"iss":   "https://amusing-ram-19.clerk.accounts.dev",
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
	if claims.Issuer != "https://amusing-ram-19.clerk.accounts.dev" {
		t.Fatalf("Issuer = %q", claims.Issuer)
	}
	if !claims.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("Expiry = %s, want %s", claims.Expiry, now.Add(time.Hour))
	}
	if claims.Raw["sub"] != "user_123" {
		t.Fatalf("Raw sub = %v, want user_123", claims.Raw["sub"])
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

func verifierWithKey(t *testing.T, kid string, publicKey *rsa.PublicKey) *Verifier {
	t.Helper()

	verifier, err := newVerifierWithJWKS(jwksForKey(t, kid, publicKey))
	if err != nil {
		t.Fatalf("newVerifierWithJWKS() error = %v", err)
	}
	return verifier
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
