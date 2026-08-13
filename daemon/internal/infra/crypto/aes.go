// Package crypto provides AES-256-GCM at-rest encryption in pure Go (Android
// cross-compile); output is base64 so it fits a TEXT SQLite column.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

// NewCipher: 32-byte key comes from Android Keystore via JNI, or app-generated on first boot.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// encPrefix marks a value as encrypted so callers can distinguish ciphertext
// from legacy plaintext during migration (Decrypt tolerates unprefixed input).
const encPrefix = "enc:v1:"

func (c *Cipher) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plain, nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

func IsEncrypted(s string) bool {
	return len(s) >= len(encPrefix) && s[:len(encPrefix)] == encPrefix
}

// Decrypt treats an unprefixed value as legacy plaintext and returns it
// unchanged — enabling encryption on an existing DB doesn't corrupt old rows.
func (c *Cipher) Decrypt(stored string) ([]byte, error) {
	if !IsEncrypted(stored) {
		return []byte(stored), nil // legacy plaintext
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(encPrefix):])
	if err != nil {
		return nil, fmt.Errorf("crypto: base64: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plain, nil
}

func (c *Cipher) EncryptString(s string) (string, error) { return c.Encrypt([]byte(s)) }

func (c *Cipher) DecryptString(s string) (string, error) {
	b, err := c.Decrypt(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// GenerateKey is used on first boot when no Keystore-provided key exists.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("crypto: generate key: %w", err)
	}
	return key, nil
}
