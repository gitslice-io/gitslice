// Package secretbox encrypts secret values for storage.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	sealedPrefix   = "enc:v1:"
	sealedV2Prefix = "enc:v2:"
)

// Box seals and opens secret values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a standard or raw standard base64 encoding of exactly
// 32 key bytes.
func New(base64Key string) (*Box, error) {
	key, err := decodeBase64(base64Key)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("key must decode to exactly 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext in the enc:v1 storage format.
func (b *Box) Seal(plaintext string) (string, error) {
	return b.seal(sealedPrefix, plaintext, nil)
}

// SealWithAAD encrypts plaintext in the enc:v2 storage format, binding aad to
// the authenticated ciphertext so values cannot be transplanted across rows.
func (b *Box) SealWithAAD(plaintext string, aad []byte) (string, error) {
	return b.seal(sealedV2Prefix, plaintext, aad)
}

func (b *Box) seal(prefix, plaintext string, aad []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), aad)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts an enc:v1 value (or an enc:v2 value sealed with empty AAD).
// Unprefixed legacy plaintext is returned unchanged.
func (b *Box) Open(stored string) (string, error) {
	return b.OpenWithAAD(stored, nil)
}

// OpenWithAAD decrypts an enc:v2 value verifying aad, while keeping backward
// compatibility for pre-AAD enc:v1 values and unprefixed legacy plaintext.
func (b *Box) OpenWithAAD(stored string, aad []byte) (string, error) {
	switch {
	case strings.HasPrefix(stored, sealedV2Prefix):
		return b.openPayload(strings.TrimPrefix(stored, sealedV2Prefix), aad)
	case strings.HasPrefix(stored, sealedPrefix):
		return b.openPayload(strings.TrimPrefix(stored, sealedPrefix), nil)
	default:
		return stored, nil
	}
}

func (b *Box) openPayload(encoded string, aad []byte) (string, error) {
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode sealed value: %w", err)
	}
	nonceSize := b.aead.NonceSize()
	if len(payload) < nonceSize {
		return "", fmt.Errorf("sealed value is too short")
	}
	plaintext, err := b.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], aad)
	if err != nil {
		return "", fmt.Errorf("open sealed value: %w", err)
	}
	return string(plaintext), nil
}

// IsSealed reports whether stored carries a known sealed envelope prefix.
func IsSealed(stored string) bool {
	return strings.HasPrefix(stored, sealedPrefix) || strings.HasPrefix(stored, sealedV2Prefix)
}

func decodeBase64(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}
