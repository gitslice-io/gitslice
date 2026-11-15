package clerk

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"

	"github.com/golang-jwt/jwt/v5"
)

const maxJWKSSize = 1 << 20

type keySource interface {
	Key(context.Context, *jwt.Token) (any, error)
}

type remoteKeySource struct {
	url    string
	client *http.Client

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey
}

func (s *remoteKeySource) Key(ctx context.Context, token *jwt.Token) (any, error) {
	kid, err := tokenKeyID(token)
	if err != nil {
		return nil, err
	}
	if key := s.cachedKey(kid); key != nil {
		return key, nil
	}
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	if key := s.cachedKey(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("clerk: no JWKS key found for kid %q", kid)
}

func (s *remoteKeySource) cachedKey(kid string) *rsa.PublicKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.keys == nil {
		return nil
	}
	return s.keys[kid]
}

func (s *remoteKeySource) refresh(ctx context.Context) error {
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return fmt.Errorf("clerk: build JWKS request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("clerk: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clerk: fetch JWKS: unexpected HTTP status %s", resp.Status)
	}

	keys, err := parseJWKS(io.LimitReader(resp.Body, maxJWKSSize))
	if err != nil {
		return fmt.Errorf("clerk: parse JWKS: %w", err)
	}
	if len(keys) == 0 {
		return errors.New("clerk: JWKS contains no RSA signing keys")
	}

	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
	return nil
}

type staticKeySource map[string]*rsa.PublicKey

func (s staticKeySource) Key(_ context.Context, token *jwt.Token) (any, error) {
	kid, err := tokenKeyID(token)
	if err != nil {
		return nil, err
	}
	key := s[kid]
	if key == nil {
		return nil, fmt.Errorf("clerk: no JWKS key found for kid %q", kid)
	}
	return key, nil
}

func tokenKeyID(token *jwt.Token) (string, error) {
	if token == nil {
		return "", errors.New("clerk: token is nil")
	}
	if token.Method == nil || token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
		return "", fmt.Errorf("clerk: unexpected signing method %q", token.Header["alg"])
	}
	kid, _ := token.Header["kid"].(string)
	if strings.TrimSpace(kid) == "" {
		return "", errors.New("clerk: token missing kid header")
	}
	return kid, nil
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use,omitempty"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func newVerifierWithJWKS(jwksJSON []byte, issuer string, authorizedParties []string) (*Verifier, error) {
	keys, err := parseJWKSBytes(jwksJSON)
	if err != nil {
		return nil, err
	}
	return newVerifier(staticKeySource(keys), issuer, authorizedParties), nil
}

func parseJWKS(reader io.Reader) (map[string]*rsa.PublicKey, error) {
	var doc jwksDocument
	if err := json.NewDecoder(reader).Decode(&doc); err != nil {
		return nil, err
	}
	return rsaKeysFromJWKS(doc)
}

func parseJWKSBytes(jwksJSON []byte) (map[string]*rsa.PublicKey, error) {
	return parseJWKS(bytes.NewReader(jwksJSON))
}

func rsaKeysFromJWKS(doc jwksDocument) (map[string]*rsa.PublicKey, error) {
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, candidate := range doc.Keys {
		key, include, err := rsaKeyFromJWK(candidate)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		keys[candidate.Kid] = key
	}
	return keys, nil
}

func rsaKeyFromJWK(candidate jwk) (*rsa.PublicKey, bool, error) {
	if candidate.Kty != "RSA" {
		return nil, false, nil
	}
	if candidate.Use != "" && candidate.Use != "sig" {
		return nil, false, nil
	}
	if candidate.Alg != "" && candidate.Alg != jwt.SigningMethodRS256.Alg() {
		return nil, false, nil
	}
	if strings.TrimSpace(candidate.Kid) == "" {
		return nil, false, errors.New("RSA JWK missing kid")
	}
	if candidate.N == "" || candidate.E == "" {
		return nil, false, fmt.Errorf("RSA JWK %q missing modulus or exponent", candidate.Kid)
	}

	nBytes, err := base64.RawURLEncoding.DecodeString(candidate.N)
	if err != nil {
		return nil, false, fmt.Errorf("decode RSA modulus for kid %q: %w", candidate.Kid, err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(candidate.E)
	if err != nil {
		return nil, false, fmt.Errorf("decode RSA exponent for kid %q: %w", candidate.Kid, err)
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, false, fmt.Errorf("RSA JWK %q has empty modulus or exponent", candidate.Kid)
	}
	modulus := new(big.Int).SetBytes(nBytes)
	if modulus.Sign() <= 0 {
		return nil, false, fmt.Errorf("RSA JWK %q has invalid modulus", candidate.Kid)
	}

	exponent := new(big.Int).SetBytes(eBytes)
	if !exponent.IsInt64() || exponent.Sign() <= 0 {
		return nil, false, fmt.Errorf("RSA JWK %q has invalid exponent", candidate.Kid)
	}
	if exponent.BitLen() > 31 {
		return nil, false, fmt.Errorf("RSA JWK %q exponent is too large", candidate.Kid)
	}

	return &rsa.PublicKey{
		N: modulus,
		E: int(exponent.Int64()),
	}, true, nil
}
