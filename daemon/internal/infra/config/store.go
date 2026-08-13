package config

import (
	"database/sql"
	"fmt"
)

const schema = `
CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
`

// FieldCipher encrypts sensitive field values at rest. crypto.Cipher satisfies
// it; kept an interface so config doesn't hard-depend on crypto.
type FieldCipher interface {
	EncryptString(s string) (string, error)
	DecryptString(s string) (string, error)
}

type Store struct {
	db     *sql.DB
	cipher FieldCipher     // optional; when set, encryptedKeys are stored encrypted
	encKey map[string]bool // keys whose values are encrypted at rest
}

func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init config schema: %w", err)
	}
	return &Store{db: db, encKey: map[string]bool{}}, nil
}

// Existing plaintext rows stay readable (Decrypt tolerates unprefixed values)
// and re-encrypt on the next Set.
func (s *Store) EnableEncryption(c FieldCipher) {
	s.cipher = c
	s.encKey = map[string]bool{"api_key": true}
}

func (s *Store) shouldEncrypt(key string) bool { return s.cipher != nil && s.encKey[key] }

func (s *Store) Get(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get config %q: %w", key, err)
	}
	if s.shouldEncrypt(key) && value != "" {
		dec, derr := s.cipher.DecryptString(value)
		if derr != nil {
			return "", fmt.Errorf("decrypt config %q: %w", key, derr)
		}
		return dec, nil
	}
	return value, nil
}

func (s *Store) Set(key, value string) error {
	stored := value
	if s.shouldEncrypt(key) && value != "" {
		enc, eerr := s.cipher.EncryptString(value)
		if eerr != nil {
			return fmt.Errorf("encrypt config %q: %w", key, eerr)
		}
		stored = enc
	}
	_, err := s.db.Exec(
		`INSERT INTO config (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, stored,
	)
	if err != nil {
		return fmt.Errorf("set config %q: %w", key, err)
	}
	return nil
}

func (s *Store) GetAll() (map[string]string, error) {
	rows, err := s.db.Query("SELECT key, value FROM config ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("list config: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan config: %w", err)
		}
		if s.shouldEncrypt(k) && v != "" {
			dec, derr := s.cipher.DecryptString(v)
			if derr != nil {
				return nil, fmt.Errorf("decrypt config %q: %w", k, derr)
			}
			v = dec
		}
		result[k] = v
	}
	return result, rows.Err()
}
