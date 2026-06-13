// Package servicetoken implements an optional asymmetric-key auth path used for
// automated testing and service accounts. A holder of the Ed25519 private key
// mints short-lived JWTs (EdDSA); the server verifies them with the configured
// public key as an alternative to the interactive Clerk flow.
//
// It is disabled unless GITSLICE_SERVICE_JWT_PUBLIC_KEY (or _FILE) is configured,
// so it is an explicit opt-in test affordance, not a default backdoor. The
// private key must be kept secret; anyone holding it can authenticate as any
// subject, so only enable this in non-production environments.
package servicetoken

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultIssuer is the `iss` claim used when none is configured.
const DefaultIssuer = "gitslice-service"

type Config struct {
	// PublicKeyPEM is a PEM-encoded PKIX Ed25519 public key. When empty the
	// verifier is disabled.
	PublicKeyPEM string
	Issuer       string
}

// ConfigFromEnv reads GITSLICE_SERVICE_JWT_PUBLIC_KEY (PEM literal) or
// GITSLICE_SERVICE_JWT_PUBLIC_KEY_FILE (path), and GITSLICE_SERVICE_JWT_ISSUER.
func ConfigFromEnv() Config {
	keyPEM := os.Getenv("GITSLICE_SERVICE_JWT_PUBLIC_KEY")
	if strings.TrimSpace(keyPEM) == "" {
		if path := os.Getenv("GITSLICE_SERVICE_JWT_PUBLIC_KEY_FILE"); path != "" {
			if data, err := os.ReadFile(path); err == nil {
				keyPEM = string(data)
			}
		}
	}
	issuer := os.Getenv("GITSLICE_SERVICE_JWT_ISSUER")
	if issuer == "" {
		issuer = DefaultIssuer
	}
	return Config{PublicKeyPEM: keyPEM, Issuer: issuer}
}

type Claims struct {
	Subject string
	Email   string
	Issuer  string
	Expiry  time.Time
}

type Verifier struct {
	pub    ed25519.PublicKey
	issuer string
}

// NewVerifier returns (nil, nil) when no public key is configured, signalling the
// service-token path is disabled.
func NewVerifier(cfg Config) (*Verifier, error) {
	if strings.TrimSpace(cfg.PublicKeyPEM) == "" {
		return nil, nil
	}
	pub, err := ParsePublicKey(cfg.PublicKeyPEM)
	if err != nil {
		return nil, err
	}
	issuer := cfg.Issuer
	if issuer == "" {
		issuer = DefaultIssuer
	}
	return &Verifier{pub: pub, issuer: issuer}, nil
}

// Verify cryptographically validates an EdDSA service token, checks issuer and
// expiry, and returns its claims.
func (v *Verifier) Verify(_ context.Context, token string) (*Claims, error) {
	parsed, err := jwt.Parse(
		token,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
				return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
			}
			return v.pub, nil
		},
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid service token")
	}
	subject, _ := claims["sub"].(string)
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("service token missing sub")
	}
	email, _ := claims["email"].(string)
	out := &Claims{Subject: subject, Email: email, Issuer: v.issuer}
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		out.Expiry = exp.Time
	}
	return out, nil
}

// Mint signs an EdDSA service token with the given PKCS8 Ed25519 private key.
func Mint(privateKeyPEM, subject, email, issuer string, ttl time.Duration) (string, error) {
	priv, err := ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(subject) == "" {
		return "", errors.New("subject is required")
	}
	if issuer == "" {
		issuer = DefaultIssuer
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": subject,
		"iss": issuer,
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	if email != "" {
		claims["email"] = email
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
}

// GenerateKeyPair returns a fresh Ed25519 keypair as PEM (PKCS8 private, PKIX public).
func GenerateKeyPair() (privatePEM, publicPEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privatePEM, publicPEM, nil
}

func ParsePublicKey(pemStr string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, errors.New("invalid public key PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want ed25519", parsed)
	}
	return pub, nil
}

func ParsePrivateKey(pemStr string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want ed25519", parsed)
	}
	return priv, nil
}
