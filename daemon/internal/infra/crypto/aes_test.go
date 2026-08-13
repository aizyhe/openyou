package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, _ := GenerateKey()
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	for _, plain := range []string{"", "sk-abc123", "张伟的秘密 API key 🔑", strings.Repeat("x", 5000)} {
		enc, err := c.EncryptString(plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		if plain != "" && !IsEncrypted(enc) {
			t.Errorf("encrypted value missing marker: %q", enc)
		}
		if plain != "" && strings.Contains(enc, plain) {
			t.Errorf("ciphertext leaks plaintext: %q", enc)
		}
		dec, err := c.DecryptString(enc)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if dec != plain {
			t.Errorf("round-trip mismatch: want %q got %q", plain, dec)
		}
	}
}

func TestDecryptLegacyPlaintext(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)
	// A value without the enc marker (pre-encryption row) is returned as-is.
	got, err := c.DecryptString("plain-old-value")
	if err != nil || got != "plain-old-value" {
		t.Errorf("legacy plaintext should pass through, got %q err=%v", got, err)
	}
}

func TestNonceIsRandom(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)
	a, _ := c.EncryptString("same input")
	b, _ := c.EncryptString("same input")
	if a == b {
		t.Errorf("two encryptions of the same plaintext must differ (random nonce)")
	}
}

func TestWrongKeyFails(t *testing.T) {
	k1, _ := GenerateKey()
	k2, _ := GenerateKey()
	c1, _ := NewCipher(k1)
	c2, _ := NewCipher(k2)
	enc, _ := c1.EncryptString("secret")
	if _, err := c2.DecryptString(enc); err == nil {
		t.Errorf("decrypting with the wrong key must fail")
	}
}

func TestBadKeySize(t *testing.T) {
	if _, err := NewCipher([]byte("short")); err == nil {
		t.Errorf("non-32-byte key must be rejected")
	}
}

func TestDecryptCorruptBase64(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)
	// Valid enc prefix but the payload is not valid base64.
	_, err := c.DecryptString(encPrefix + "!!!not base64!!!")
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("expected base64 error, got %v", err)
	}
}

func TestDecryptShortCiphertext(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)
	// Base64-valid but fewer bytes than the GCM nonce size → "too short".
	short := base64.StdEncoding.EncodeToString([]byte("abc"))
	_, err := c.DecryptString(encPrefix + short)
	if err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("expected too-short error, got %v", err)
	}
}

func TestDecryptTampered(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)
	enc, _ := c.EncryptString("secret data")
	// Flip a byte in the base64 payload → GCM authentication fails on Open.
	raw := enc[len(encPrefix):]
	b, _ := base64.StdEncoding.DecodeString(raw)
	b[len(b)-1] ^= 0xFF
	tampered := encPrefix + base64.StdEncoding.EncodeToString(b)
	_, err := c.DecryptString(tampered)
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("expected open/auth error on tampered ciphertext, got %v", err)
	}
}

func TestNewCipherKeySizes(t *testing.T) {
	// Too long is also rejected.
	if _, err := NewCipher(make([]byte, 33)); err == nil {
		t.Error("33-byte key must be rejected")
	}
	// Exactly 32 bytes succeeds.
	if _, err := NewCipher(make([]byte, 32)); err != nil {
		t.Errorf("32-byte key must succeed, got %v", err)
	}
}

func TestIsEncrypted(t *testing.T) {
	if IsEncrypted("plain") {
		t.Error("plain string should not be marked encrypted")
	}
	if IsEncrypted("") {
		t.Error("empty string should not be marked encrypted")
	}
	if !IsEncrypted(encPrefix + "payload") {
		t.Error("prefixed string should be marked encrypted")
	}
}

func TestGenerateKeyLength(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(k) != 32 {
		t.Errorf("GenerateKey len = %d, want 32", len(k))
	}
}

// failReader always errors, used to exercise the rand.Reader failure branches.
type failReader struct{}

func (failReader) Read(p []byte) (int, error) { return 0, errors.New("rand failure") }

func TestEncryptNonceError(t *testing.T) {
	orig := rand.Reader
	rand.Reader = failReader{}
	defer func() { rand.Reader = orig }()

	key := make([]byte, 32)
	c, _ := NewCipher(key)
	if _, err := c.Encrypt([]byte("data")); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce error, got %v", err)
	}
}

func TestGenerateKeyError(t *testing.T) {
	orig := rand.Reader
	rand.Reader = failReader{}
	defer func() { rand.Reader = orig }()

	if _, err := GenerateKey(); err == nil || !strings.Contains(err.Error(), "generate key") {
		t.Fatalf("expected generate key error, got %v", err)
	}
}
