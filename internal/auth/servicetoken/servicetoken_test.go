package servicetoken

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestMintVerifyRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	token, err := Mint(priv, "svc_test", "tester@example.com", "", time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	v, err := NewVerifier(Config{PublicKeyPEM: pub})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if v == nil {
		t.Fatal("verifier should be enabled when a key is configured")
	}
	claims, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "svc_test" || claims.Email != "tester@example.com" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.Issuer != DefaultIssuer {
		t.Fatalf("issuer = %q, want %q", claims.Issuer, DefaultIssuer)
	}
}

func TestDisabledWhenNoKey(t *testing.T) {
	v, err := NewVerifier(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Fatal("verifier should be nil (disabled) without a configured key")
	}
}

func TestRejectsWrongKey(t *testing.T) {
	privA, _, _ := GenerateKeyPair()
	_, pubB, _ := GenerateKeyPair()
	token, err := Mint(privA, "svc_test", "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(Config{PublicKeyPEM: pubB})
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected verification to fail against a different key")
	}
}

func TestRejectsExpired(t *testing.T) {
	privPEM, pub, _ := GenerateKeyPair()
	priv, err := ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatal(err)
	}
	// Sign a token whose exp is in the past (Mint guards against negative ttl).
	past := time.Now().Add(-time.Hour)
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"sub": "svc_test",
		"iss": DefaultIssuer,
		"iat": past.Add(-time.Hour).Unix(),
		"exp": past.Unix(),
	}).SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(Config{PublicKeyPEM: pub})
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestRejectsWrongIssuer(t *testing.T) {
	priv, pub, _ := GenerateKeyPair()
	token, err := Mint(priv, "svc_test", "", "other-issuer", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(Config{PublicKeyPEM: pub, Issuer: DefaultIssuer})
	if _, err := v.Verify(context.Background(), token); err == nil {
		t.Fatal("expected issuer mismatch to be rejected")
	}
}
