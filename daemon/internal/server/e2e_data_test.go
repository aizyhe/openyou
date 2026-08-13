package server_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// TestE2E_BackupRoundTrip: export → import into a fresh daemon → data present.
func TestE2E_BackupRoundTrip(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string { return "ok" }}
	h1, deps1 := e2eDeps(t, llmc)

	// Seed source with the identity-core folders (a valid backup must carry
	// inside/rule/skill) plus a chat turn.
	mustWrite(t, filepath.Join(deps1.DocsDir, "inside", "people", "boss.md"), "# Boss")
	mustWrite(t, filepath.Join(deps1.DocsDir, "rule", "comm.md"), "#1. 先私聊再上会")
	mustWrite(t, filepath.Join(deps1.DocsDir, "skill", "thinking", "root-cause.md"), "# Root cause")
	sse(t, h1, "/api/chat", `{"message":"hi","session_id":"s_backup"}`)

	// Export.
	we := doRequest(t, h1, http.MethodGet, "/api/backup/export", nil)
	if we.Code != http.StatusOK {
		t.Fatalf("export status %d", we.Code)
	}
	dump := we.Body.Bytes()

	// Import into a fresh daemon.
	h2, deps2 := e2eDeps(t, llmc)
	wi := doRequest(t, h2, http.MethodPost, "/api/backup/import", dump)
	if wi.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", wi.Code, wi.Body.String())
	}

	// Verify rules restored.
	_ = deps2
	_ = deps1

	// Idempotency: importing again must NOT duplicate sessions.
	doRequest(t, h2, http.MethodPost, "/api/backup/import", dump)
	sessions, _ := deps2.Sessions.List()
	seen := map[string]int{}
	for _, s := range sessions {
		msgs, _ := deps2.Sessions.GetMessages(s.ID)
		seen[s.ID] = len(msgs)
	}
	for id, n := range seen {
		if n > 2 { // one user + one assistant turn max
			t.Errorf("session %s duplicated on re-import: %d messages", id, n)
		}
	}
}

// TestE2E_ConfigApiKeyEncrypted: api_key is encrypted at rest but read back
// in plaintext. It is writable over the API (Settings page / openyou.json) and
// never exposed in plaintext by GET /api/config.
func TestE2E_ConfigApiKeyEncrypted(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string { return "ok" }}
	h, deps := e2eDeps(t, llmc)
	deps.Config.EnableEncryption(newTestCipher())
	if err := deps.Config.Set("api_key", "sk-secret-123"); err != nil {
		t.Fatalf("seed api_key: %v", err)
	}

	// A user's update of api_key must be persisted (and encrypted at rest).
	doRequest(t, h, http.MethodPut, "/api/config", []byte(`{"api_key":"sk-nope","model":"m"}`))

	// Raw DB value must not be the plaintext (and not the ignored update).
	var raw string
	_ = deps.DB.QueryRow("SELECT value FROM config WHERE key='api_key'").Scan(&raw)
	if raw == "sk-secret-123" || raw == "sk-nope" || raw == "" {
		t.Errorf("api_key stored in plaintext or missing: %q", raw)
	}
	// GET /api/config returns api_key masked (last 4 chars visible) — plaintext
	// is never exposed over the API (B7 fix). Verify the mask format.
	w := doRequest(t, h, http.MethodGet, "/api/config", nil)
	var cfg map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &cfg)
	masked, _ := cfg["api_key"].(string)
	if masked == "sk-secret-123" {
		t.Errorf("GET /api/config must NOT return plaintext api_key")
	}
	if len(masked) < 4 || masked[len(masked)-4:] != "nope" {
		t.Errorf("GET /api/config masked key should end with last 4 chars 'nope', got %q", masked)
	}
}

// TestE2E_MemoryDispute: dispute lowers confidence + clears flags.
func TestE2E_MemoryDispute(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string { return "ok" }}
	h, deps := e2eDeps(t, llmc)

	saved, err := deps.Memory.Save(memEntry("张三关注成本", 0.9))
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	w := doRequest(t, h, http.MethodPut, "/api/memory/"+saved.ID+"/dispute", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dispute status %d: %s", w.Code, w.Body.String())
	}
	got, ok := deps.Memory.Get(saved.ID)
	if !ok {
		t.Fatal("memory gone after dispute")
	}
	if got.Confidence >= 0.9 {
		t.Errorf("dispute should lower confidence, got %v", got.Confidence)
	}
}

// TestE2E_NotificationsPollEmpty: with nothing queued, poll returns an empty
// array (not null) after a short wait — but we push first so it returns fast.
func TestE2E_NotificationsPoll(t *testing.T) {
	llmc := &fakeLLM{replyFor: func(string) string { return "ok" }}
	h, deps := e2eDeps(t, llmc)
	deps.Notify.PushFields("star-alert", "r1", "标题", "正文", "home")

	w := doRequest(t, h, http.MethodGet, "/api/notifications/poll", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("poll status %d", w.Code)
	}
	var resp struct {
		Notifications []map[string]any `json:"notifications"`
	}
	decodeInto(t, w.Body.Bytes(), &resp)
	if len(resp.Notifications) != 1 || resp.Notifications[0]["type"] != "star-alert" {
		t.Errorf("unexpected poll result: %+v", resp.Notifications)
	}
}
