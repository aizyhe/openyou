package config

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newTestConfigStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestNewStoreCreatesTable(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore should return a non-nil store")
	}

	// Verify the config table exists by querying sqlite_master.
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='config'").Scan(&name)
	if err != nil {
		t.Fatalf("config table not found: %v", err)
	}
	if name != "config" {
		t.Errorf("expected table name 'config', got %q", name)
	}
}

func TestSetAndGet(t *testing.T) {
	store := newTestConfigStore(t)

	// Set a key.
	if err := store.Set("api_key", "sk-test-12345"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get the key.
	val, err := store.Get("api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-test-12345" {
		t.Errorf("Get api_key = %q, want sk-test-12345", val)
	}
}

func TestGetNonExistent(t *testing.T) {
	store := newTestConfigStore(t)

	val, err := store.Get("nonexistent_key")
	if err != nil {
		t.Errorf("Get nonexistent should not error: %v", err)
	}
	if val != "" {
		t.Errorf("Get nonexistent should return empty string, got %q", val)
	}
}

func TestSetOverwrites(t *testing.T) {
	store := newTestConfigStore(t)

	// Initial set.
	if err := store.Set("model", "deepseek-chat"); err != nil {
		t.Fatalf("first Set: %v", err)
	}

	// Overwrite.
	if err := store.Set("model", "deepseek-v4-pro"); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	val, err := store.Get("model")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "deepseek-v4-pro" {
		t.Errorf("Get model after overwrite = %q, want deepseek-v4-pro", val)
	}
}

func TestSetEmptyValue(t *testing.T) {
	store := newTestConfigStore(t)

	// Set a key with a value first.
	if err := store.Set("test_key", "initial"); err != nil {
		t.Fatalf("first Set: %v", err)
	}

	// Overwrite with empty string.
	if err := store.Set("test_key", ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}

	val, err := store.Get("test_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("Get after Set empty should return empty string, got %q", val)
	}
}

func TestGetAll(t *testing.T) {
	store := newTestConfigStore(t)

	// Initially empty.
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("GetAll on empty store should return empty map, got %d entries", len(all))
	}

	// Add entries.
	entries := map[string]string{
		"api_key":  "sk-test",
		"base_url": "https://api.deepseek.com/v1",
		"model":    "deepseek-v4-pro",
	}
	for k, v := range entries {
		if err := store.Set(k, v); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	all, err = store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("GetAll len = %d, want 3", len(all))
	}
	for k, expected := range entries {
		if got := all[k]; got != expected {
			t.Errorf("GetAll[%q] = %q, want %q", k, got, expected)
		}
	}
}

func TestGetAllOrdered(t *testing.T) {
	store := newTestConfigStore(t)

	// Insert in reverse alphabetical order.
	keys := []string{"z_key", "a_key", "m_key"}
	for i, k := range keys {
		if err := store.Set(k, "value"); err != nil {
			t.Fatalf("Set %q (index %d): %v", k, i, err)
		}
	}

	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	// GetAll uses ORDER BY key, so keys should be alphabetical.
	// We can't iterate over map in order, but we can verify all keys exist.
	if len(all) != 3 {
		t.Fatalf("GetAll len = %d, want 3", len(all))
	}
	for _, k := range keys {
		if _, ok := all[k]; !ok {
			t.Errorf("GetAll missing key %q", k)
		}
	}
}

func TestSpecialCharacters(t *testing.T) {
	store := newTestConfigStore(t)

	specialKey := "key_with_special_!@#$%^&*()"
	specialVal := "value\nwith\tnewlines and \"quotes\""

	if err := store.Set(specialKey, specialVal); err != nil {
		t.Fatalf("Set special: %v", err)
	}

	val, err := store.Get(specialKey)
	if err != nil {
		t.Fatalf("Get special: %v", err)
	}
	if val != specialVal {
		t.Errorf("Get special = %q, want %q", val, specialVal)
	}
}

func TestLongValue(t *testing.T) {
	store := newTestConfigStore(t)

	// 10KB value.
	longVal := make([]byte, 10240)
	for i := range longVal {
		longVal[i] = byte('a' + (i % 26))
	}
	longStr := string(longVal)

	if err := store.Set("long_key", longStr); err != nil {
		t.Fatalf("Set long: %v", err)
	}

	val, err := store.Get("long_key")
	if err != nil {
		t.Fatalf("Get long: %v", err)
	}
	if val != longStr {
		t.Errorf("Get long mismatch: len got=%d, want=%d", len(val), len(longStr))
	}
}

func TestMultipleSetsSameKey(t *testing.T) {
	store := newTestConfigStore(t)

	for i := 0; i < 10; i++ {
		val := "value_" + string(rune('0'+i))
		if err := store.Set("counter", val); err != nil {
			t.Fatalf("Set #%d: %v", i, err)
		}
	}

	val, err := store.Get("counter")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "value_9" {
		t.Errorf("After 10 sets, value = %q, want value_9", val)
	}
}

func TestIsolationAcrossStores(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store1, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	store2, err := NewStore(db)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}

	// Set via store1, read via store2 (same underlying DB).
	if err := store1.Set("shared_key", "hello"); err != nil {
		t.Fatalf("store1.Set: %v", err)
	}

	val, err := store2.Get("shared_key")
	if err != nil {
		t.Fatalf("store2.Get: %v", err)
	}
	if val != "hello" {
		t.Errorf("store2.Get shared_key = %q, want hello (shared DB)", val)
	}
}

// stubCipher is a trivial reversible transform for testing the encryption hook
// without pulling in the crypto package (keeps config a leaf in tests).
type stubCipher struct{}

func (stubCipher) EncryptString(s string) (string, error) { return "ENC(" + s + ")", nil }
func (stubCipher) DecryptString(s string) (string, error) {
	if strings.HasPrefix(s, "ENC(") && strings.HasSuffix(s, ")") {
		return s[4 : len(s)-1], nil
	}
	return s, nil // legacy plaintext
}

func TestApiKeyStoredEncrypted(t *testing.T) {
	db, _ := sql.Open("sqlite3", ":memory:")
	t.Cleanup(func() { db.Close() })
	s, _ := NewStore(db)
	s.EnableEncryption(stubCipher{})

	if err := s.Set("api_key", "sk-secret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Raw DB value must be encrypted, not plaintext.
	var raw string
	_ = db.QueryRow("SELECT value FROM config WHERE key='api_key'").Scan(&raw)
	if raw == "sk-secret" || !strings.HasPrefix(raw, "ENC(") {
		t.Errorf("api_key stored unencrypted: %q", raw)
	}
	// Get transparently decrypts.
	got, _ := s.Get("api_key")
	if got != "sk-secret" {
		t.Errorf("Get should decrypt: got %q", got)
	}
}

func TestNonSensitiveKeyNotEncrypted(t *testing.T) {
	db, _ := sql.Open("sqlite3", ":memory:")
	t.Cleanup(func() { db.Close() })
	s, _ := NewStore(db)
	s.EnableEncryption(stubCipher{})

	_ = s.Set("model", "deepseek-v4-pro")
	var raw string
	_ = db.QueryRow("SELECT value FROM config WHERE key='model'").Scan(&raw)
	if raw != "deepseek-v4-pro" {
		t.Errorf("non-sensitive key should be plaintext, got %q", raw)
	}
}

func TestGetAllDecryptsApiKey(t *testing.T) {
	db, _ := sql.Open("sqlite3", ":memory:")
	t.Cleanup(func() { db.Close() })
	s, _ := NewStore(db)
	s.EnableEncryption(stubCipher{})
	_ = s.Set("api_key", "sk-xyz")
	all, _ := s.GetAll()
	if all["api_key"] != "sk-xyz" {
		t.Errorf("GetAll should decrypt api_key, got %q", all["api_key"])
	}
}
