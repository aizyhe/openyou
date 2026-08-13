package server_test

// Regression tests added by the adversarial audit. Each pins the fixed
// behavior of one confirmed bug so it cannot silently come back.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server"
)

// Bug: GET/DELETE /api/docs/{path...} were not restricted to .md while PUT
// was — DELETE could remove arbitrary files under DocsDir and GET could read
// them. Both must now reject non-.md paths with 400.
func TestDocsEndpointsRequireMarkdownSuffix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rule"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rule", "a.md"), []byte("# r"), 0644); err != nil {
		t.Fatal(err)
	}
	deps := server.Deps{Hub: bridge.NewHub(), Tools: tool.NewRegistry(), DocsDir: dir}
	h := server.NewRouter(deps)

	if w := doRequest(t, h, http.MethodGet, "/api/docs/secret.txt", nil); w.Code != http.StatusBadRequest {
		t.Errorf("GET non-md want 400, got %d", w.Code)
	}
	if w := doRequest(t, h, http.MethodDelete, "/api/docs/secret.txt", nil); w.Code != http.StatusBadRequest {
		t.Errorf("DELETE non-md want 400, got %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.txt")); err != nil {
		t.Errorf("non-md file must not be deleted: %v", err)
	}
	// The .md path still works.
	if w := doRequest(t, h, http.MethodGet, "/api/docs/rule/a.md", nil); w.Code != http.StatusOK {
		t.Errorf("GET md want 200, got %d", w.Code)
	}
	if w := doRequest(t, h, http.MethodDelete, "/api/docs/rule/a.md", nil); w.Code != http.StatusOK {
		t.Errorf("DELETE md want 200, got %d", w.Code)
	}
}

// Bug: POST /api/feedback accepted an empty session_id, creating orphan
// feedback rows (id like "-0") that pollute HasFeedback lookups.
func TestFeedbackRejectsEmptySessionID(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	w := doRequest(t, h, http.MethodPost, "/api/feedback",
		[]byte(`{"session_id":"","rating":"useful","turn_index":0}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty session_id want 400, got %d %s", w.Code, w.Body.String())
	}
}

// Bug: PUT /api/config stored garbage numeric values ("max_tokens":"abc")
// which the LLM client then silently ignored — the user believed the change
// took effect. Invalid numbers must now be rejected with 400.
func TestConfigRejectsInvalidNumericValues(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	cases := []string{
		`{"max_tokens":"abc"}`,
		`{"max_tokens":0}`,
		`{"max_tokens":99999999}`,
		`{"temperature":"xyz"}`,
		`{"temperature":7}`,
		`{"temperature":-1}`,
	}
	for _, body := range cases {
		if w := doRequest(t, h, http.MethodPut, "/api/config", []byte(body)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", body, w.Code)
		}
	}
	// Valid values still accepted.
	if w := doRequest(t, h, http.MethodPut, "/api/config",
		[]byte(`{"max_tokens":4096,"temperature":0.7}`)); w.Code != http.StatusOK {
		t.Errorf("valid config want 200, got %d %s", w.Code, w.Body.String())
	}
}

// Bug: backup restore silently dropped task description, priority and
// updated_at because the importer didn't read those fields from the JSON.
// (Now exercised through the v3 zip — the only supported import format.)
func TestBackupImportTaskPreservesAllFields(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	backup := buildCoreZip(t, nil, `{
		"version":"3.0",
		"tasks":[{
			"id":"tsk_00aa11","title":"ship it","description":"the full plan",
			"start_date":"2026-01-01","end_date":"2026-01-02","done":true,
			"priority":"high","source":"conversation","session_id":"ses_x",
			"created_at":111,"updated_at":222
		}]
	}`)
	w := doRequest(t, h, http.MethodPost, "/api/backup/import", backup)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	got, err := deps.Tasks.Get("tsk_00aa11")
	if err != nil {
		t.Fatalf("restored task missing: %v", err)
	}
	if got.Description != "the full plan" {
		t.Errorf("description lost on restore: %q", got.Description)
	}
	if got.Priority != "high" {
		t.Errorf("priority lost on restore: %q", got.Priority)
	}
	if got.UpdatedAt != 222 {
		t.Errorf("updated_at lost on restore: %d", got.UpdatedAt)
	}
	if got.Source != "conversation" || got.SessionID != "ses_x" {
		t.Errorf("source/session lost on restore: %+v", got)
	}
}

// Bug: mutating endpoints accepted any Content-Type, letting a malicious web
// page on the same device forge cross-origin writes with text/plain bodies
// (no CORS preflight). Bodies must now be declared application/json.
func TestMutationEndpointsRequireJSONContentType(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	// doRequest always sets application/json, so build the forged request
	// manually the way a cross-site page would send it.
	r1 := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"title":"x"}`))
	r1.Header.Set("Content-Type", "text/plain")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain body want 415, got %d", w1.Code)
	}
	// Same request with the correct Content-Type passes the guard (and the
	// handler itself accepts it).
	w2 := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(`{"title":"x"}`))
	if w2.Code != http.StatusOK {
		t.Errorf("json body want 200, got %d %s", w2.Code, w2.Body.String())
	}
	// Body-less mutations stay allowed (the UI's DELETEs send no body).
	w3 := doRequest(t, h, http.MethodDelete, "/api/tasks/nonexistent", nil)
	if w3.Code == http.StatusUnsupportedMediaType {
		t.Errorf("body-less DELETE must not be rejected by the content-type guard")
	}
}
