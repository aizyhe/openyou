package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"openyou/daemon/internal/infra/config"
)

func testConfigStore(t *testing.T) *config.Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := config.NewStore(db)
	if err != nil {
		t.Fatalf("config store: %v", err)
	}
	return store
}

func TestStripJSONComments(t *testing.T) {
	src := `{
		// 行注释
		"api_key": "sk-//not-a-comment",  // 行尾注释
		"base_url": "https://api.deepseek.com/v1", /* 块注释 */
		"model": "deepseek-v4-pro"
	}`
	out := string(stripJSONComments([]byte(src)))
	if !contains(out, `"sk-//not-a-comment"`) {
		t.Errorf("string literal '//' was stripped: %q", out)
	}
	if contains(out, "行注释") || contains(out, "块注释") || contains(out, "行尾注释") {
		t.Errorf("comments not stripped: %q", out)
	}
	if contains(out, "/*") || contains(out, "// 行") {
		t.Errorf("comment markers leaked: %q", out)
	}
}

func TestApplyUserDataConfig_AppliesWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "openyou.jsonc", `{
		"api_key": "sk-file-key",
		"base_url": "https://api.example.com/v1",
		"model": "m-file",
		"max_tokens": 2048,
		"temperature": 0.3
	}`)
	store := testConfigStore(t)
	applyUserDataConfig(store, dir)

	if v, _ := store.Get("api_key"); v != "sk-file-key" {
		t.Errorf("api_key = %q, want sk-file-key", v)
	}
	if v, _ := store.Get("base_url"); v != "https://api.example.com/v1" {
		t.Errorf("base_url = %q", v)
	}
	if v, _ := store.Get("model"); v != "m-file" {
		t.Errorf("model = %q", v)
	}
	if v, _ := store.Get("max_tokens"); v != "2048" {
		t.Errorf("max_tokens = %q", v)
	}
	if v, _ := store.Get("temperature"); v != "0.3" {
		t.Errorf("temperature = %q", v)
	}
}

func TestApplyUserDataConfig_UserValueWins(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "openyou.json", `{ "api_key": "sk-file-key", "model": "m-file" }`)
	store := testConfigStore(t)
	if err := store.Set("api_key", "sk-user-key"); err != nil {
		t.Fatalf("set user api_key: %v", err)
	}

	applyUserDataConfig(store, dir)

	if v, _ := store.Get("api_key"); v != "sk-user-key" {
		t.Errorf("user api_key overwritten by file: %q", v)
	}
	// model was empty → file value still applies.
	if v, _ := store.Get("model"); v != "m-file" {
		t.Errorf("model = %q, want m-file", v)
	}
}

func TestApplyUserDataConfig_PrefersJSONOverJSONC(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "openyou.json", `{ "api_key": "sk-from-json" }`)
	writeTestConfig(t, dir, "openyou.jsonc", `{ "api_key": "sk-from-jsonc" }`)
	store := testConfigStore(t)
	applyUserDataConfig(store, dir)

	if v, _ := store.Get("api_key"); v != "sk-from-json" {
		t.Errorf("api_key = %q, want sk-from-json (json wins over jsonc)", v)
	}
}

func TestApplyUserDataConfig_NoFileNoop(t *testing.T) {
	dir := t.TempDir()
	store := testConfigStore(t)
	applyUserDataConfig(store, dir)
	if v, _ := store.Get("api_key"); v != "" {
		t.Errorf("api_key = %q, want empty", v)
	}
}

func writeTestConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
